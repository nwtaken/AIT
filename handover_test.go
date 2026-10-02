package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
// review instruction only appears once two different AIs are connected.
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
	if _, text := store.RulesFor(); !strings.Contains(text, reviewMarker) {
		t.Fatal("review rule missing with two AIs")
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
