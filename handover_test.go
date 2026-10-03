package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"unsafe"
)

type chatSink struct {
	bytes.Buffer
	fail bool
}

func (s *chatSink) Close() error { return nil }
func (s *chatSink) Write(b []byte) (int, error) {
	if s.fail {
		return 0, errors.New("closed pipe")
	}
	return s.Buffer.Write(b)
}

func TestHandoverInterruptAndCompletion(t *testing.T) {
	for _, state := range []string{"complete", "failed", "send-failed"} {
		t.Run(state, func(t *testing.T) {
			p := &claude{}
			sink := &chatSink{}
			proc := &chatProc{stdin: sink}
			st := &ChatState{Asks: map[string]json.RawMessage{}, Data: map[string]any{}}
			tab := &Tab{id: 1, agent: p, chat: proc, chatState: st, reading: true, handover: "history"}
			a := &App{tabs: map[int]*Tab{1: tab}}
			if err := a.ChatControl(1, "interrupt"); err != nil || sink.Len() != 0 {
				t.Fatalf("reading was interrupted: %v", err)
			}
			if err := a.ChatSend(1, "new work", nil); err == nil {
				t.Fatal("accepted work during reading")
			}
			if out := a.handoverEvents(tab, p, proc, st, 0, []Ev{{"k": "delta", "text": "READY"}}); len(out) != 0 {
				t.Fatal("reading output leaked")
			}
			done := Ev{"k": "done"}
			if state == "failed" {
				done["error"] = "usage limit"
			}
			if state == "send-failed" {
				sink.fail = true
			}
			out := a.handoverEvents(tab, p, proc, st, 0, []Ev{done})
			want := "complete"
			if state != "complete" {
				want = "failed"
			}
			if tab.reading || tab.handover != "" || len(out) == 0 || out[0]["state"] != want {
				t.Fatalf("handover stuck or wrong result: %v", out)
			}
			if (state == "complete") != strings.Contains(sink.String(), handoverContinue) {
				t.Fatalf("unexpected continuation: %s", sink.String())
			}
			sink.fail = false
			sink.Reset()
			if err := a.ChatControl(1, "interrupt"); err != nil || !strings.Contains(sink.String(), "interrupt") {
				t.Fatalf("normal interruption was not restored: %v", err)
			}
		})
	}
}

func TestAccountModelsSurviveRestart(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	s, err := newStoreAt(root, home)
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.Config()
	cfg.Accounts = []Account{{ID: "a", Provider: "claude"}, {ID: "b", Provider: "claude"}, {ID: "c", Provider: "codex"}}
	if err := s.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	choices := map[string]string{"a": "claude-opus-5-5", "b": "", "c": "gpt-6-sol"}
	for id, model := range choices {
		if err := s.SetAccountModel(id, model); err != nil {
			t.Fatal(err)
		}
		s.MarkSignedOut(id)
		s.ClearSignedOut(id)
	}
	reopened, err := newStoreAt(root, home)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range choices {
		acct, ok := reopened.Account(id)
		if !ok || acct.Model == nil || *acct.Model != want {
			t.Fatalf("account %s lost model %q", id, want)
		}
	}
}

