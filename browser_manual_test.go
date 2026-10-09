//go:build manual

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real Claude (or ChatGPT) with the real browser: the AI opens a page, the
// user is asked first, the pane log has the step, and the answer comes
// back. Opens a Chrome window for a moment. Run with:
//
//	go test -tags manual -run TestRealBrowser -v
func TestRealBrowserClaude(t *testing.T) {
	realBrowser(t, "claude", Account{ID: "claude-main", Label: "Claude", Provider: "claude"})
}
func TestRealBrowserChatGPT(t *testing.T) {
	realBrowser(t, "codex", Account{ID: "codex-main", Label: "ChatGPT", Provider: "codex"})
}

func TestRealBrowserHelpClaude(t *testing.T) {
	realBrowser(t, "claude", Account{ID: "claude-main", Label: "Claude", Provider: "claude"}, true)
}

// With permissions off the AI browses without any question about the site.
func TestRealBrowserFreeChatGPT(t *testing.T) {
	browserTestFree = true
	defer func() { browserTestFree = false }()
	realBrowser(t, "codex", Account{ID: "codex-main", Label: "ChatGPT", Provider: "codex"})
}

var browserTestFree bool

func realBrowser(t *testing.T, profile string, acct Account, help ...bool) {
	forgetTestChats(t)
	home, _ := os.UserHomeDir()
	root := t.TempDir()
	// The real, installed Playwright server, copied so the test needs no network.
	appData, _ := os.UserConfigDir()
	src := filepath.Join(appData, "AIT", "browser")
	if !fileExists(filepath.Join(src, "node_modules", "@playwright", "mcp", "cli.js")) {
		t.Skip("the browser is not installed (start AIT once)")
	}
	if out, err := exec.Command("robocopy", src, filepath.Join(root, "browser"), "/E", "/NFL", "/NDL", "/NJH", "/NJS", "/XD", "profile", "output", "tmp").CombinedOutput(); err != nil && !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("copy: %v %s", err, out)
	}
	store, err := newStoreAt(root, home)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Config()
	if browserTestFree {
		c.Permissions = "never"
	}
	c.Args = map[string][]string{"codex": {"-m", "gpt-5.6-luna"}, "claude": {"--model", "haiku"}}
	c.StartingDir = t.TempDir()
	c.MemoryDir = t.TempDir()
	store.saveConfig(c)
	if !store.browserReady() {
		t.Skip("no Node.js or Chrome/Edge")
	}
	a := NewApp(store)
	evs := make(chan Ev, 4096)
	a.emit = func(name string, d ...any) {
		if name == "chat:ev" {
			for _, e := range d[1].([]Ev) {
				evs <- e
			}
		}
	}
	if _, err := a.Open(OpenRequest{ID: 1, Profile: profile, Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	defer a.Close(1)
	prompt := "Use your ait-browser tools to open https://example.com and tell me the page's title, in one short sentence. Do nothing else."
	if len(help) > 0 {
		prompt = "Use your ait-browser tools to open https://example.com. Then use browser_ask_user to ask me to click the page's only link and press Done. After I have, tell me in one short sentence what the page's title was."
	}
	a.ChatSend(1, prompt, nil)
	helped := false
	var text strings.Builder
	asked := false
	deadline := time.After(240 * time.Second)
	for done := false; !done; {
		select {
		case e := <-evs:
			if e["k"] != "delta" {
				t.Logf("event %v", e)
			}
			switch e["k"] {
			case "ask":
				if req, _ := e["req"].(string); strings.HasPrefix(req, askPrefix) {
					if e["tool"] == "BrowserHelp" {
						helped = true
						t.Logf("the AI asks the user: %v", e["desc"])
						if err := a.ChatAnswer(1, req, "allow"); err != nil {
							t.Fatal(err)
						}
						continue
					}
					if browserTestFree {
						t.Fatalf("a site was asked about with permissions off: %v", e)
					}
					if e["desc"] != "open example.com" {
						t.Fatalf("question %v", e)
					}
					asked = true
					if err := a.ChatAnswer(1, req, "allow"); err != nil {
						t.Fatal(err)
					}
				} else { // anything else the AI asks (a tool permission): this test does not need it
					a.ChatAnswer(1, e["req"].(string), "allow")
				}
			case "delta":
				text.WriteString(e["text"].(string))
			case "done":
				done = true
			case "exit":
				t.Fatalf("the AI exited: %v", e)
			}
		case <-deadline:
			t.Fatalf("no answer; text so far %q", text.String())
		}
	}
	key, _ := a.tab(1).browserKey.Load().(string)
	l := a.browserLogFor(key)
	if l == nil {
		t.Fatal("the chat had no browser")
	}
	a.browser.mu.Lock()
	var steps []string
	for _, s := range l.steps {
		steps = append(steps, s.Kind+": "+s.Text)
	}
	a.browser.mu.Unlock()
	t.Logf("asked=%v\nsteps:\n  %s\nanswer: %s", asked, strings.Join(steps, "\n  "), strings.TrimSpace(text.String()))
	if !asked && !browserTestFree {
		t.Error("the user was never asked about example.com")
	}
	if len(help) > 0 && !helped {
		t.Error("the AI never asked the user for help")
	}
	if !strings.Contains(strings.Join(steps, "\n"), "do: Open example.com") {
		t.Error("the open step is not in the pane log")
	}
	if !strings.Contains(strings.ToLower(text.String()), "example domain") {
		t.Errorf("the AI did not report the title: %q", text.String())
	}
}
