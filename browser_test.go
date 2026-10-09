package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBrowserActionAndPlan(t *testing.T) {
	for _, c := range []struct {
		tool  string
		input any
		want  string
	}{
		{"browser_navigate", map[string]any{"url": "https://playwright.dev/docs"}, "Open playwright.dev/docs"},
		{"browser_click", map[string]any{"element": "Sign in button"}, "Click Sign in button"},
		{"browser_click", map[string]any{"element": "row", "doubleClick": true}, "Double-click row"},
		{"browser_type", map[string]any{"element": "Search box", "text": "go test"}, `Type "go test" into Search box`},
		{"browser_fill_form", map[string]any{"fields": []any{1, 2}}, "Fill in 2 form fields"},
		{"browser_snapshot", map[string]any{}, "Read the page"},
		{"browser_tabs", map[string]any{"action": "new"}, "Open a new tab"},
		{"browser_something_new", nil, "something new"},
	} {
		if got := browserAction(c.tool, c.input); got != c.want {
			t.Errorf("%s: %q, want %q", c.tool, got, c.want)
		}
	}
	if got := browserPlan("I'll open the **docs**. They have the install steps. Then I will read them carefully.\n"); got != "I'll open the docs. They have the install steps." {
		t.Errorf("plan %q", got)
	}
	if browserPlan(reviewMarker) != "" || browserPlan("  ") != "" {
		t.Error("nothing to say is not a plan")
	}
	if got := browserPlan(strings.Repeat("word ", 80)); len([]rune(got)) > 202 {
		t.Errorf("plan too long: %d", len(got))
	}
}

func TestBrowserLaunchArgs(t *testing.T) {
	l := Launch{Mcp: []McpServer{{Name: browserServer, Command: `C:\node.exe`, Args: []string{`C:\gate.cjs`, "--key", "k"}}}}
	args := (&claude{}).ChatArgs(l, "ask")
	var cfg, allow string
	for i, a := range args {
		if a == "--mcp-config" {
			cfg = args[i+1]
		}
		if strings.HasPrefix(a, "--allowedTools=") {
			allow = a
		}
	}
	var parsed struct {
		McpServers map[string]struct {
			Type    string
			Command string
			Args    []string
		}
	}
	if err := json.Unmarshal([]byte(cfg), &parsed); err != nil {
		t.Fatalf("claude mcp config %q: %v", cfg, err)
	}
	srv := parsed.McpServers[browserServer]
	if srv.Type != "stdio" || srv.Command != `C:\node.exe` || strings.Join(srv.Args, " ") != `C:\gate.cjs --key k` || allow != "--allowedTools=mcp__ait-browser" {
		t.Errorf("claude: %+v %q", srv, allow)
	}
	cx := strings.Join((&codex{}).ChatArgs(l, "ask"), "\x00")
	for _, want := range []string{"-c\x00mcp_servers.ait-browser.command=\"C:\\\\node.exe\"", "-c\x00mcp_servers.ait-browser.args=[\"C:\\\\gate.cjs\",\"--key\",\"k\"]"} {
		if !strings.Contains(cx, want) {
			t.Errorf("codex args lack %q in %q", want, cx)
		}
	}
	if strings.Contains(strings.Join((&claude{}).ChatArgs(Launch{}, "ask"), " "), "mcp-config") {
		t.Error("no browser, no mcp config")
	}
	if mcpCategory(browserServer, "", nil) != "Websites" {
		t.Error("the browser is listed under Websites")
	}
}