func TestAccountModelRestoredOnSwitch(t *testing.T) {
	t.Setenv("AIT_FAKE_CLI", "1")
	s, err := newStoreAt(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.Config()
	m1, m2 := "model-one", "model-two"
	cfg.Accounts = []Account{{ID: "a", Provider: "claude", Model: &m1}, {ID: "b", Provider: "claude", Model: &m2}}
	if err := s.saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	registry["claude"] = fakeChat{registry["claude"].(*claude)}
	a := NewApp(s)
	a.emit = func(string, ...any) {}
	info, err := a.Open(OpenRequest{ID: 1, Profile: "claude", Account: "a"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(1)
	if info.Model != m1 {
		t.Fatalf("opened on %q", info.Model)
	}
	if err := a.ChatControl(1, "model:model-updated"); err != nil {
		t.Fatal(err)
	}
	for _, choice := range []struct{ id, model string }{{"b", m2}, {"a", "model-updated"}} {
		if err := a.Switch(1, choice.id); err != nil {
			t.Fatal(err)
		}
		tab := a.tab(1)
		tab.mu.Lock()
		model := tab.model
		args := strings.Join(tab.chat.cmd.Args, " ")
		tab.mu.Unlock()
		if model != choice.model || !strings.Contains(args, "--model "+choice.model) {
			t.Fatalf("account %s started on %q, args %s", choice.id, model, args)
		}
	}
}

func TestSuperReviewUsesDifferentAccountAndReturnsFeedback(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("AIT_FAKE_CLI", "1")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644)
	cfg := Config{Accounts: []Account{{ID: "main", Label: "Claude 1"}, {ID: "other", Label: "ChatGPT 1", Provider: "codex"}},
		StartingDir: cwd, AIOrder: []string{"claude", "codex"}}
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	s, err := newStoreAt(root, home)
	if err != nil {
		t.Fatal(err)
	}
	cl := registry["claude"].(*claude)
	registry["claude"] = fakeChat{cl}
	registry["codex"] = fakeOther{fakeChat{cl}}
	a := NewApp(s)
	done := make(chan string, 1)
	a.emit = func(event string, data ...any) {
		if event == "review:done" {
			done <- data[1].(string)
		}
		if event == "review:error" {
			done <- "ERROR: " + data[1].(string)
		}
	}
	if _, err := a.Open(OpenRequest{ID: 1, Profile: "claude", Account: "main", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	defer a.Close(1)
	if !a.CanReview(1) {
		t.Fatal("second connected AI was unavailable")
	}
	if err := a.Review(1); err != nil {
		t.Fatal(err)
	}
	if err := a.Review(1); err == nil {
		t.Fatal("accepted duplicate review")
	}
	select {
	case name := <-done:
		if name != "ChatGPT" {
			t.Fatalf("review result: %s", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("review never returned")
	}
	if a.tab(1).profile != "claude" {
		t.Fatal("review changed the working AI")
	}
	if files, _ := filepath.Glob(filepath.Join(root, "handovers", "*.md")); len(files) != 1 {
		t.Fatalf("review did not capture the conversation: %v", files)
	}
}

// A model picked on an AI with no saved account must not fail, and the
// review instruction only appears with /supereview on and two different AIs.
func TestModelWithoutAccountAndReviewRule(t *testing.T) {
	t.Setenv("AIT_FAKE_CLI", "1")
	store, err := newStoreAt(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAccountModel("codex-main", "gpt-x"); err != nil {
		t.Fatalf("model on unsaved account: %v", err)
	}
	if _, text := store.RulesFor(); strings.Contains(text, reviewMarker) {
		t.Fatal("review rule offered with one AI")
	}
	store.saveConfig(Config{Accounts: []Account{{ID: "a", Provider: "claude"}, {ID: "b", Provider: "codex"}}})
	if _, text := store.RulesFor(); strings.Contains(text, reviewMarker) {
		t.Fatal("review rule offered before /supereview is switched on")
	}
	app := NewApp(store)
	if on, err := app.ToggleReview(0); !on || err != nil {
		t.Fatalf("toggle on: %v %v", on, err)
	}
	if _, text := store.RulesFor(); !strings.Contains(text, reviewMarker) {
		t.Fatal("review rule missing with /supereview on and two AIs")
	}
	if on, _ := app.ToggleReview(0); on || app.ReviewOn() {
		t.Fatal("toggle off did not switch it off")
	}
}

// Another program's transcript in the same folder (a /supereview reviewer,
// Claude Code run outside AIT) must not replace the tab's own conversation.
func TestNativeChatKeepsItsOwnTranscript(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644)
	b, _ := json.Marshal(Config{Accounts: []Account{{ID: "main", Label: "Main"}}, StartingDir: cwd})
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	t.Setenv("AIT_FAKE_CLI", "1")
	store, _ := newStoreAt(root, home)
	cl := registry["claude"].(*claude)
	registry["claude"] = fakeChat{cl}
	defer func() { registry["claude"] = cl }()
	app := NewApp(store)
	inited := make(chan bool, 8)
	app.emit = func(name string, d ...any) {
		if name == "chat:ev" {
			for _, e := range d[1].([]Ev) {
				if e["k"] == "init" {
					inited <- true
				}
			}
		}
	}
	if _, err := app.Open(OpenRequest{ID: 1, Profile: "claude", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	defer app.Close(1)
	select {
	case <-inited:
	case <-time.After(5 * time.Second):
		t.Fatal("no init")
	}
	tab := app.tab(1)
	tab.mu.Lock()
	own := tab.session
	tab.mu.Unlock()
	if own == "" {
		t.Fatal("tab has no transcript")
	}
	acct, _ := store.Account("main")
	foreign := filepath.Join(cl.projectDir(store.Home(acct), cwd), "someone-else.jsonl")
	os.MkdirAll(filepath.Dir(foreign), 0o755)
	os.WriteFile(foreign, []byte("{}\n"), 0o644)
	time.Sleep(2500 * time.Millisecond) // the watcher ticks every second
	tab.mu.Lock()
	got := tab.session
	tab.mu.Unlock()
	if got != own {
		t.Fatalf("tab moved to %s, want %s", got, own)
	}
}

func TestTrimHandoverKeepsWholeCharacters(t *testing.T) {
	line := strings.Repeat("ş—…🙂", 37) + "\n"
	text := "# Conversation handed over\n" + strings.Repeat(line, 3000)
	out := trimHandover(text)
	if !utf8.ValidString(out) {
		t.Fatal("trimmed handover is not valid UTF-8")
	}
	if len(out) > handoverLimit+100 || !strings.HasPrefix(out, "# Conversation handed over\n") || !strings.HasSuffix(out, line) {
		t.Fatalf("trim lost the start or the end: %d bytes", len(out))
	}
}

func TestCutTextKeepsWholeCharacters(t *testing.T) {
	s := strings.Repeat("a", 139) + "ş" + strings.Repeat("—", 100)
	for _, n := range []int{139, 140, 141, 142, 143} {
		if got := cutText(s, n); !utf8.ValidString(got) || len(got) > n {
			t.Fatalf("cutText(%d) = %d bytes, valid %v", n, len(got), utf8.ValidString(got))
		}
	}
}

// Switching accounts mid-turn tells the next account to carry on; after a
// finished turn the switch just resumes and waits.
func TestSwitchMidTurnContinues(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	mainDir := filepath.Join(home, ".claude")
	second := filepath.Join(root, "profiles", "acct-02")
	for _, d := range []string{mainDir, second} {
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, ".credentials.json"), []byte("{}"), 0o644)
	}
	b, _ := json.Marshal(Config{Accounts: []Account{{ID: "main", Label: "Main"}, {ID: "acct-02", Label: "Account 2", Provider: "claude", Dir: second}}, StartingDir: cwd})
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	t.Setenv("AIT_FAKE_CLI", "1")
	store, _ := newStoreAt(root, home)
	registry["claude"] = fakeChat{registry["claude"].(*claude)}
	app := NewApp(store)
	evs := make(chan Ev, 256)
	app.emit = func(name string, d ...any) {
		if name == "chat:ev" {
			for _, e := range d[1].([]Ev) {
				evs <- e
			}
		}
	}
	if _, err := app.Open(OpenRequest{ID: 1, Profile: "claude", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	defer app.Close(1)
	wait := func(within time.Duration, pred func(Ev) bool) bool {
		deadline := time.After(within)
		for {
			select {
			case e := <-evs:
				if pred(e) {
					return true
				}
			case <-deadline:
				return false
			}
		}
	}
	isInit := func(e Ev) bool { return e["k"] == "init" }
	echoed := func(s string) func(Ev) bool {
		return func(e Ev) bool { return e["k"] == "delta" && e["text"] == "echo: "+s }
	}
	// A transcript to resume, at the path the tab claimed for the session.
	writeTranscript := func() {
		tab := app.tab(1)
		tab.mu.Lock()
		p := tab.session
		tab.mu.Unlock()
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(`{"type":"user","message":{"role":"user","content":"hang"}}`+"\n"), 0o644)
	}
	if !wait(5*time.Second, isInit) {
		t.Fatal("no init")
	}
	writeTranscript()
	if !wait(3*time.Second, func(e Ev) bool { b, _ := e["bytes"].(int64); return e["k"] == "size" && b > 0 }) {
		t.Fatal("the conversation size was not reported")
	}
	app.ChatSend(1, "hang", nil)
	if !wait(5*time.Second, func(e Ev) bool { return e["k"] == "msg" }) {
		t.Fatal("turn did not start")
	}
	if err := app.Switch(1, "acct-02"); err != nil {
		t.Fatal(err)
	}
	if !wait(5*time.Second, echoed("continue")) {
		t.Fatal("switching mid-turn did not continue the work")
	}
	if !wait(5*time.Second, func(e Ev) bool { return e["k"] == "done" }) {
		t.Fatal("continued turn did not finish")
	}
	writeTranscript()
	if err := app.Switch(1, "main"); err != nil {
		t.Fatal(err)
	}
	if wait(2*time.Second, echoed("continue")) {
		t.Fatal("switching after a finished turn sent continue")
	}
}

// Saved rules reach agents under the top-priority header; empty rules are off.
func TestSavedRulesArePriority(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	if err := app.SaveRules("  - Always answer in French.  "); err != nil {
		t.Fatal(err)
	}
	if _, text := store.RulesFor(); !strings.HasPrefix(text, rulesPriority+"- Always answer in French.") {
		t.Fatalf("rules not first with priority header:\n%.300s", text)
	}
	app.SaveRules("")
	if _, text := store.RulesFor(); strings.Contains(text, "top priority") || app.GetRules().Text != "" {
		t.Fatalf("empty rules still applied:\n%.300s", text)
	}
	if len(app.GetRules().Presets) != 10 {
		t.Fatalf("want 10 presets, got %d", len(app.GetRules().Presets))
	}
}

// Gemini is found at the npm package's current script, and an extra
// account's sign-in is read from the .gemini folder inside it.
func TestGeminiLookupAndAccountFolder(t *testing.T) {
	if lookBinary("node") == "" {
		t.Skip("node not installed")
	}
	appdata := t.TempDir()
	t.Setenv("APPDATA", appdata)
	t.Setenv("PATH", filepath.Dir(lookBinary("node")))
	g := &gemini{userHome: t.TempDir()}
	if g.Command() != nil {
		t.Fatal("found Gemini where none is installed")
	}
	js := filepath.Join(appdata, "npm", "node_modules", "@google", "gemini-cli", "bundle", "gemini.js")
	os.MkdirAll(filepath.Dir(js), 0o755)
	os.WriteFile(js, nil, 0o644)
	if cmd := g.Command(); len(cmd) != 2 || cmd[1] != js {
		t.Fatalf("Gemini not found at bundle/gemini.js: %v", cmd)
	}
	acct := t.TempDir()
	os.MkdirAll(filepath.Join(acct, ".gemini"), 0o755)
	os.WriteFile(filepath.Join(acct, ".gemini", "oauth_creds.json"), []byte("{}"), 0o644)
	t.Setenv("GEMINI_API_KEY", "")
	if !g.SignedIn(acct) || g.SignedIn(t.TempDir()) {
		t.Fatal("account sign-in not read from its .gemini folder")
	}
}

// MCP switches are kept per AI; Codex only gets switches for servers its
// config has, and its status list hides its own connector bridge.
func TestMcpSwitches(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	store.setMcpOff("claude", "elevenlabs", true)
	store.setMcpOff("claude", "gmail", true)
	store.setMcpOff("claude", "gmail", false)
	store.setMcpOff("codex", "robloxstudio", true)
	if got := app.McpOff("claude"); len(got) != 1 || got[0] != "elevenlabs" {
		t.Fatalf("claude off list: %v", got)
	}
	if got := app.McpOff("codex"); len(got) != 1 || got[0] != "robloxstudio" {
		t.Fatalf("codex off list: %v", got)
	}

	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.robloxstudio]\ncommand = \"cmd\"\n[mcp_servers.robloxstudio.env]\nA = \"1\"\n"), 0o644)
	if got := (&codex{}).McpKnown(home, []string{"robloxstudio", "gone"}); len(got) != 1 || got[0] != "robloxstudio" {
		t.Fatalf("known: %v", got)
	}
	args := (&codex{}).ChatArgs(Launch{McpOff: []string{"robloxstudio"}}, "ask")
	if !strings.Contains(strings.Join(args, " "), "-c mcp_servers.robloxstudio.enabled=false") {
		t.Fatalf("args: %v", args)
	}

	evs := codexMcp([]byte(`{"data":[{"name":"codex_apps","tools":{"a":{}}},{"name":"one","tools":{"x":{},"y":{}}},{"name":"two","tools":{},"toolsError":"boom"}]}`))
	servers := evs[0]["servers"].([]map[string]any)
	if len(servers) != 2 || servers[0]["status"] != "connected" || servers[0]["tools"] != 2 || servers[1]["status"] != "failed" {
		t.Fatalf("codex status: %v", servers)
	}
}

// The notification struct must match NOTIFYICONDATAW exactly, or Windows
// reads the title and text from the wrong place.
func TestNotifyIconDataSize(t *testing.T) {
	if n := unsafe.Sizeof(notifyIconData{}); n != 976 {
		t.Fatalf("NOTIFYICONDATAW is %d bytes, want 976", n)
	}
}

// A long conversation is trimmed by the handover writer itself: the first
// request and the latest work survive, and no character is cut in half.
func TestWriteHandoverTrimsWholeCharacters(t *testing.T) {
	root := t.TempDir()
	store, _ := newStoreAt(root, t.TempDir())
	app := NewApp(store)
	var b strings.Builder
	for i := 0; i < 1200; i++ {
		msg, _ := json.Marshal(fmt.Sprintf("request %d: %s", i, strings.Repeat("ş—…🙂", 40)))
		fmt.Fprintf(&b, `{"type":"user","message":{"role":"user","content":%s}}`+"\n", msg)
	}
	session := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(session, []byte(b.String()), 0o644)
	tab := &Tab{session: session, cwd: t.TempDir()}
	p, err := app.writeHandover(tab, registry["claude"], "was switched out")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(p)
	if !utf8.Valid(out) || len(out) > handoverLimit+200 {
		t.Fatalf("handover: %d bytes, valid UTF-8 %v", len(out), utf8.Valid(out))
	}
	if !strings.Contains(string(out), "request 0:") || !strings.Contains(string(out), "request 1199:") {
		t.Fatal("handover lost the first request or the latest work")
	}
}

// Claude's get_context_usage answer becomes the context readout.
func TestClaudeContextUsage(t *testing.T) {
	evs := claudeAnswer([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"ait-ctx","response":{"totalTokens":17710,"maxTokens":1000000}}}`))
	if len(evs) != 1 || evs[0]["k"] != "ctx" || evs[0]["ctx"] != 17710 || evs[0]["window"] != 1000000 {
		t.Fatalf("got %v", evs)
	}
}

// A big chat replays from the AI's last compaction summary, found by a quick
// scan (even past lines longer than the read buffer), with only the latest
// events after it.
func TestSummaryReplay(t *testing.T) {
	var b strings.Builder
	line := func(v any) { j, _ := json.Marshal(v); b.Write(j); b.WriteByte('\n') }
	user := func(s string) {
		line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": s}})
	}
	user("old request")
	user(strings.Repeat("x", 3<<20)) // longer than the scanner's read buffer
	line(map[string]any{"type": "user", "isCompactSummary": true, "message": map[string]any{"role": "user", "content": "This session is being continued from a previous conversation.\n\nSummary: built the map"}})
	for i := 0; i < summaryKeep+50; i++ {
		user(fmt.Sprintf("later %d", i))
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(b.String()), 0o644)
	evs := summarize(visibleHistory((&claude{}).ChatHistoryTail(p)))
	if evs[0]["k"] != "summary" || !strings.Contains(evs[0]["text"].(string), "built the map") {
		t.Fatalf("does not start with the summary: %v", evs[0])
	}
	if evs[1]["k"] != "note" || evs[len(evs)-1]["text"] != fmt.Sprintf("later %d", summaryKeep+49) || len(evs) > summaryKeep+2 {
		t.Fatalf("latest events not kept: %d events, second %v", len(evs), evs[1])
	}
	for _, e := range evs {
		if e["text"] == "old request" {
			t.Fatal("events before the summary were replayed")
		}
	}
}

// A chat that began as a handover is titled by the original request.
func TestHandoverTitle(t *testing.T) {
	inner := "You are taking over a conversation from Claude, which was switched out by the user. The conversation so far is in this file: x\n\n<conversation>\n# Conversation handed over from Claude\n\nClaude was switched out. Working folder: C:/x\n\n## User\n\nfix the tray icon\n\n## Claude\n\ndone\n</conversation>"
	outer := "You are taking over a conversation from ChatGPT, which ran out of usage. The conversation so far is in this file: y\n\n<conversation>\n# Conversation handed over from ChatGPT\n\n## User\n\n" + inner + "\n\n## ChatGPT\n\nREADY\n</conversation>"
	for in, want := range map[string]string{
		"plain request": "plain request",
		inner:           "fix the tray icon (continued from Claude)",
		outer:           "fix the tray icon (continued from Claude) (continued from ChatGPT)",
		"You are taking over a conversation from Gemini, which was switched out.": "Continued from Gemini",
	} {
		if got := handoverTitle(in); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// A notification remembers its tab, so clicking it can open that tab.
func TestNotificationRemembersTab(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	app.emit = func(string, ...any) {}
	tab := &Tab{id: 4, agent: registry["claude"]}
	tab.adopted.Store(true)
	app.notifyTab(tab, " finished", " stopped", "done", "")
	if got := app.notifiedTab.Load(); got != 4 {
		t.Fatalf("notified tab %d, want 4", got)
	}
}

// /export saves the whole conversation, compaction summaries included, to
// Downloads, named after the first request.
func TestExportChat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	revealFile = func(string) {}
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	var b strings.Builder
	for _, l := range []string{
		`{"type":"user","message":{"role":"user","content":"Build the tray: panel?"}}`,
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"Summary: built the panel"}}`,
		`{"type":"user","message":{"role":"user","content":"now notifications"}}`,
	} {
		b.WriteString(l + "\n")
	}
	session := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(session, []byte(b.String()), 0o644)
	app.tabs[1] = &Tab{id: 1, agent: registry["claude"], session: session, cwd: home}
	p, err := app.ExportChat(1)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(p)
	if filepath.Dir(p) != filepath.Join(home, "Downloads") || !strings.Contains(filepath.Base(p), "Build the tray panel") {
		t.Fatalf("file %s", p)
	}
	for _, want := range []string{"# Chat with Claude", "## User\n\nBuild the tray: panel?", "## Summary of the earlier conversation\n\nSummary: built the panel", "## User\n\nnow notifications"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("export lacks %q", want)
		}
	}
}

// A trimmed handover keeps the AI's latest compaction summary even when it
// lies in the part that is left out.
func TestTrimHandoverKeepsLatestSummary(t *testing.T) {
	filler := strings.Repeat("## User\n\nmore work\n\n", 4000)
	text := "# Conversation\n\n" + filler + "## Summary of the earlier conversation\n\nSUMMARY-OLD\n\n" + filler +
		"## Summary of the earlier conversation\n\nSUMMARY-LATEST\n\n" + filler + filler + "## User\n\nthe latest request\n\n"
	out := trimHandover(text)
	if !strings.Contains(out, "SUMMARY-LATEST") || strings.Contains(out, "SUMMARY-OLD") {
		t.Fatal("latest summary not kept (or an older one kept)")
	}
	if !strings.HasSuffix(out, "the latest request\n\n") || len(out) > handoverLimit+1024 || !utf8.ValidString(out) {
		t.Fatalf("tail or size wrong: %d bytes", len(out))
	}
}

// Open AI tabs with a conversation are saved in order with the active one,
// survive the quit (closing tabs afterwards cannot empty the list), and come
// back unless the conversation is gone or reopening is switched off.
func TestReopenTabs(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	dir := t.TempDir()
	file := func(name string, n int) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(strings.Repeat("x", n)), 0o644)
		return p
	}
	mk := func(id int, profile, session string) {
		tab := &Tab{id: id, agent: registry[profile], profile: profile, session: session}
		tab.adopted.Store(true)
		app.tabs[id] = tab
	}
	a, gone := file("a.jsonl", 10), file("gone.jsonl", 5)
	c := file("c.jsonl", 30)
	mk(3, "codex", c)
	mk(1, "claude", a)
	mk(2, "claude", "") // no conversation yet: nothing to reopen
	mk(4, "claude", gone)
	app.SetActiveTab(3)
	app.finalTabsSave()
	app.Close(1) // closing on the way out must not change the saved list
	time.Sleep(700 * time.Millisecond)
	os.Remove(gone)

	got := app.LastTabs()
	want := []SavedTab{{Provider: "claude", Ref: a, Size: 10}, {Provider: "codex", Ref: c, Size: 30, Active: true}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	off := false
	cfg := store.Config()
	cfg.ReopenTabs = &off
	store.saveConfig(cfg)
	if len(app.LastTabs()) != 0 {
		t.Fatal("tabs reopened with reopening switched off")
	}
}

// Reopened messages carry the time they were sent.
func TestHistoryMessageTimes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(p, []byte(`{"type":"user","timestamp":"2026-10-01T18:43:11.123Z","message":{"role":"user","content":"hello"}}`+"\n"), 0o644)
	evs := (&claude{}).ChatHistory(p)
	if len(evs) != 1 || evs[0]["ts"] != int64(1790880191) {
		t.Fatalf("got %v", evs)
	}
	if unixTime("") != 0 {
		t.Fatal("missing time not 0")
	}
}

// A tab's unsent text is saved with it and comes back.
func TestReopenTabsKeepsDraft(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	p := filepath.Join(t.TempDir(), "a.jsonl")
	os.WriteFile(p, []byte("x"), 0o644)
	tab := &Tab{id: 1, agent: registry["claude"], profile: "claude", session: p}
	tab.adopted.Store(true)
	app.tabs[1] = tab
	app.SetDraft(1, "half a prompt")
	app.finalTabsSave()
	if got := app.LastTabs(); len(got) != 1 || got[0].Draft != "half a prompt" {
		t.Fatalf("got %+v", got)
	}
}

// The AI asking for a review ([[AIT_SUPEREVIEW]] as its whole reply) starts
// one when /supereview is on, and the feedback goes back to it.
func TestReviewMarkerStartsReview(t *testing.T) {
	for _, prompt := range []string{"review me", "review quietly"} {
		t.Run(prompt, func(t *testing.T) { reviewMarker1(t, prompt) })
	}
}

func reviewMarker1(t *testing.T, prompt string) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644)
	cfg := Config{Accounts: []Account{{ID: "main", Label: "Claude 1"}, {ID: "other", Label: "ChatGPT 1", Provider: "codex"}},
		StartingDir: cwd, AIOrder: []string{"claude", "codex"}, Review: true}
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	t.Setenv("AIT_FAKE_CLI", "1")
	s, _ := newStoreAt(root, home)
	cl := registry["claude"].(*claude)
	registry["claude"] = fakeChat{cl}
	registry["codex"] = fakeOther{fakeChat{cl}}
	a := NewApp(s)
	got := make(chan string, 16)
	a.emit = func(event string, data ...any) {
		switch event {
		case "review:start", "review:done", "review:error":
			got <- event
		}
	}
	if _, err := a.Open(OpenRequest{ID: 1, Profile: "claude", Account: "main", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	defer a.Close(1)
	time.Sleep(500 * time.Millisecond)
	a.ChatSend(1, prompt, nil)
	for _, want := range []string{"review:start", "review:done"} {
		select {
		case e := <-got:
			if e != want {
				t.Fatalf("got %s, want %s", e, want)
			}
		case <-time.After(8 * time.Second):
			t.Fatalf("no %s: the review request was not picked up", want)
		}
	}
}

// With /supereview off, an AI asking for a review is told to carry on.
func TestReviewMarkerWhenOff(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644)
	b, _ := json.Marshal(Config{Accounts: []Account{{ID: "main", Label: "Claude 1"}}, StartingDir: cwd})
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	t.Setenv("AIT_FAKE_CLI", "1")
	s, _ := newStoreAt(root, home)
	registry["claude"] = fakeChat{registry["claude"].(*claude)}
	a := NewApp(s)
	got := make(chan string, 64)
	a.emit = func(event string, data ...any) {
		if event == "review:error" {
			got <- "error"
		}
		if event == "chat:ev" {
			for _, e := range data[1].([]Ev) {
				if txt, _ := e["text"].(string); e["k"] == "delta" && strings.Contains(txt, "Continue the user's request without it") {
					got <- "told"
				}
			}
		}
	}
	if _, err := a.Open(OpenRequest{ID: 1, Profile: "claude", Account: "main", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	defer a.Close(1)
	time.Sleep(500 * time.Millisecond)
	a.ChatSend(1, "review me", nil)
	for _, want := range []string{"error", "told"} {
		select {
		case e := <-got:
			if e != want {
				t.Fatalf("got %s, want %s", e, want)
			}
		case <-time.After(8 * time.Second):
			t.Fatalf("no %s: the AI was left waiting", want)
		}
	}
}

// A finished Codex message carries its whole text, so a review request or a
// reviewer's feedback is read even when no deltas streamed.
func TestCodexMessageFinalText(t *testing.T) {
	st := &ChatState{Asks: map[string]json.RawMessage{}, Data: map[string]any{}, Send: func([]byte) {}}
	evs := (&codex{}).item(st, codexItem{Type: "agentMessage", ID: "a", Text: "[[AIT_SUPEREVIEW]]"}, true)
	if len(evs) != 2 || evs[1]["k"] != "final" || evs[1]["text"] != "[[AIT_SUPEREVIEW]]" {
		t.Fatalf("got %v", evs)
	}
}

// The reviewer's steps reach the review card, and its feedback is its last
// message (not the narration before it), even when that one did not stream.
func TestReviewerStepsAndUnstreamedFindings(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644)
	cfg := Config{Accounts: []Account{{ID: "main", Label: "Claude 1"}, {ID: "other", Label: "ChatGPT 1", Provider: "codex"}},
		StartingDir: cwd, AIOrder: []string{"claude", "codex"}, Review: true}
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	t.Setenv("AIT_FAKE_CLI", "1")
	s, _ := newStoreAt(root, home)
	cl := registry["claude"].(*claude)
	registry["claude"] = fakeChat{cl}
	registry["codex"] = fakeOther{fakeChat{cl}}
	a := NewApp(s)
	steps, done := make(chan string, 16), make(chan string, 1)
	a.emit = func(event string, data ...any) {
		switch event {
		case "review:step":
			steps <- data[1].(string)
		case "review:done":
			done <- data[2].(string)
		case "review:error":
			done <- "ERROR: " + data[1].(string)
		}
	}
	// Writing the handover reads the tab's transcript; give it the request.
	if _, err := a.Open(OpenRequest{ID: 1, Profile: "claude", Account: "main", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	defer a.Close(1)
	time.Sleep(500 * time.Millisecond)
	tab := a.tab(1)
	tab.mu.Lock()
	tab.session = filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(tab.session, []byte(`{"type":"user","message":{"role":"user","content":"please QUIET-REVIEW this"}}`+"\n"), 0o644)
	tab.mu.Unlock()
	a.ChatSend(1, "review me", nil)
	select {
	case st := <-steps:
		if st != "Read app.go" {
			t.Fatalf("step %q", st)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("no review:step from the reviewer")
	}
	select {
	case fb := <-done:
		if fb != "FINDINGS: fix the edge case" { // the last message only, not the narration before it
			t.Fatalf("feedback %q", fb)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("review never finished")
	}
}