// The pane shows the steps; the gate asks about sites and waits for the
// user's answer on a card in the chat.
func TestBrowserStepsAndSiteQuestions(t *testing.T) {
	root := t.TempDir()
	store, _ := newStoreAt(root, t.TempDir())
	app := NewApp(store)
	var amu sync.Mutex
	var asks []Ev
	app.emit = func(event string, data ...any) {
		if event == "chat:ev" {
			for _, e := range data[1].([]Ev) {
				if e["k"] == "ask" {
					amu.Lock()
					asks = append(asks, e)
					amu.Unlock()
				}
			}
		}
	}
	asked := func() int { amu.Lock(); defer amu.Unlock(); return len(asks) }
	tab := &Tab{id: 7, agent: registry["claude"], profile: "claude"}
	tab.adopted.Store(true)
	tab.working.Store(true)
	app.tabs[7] = tab
	app.browser.logs = map[string]*browserLog{"k1": {key: "k1", tab: tab, ai: "Claude", allowed: map[string]bool{}}}
	app.browserStep(&Tab{}, "ignored", browserPrefix+"browser_navigate", nil) // no key: nothing happens
	tab.browserKey.Store("k1")
	app.browserStep(tab, "I'll open the docs.", browserPrefix+"browser_navigate", map[string]any{"url": "https://example.com"})
	app.browserFailed(tab, "### Error\nnavigation failed\nmore")
	base, err := app.browserBase()
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	var steps []string
	st := app.BrowserState(7)
	for _, s := range st["steps"].([]BrowserStep) {
		steps = append(steps, s.Kind+": "+s.Text)
	}
	if strings.Join(steps, "|") != "plan: I'll open the docs.|do: Open example.com|error: Failed: navigation failed" || st["ai"] != "Claude" {
		t.Fatalf("steps %v", steps)
	}
	if st["open"] != false {
		t.Errorf("no window yet: %v", st["open"])
	}
	if app.BrowserState(99) != nil {
		t.Error("an unknown tab has no browser")
	}

	ask := func(host, decision string) string {
		out := make(chan string, 1)
		before := asked()
		go func() { out <- get("/ask?key=k1&host=" + host) }()
		deadline := time.Now().Add(3 * time.Second)
		for asked() == before {
			select {
			case r := <-out:
				return r // answered without asking
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("no question")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if asked() != before+1 {
			t.Fatalf("one card expected, got %d", asked()-before)
		}
		amu.Lock()
		e := asks[len(asks)-1]
		amu.Unlock()
		if e["tool"] != "WebFetch" || e["desc"] != "open "+strings.TrimPrefix(strings.ToLower(host), "www.") || e["always"] != true {
			t.Fatalf("card %v", e)
		}
		if err := app.ChatAnswer(7, e["req"].(string), decision); err != nil {
			t.Fatal(err)
		}
		return <-out
	}
	if r := ask("www.Example.com", "allow"); r != "allow" {
		t.Fatalf("allow: %s", r)
	}
	if r := ask("example.com", "deny"); r != "allow" { // allowed for this chat's browser: not asked again
		t.Fatalf("allowed before: %s", r)
	}
	if r := ask("example.org", "deny"); r != "deny" {
		t.Fatalf("deny: %s", r)
	}
	if r := ask("example.net", "always"); r != "allow" {
		t.Fatalf("always: %s", r)
	}
	if sites := store.Config().BrowserSites; len(sites) != 1 || sites[0] != "example.net" {
		t.Fatalf("always-allowed sites %v", sites)
	}
	// Always allowed: asked in no other chat either.
	app.browser.mu.Lock()
	app.browser.logs["k2"] = &browserLog{tab: tab, allowed: map[string]bool{}}
	app.browser.mu.Unlock()
	if r := get("/ask?key=k2&host=example.net"); r != "allow" {
		t.Fatalf("always allowed: %s", r)
	}
	refused := app.BrowserState(7)["steps"].([]BrowserStep)
	if last := refused[len(refused)-1]; last.Kind != "error" || last.Text != "Not allowed to open example.org" {
		t.Errorf("the refusal shows in the browser window's steps: %+v", last)
	}
	if err := app.BrowserForget("example.net"); err != nil || len(store.Config().BrowserSites) != 0 {
		t.Fatalf("forget: %v %v", err, store.Config().BrowserSites)
	}
	if err := app.ChatAnswer(7, askPrefix+"99", "allow"); err == nil {
		t.Error("an old question cannot be answered")
	}
}

// A chat that uses its browser shows the plan and the step in the pane,
// and a failed call; other tools do not.
func TestBrowserStepsFromTheChat(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644)
	b, _ := json.Marshal(Config{Accounts: []Account{{ID: "main", Label: "Claude 1"}}, StartingDir: cwd})
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	t.Setenv("AIT_FAKE_CLI", "1")
	s, _ := newStoreAt(root, home)
	registry["claude"] = fakeChat{registry["claude"].(*claude)}
	a := NewApp(s)
	done := make(chan bool, 4)
	a.emit = func(event string, data ...any) {
		if event == "chat:ev" {
			for _, e := range data[1].([]Ev) {
				if e["k"] == "done" {
					done <- true
				}
			}
		}
	}
	if _, err := a.Open(OpenRequest{ID: 1, Profile: "claude", Account: "main", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	defer func() { a.Close(1); a.finalTabsSave() }()
	tab := a.tab(1)
	a.browser.mu.Lock()
	a.browser.logs = map[string]*browserLog{"k": {tab: tab, allowed: map[string]bool{}}}
	a.browser.mu.Unlock()
	tab.browserKey.Store("k")
	time.Sleep(500 * time.Millisecond)
	a.ChatSend(1, "browse", nil)
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("the turn never ended")
	}
	a.browser.mu.Lock()
	defer a.browser.mu.Unlock()
	var got []string
	for _, st := range a.browser.logs["k"].steps {
		got = append(got, st.Kind+":"+st.Text)
	}
	want := "plan:I'll open the docs. They have the install steps.|do:Open example.com/docs|error:Failed: navigation failed"
	if strings.Join(got, "|") != want {
		t.Fatalf("steps %q, want %q", strings.Join(got, "|"), want)
	}
}
