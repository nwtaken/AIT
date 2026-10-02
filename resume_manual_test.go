//go:build manual

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Real Codex: the tab finds its own transcript from the reported id, and a
// relaunch (account switch / handoff) resumes it with the conversation intact.
func TestRealCodexResumeRemembers(t *testing.T) {
	realResume(t, "codex", Account{ID: "codex-main", Label: "ChatGPT", Provider: "codex"})
}
func TestRealClaudeResumeRemembers(t *testing.T) {
	realResume(t, "claude", Account{ID: "claude-main", Label: "Claude", Provider: "claude"})
}

func realResume(t *testing.T, profile string, acct Account) {
	home, _ := os.UserHomeDir()
	store, err := newStoreAt(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Config()
	c.Args = map[string][]string{"codex": {"-m", "gpt-5.6-luna"}, "claude": {"--model", "haiku"}}
	c.StartingDir = t.TempDir()
	c.MemoryDir = t.TempDir()
	store.saveConfig(c)
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
	turn := func(msg string) string {
		a.ChatSend(1, msg, nil)
		var text strings.Builder
		deadline := time.After(150 * time.Second)
		for {
			select {
			case e := <-evs:
				switch e["k"] {
				case "delta":
					s, _ := e["text"].(string)
					text.WriteString(s)
				case "ask":
					a.ChatAnswer(1, e["req"].(string), "allow")
				case "error":
					t.Fatalf("error: %v", e["text"])
				case "done":
					return text.String()
				}
			case <-deadline:
				t.Fatal("timed out")
			}
		}
	}
	turn("Remember the code word PINEAPPLE-42. Reply with exactly: ok")
	time.Sleep(2500 * time.Millisecond)
	tab := a.tab(1)
	tab.mu.Lock()
	first := tab.session
	tab.mu.Unlock()
	if first == "" || !fileExists(first) {
		t.Fatalf("tab did not find its transcript: %q", first)
	}
	found := false
	for _, e := range chatOf(registry[profile]).ChatHistory(first) {
		if s, _ := e["text"].(string); e["k"] == "user" && strings.Contains(s, "PINEAPPLE-42") {
			found = true
		}
	}
	if !found {
		t.Fatal("transcript has no record of the first message")
	}

	tab.mu.Lock()
	err = a.relaunch(tab, acct, "")
	tab.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got := turn("What was the code word? Reply with only the code word.")
	t.Logf("after resume: %q", got)
	if !strings.Contains(got, "PINEAPPLE-42") {
		t.Fatalf("resumed chat forgot the conversation: %q", got)
	}
}

// Real cross-AI switches: the next AI still knows the conversation.
func TestRealCodexToClaudeHandover(t *testing.T) { realHandover(t, "codex", "cx", "claude") }
func TestRealClaudeToCodexHandover(t *testing.T) { realHandover(t, "claude", "cl", "codex") }

func realHandover(t *testing.T, from, fromAcct, to string) {
	home, _ := os.UserHomeDir()
	store, err := newStoreAt(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Config()
	c.Args = map[string][]string{"codex": {"-m", "gpt-5.6-luna"}, "claude": {"--model", "haiku"}}
	c.Accounts = []Account{{ID: "cl", Label: "Claude", Provider: "claude"}, {ID: "cx", Label: "ChatGPT", Provider: "codex"}}
	c.StartingDir = t.TempDir()
	c.MemoryDir = t.TempDir()
	store.saveConfig(c)
	a := NewApp(store)
	evs := make(chan Ev, 4096)
	a.emit = func(name string, d ...any) {
		if name == "chat:ev" {
			for _, e := range d[1].([]Ev) {
				evs <- e
			}
		}
	}
	if _, err := a.Open(OpenRequest{ID: 1, Profile: from, Account: fromAcct, Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	defer a.Close(1)
	wait := func(stop func(Ev) bool) string {
		var text strings.Builder
		deadline := time.After(200 * time.Second)
		for {
			select {
			case e := <-evs:
				if e["k"] == "delta" {
					s, _ := e["text"].(string)
					text.WriteString(s)
				} else if e["k"] != "start" && e["k"] != "stop" {
					t.Logf("ev %v", e)
				}
				if e["k"] == "ask" {
					a.ChatAnswer(1, e["req"].(string), "allow")
				}
				if e["k"] == "error" || (e["k"] == "handover" && e["state"] == "failed") {
					t.Fatalf("failed: %v", e)
				}
				if stop(e) {
					return text.String()
				}
			case <-deadline:
				t.Fatal("timed out")
			}
		}
	}
	done := func(e Ev) bool { return e["k"] == "done" }
	a.ChatSend(1, "Remember the code word MANGO-77. Reply with exactly: ok", nil)
	wait(done)
	time.Sleep(2500 * time.Millisecond)
	if err := a.ContinueOn(1, to); err != nil {
		t.Fatal(err)
	}
	wait(func(e Ev) bool { return e["k"] == "handover" && e["state"] == "complete" })
	t.Logf("continue turn: %q", wait(done))
	a.ChatSend(1, "What was the code word? Reply with only the code word.", nil)
	got := wait(done)
	t.Logf("%s says: %q", to, got)
	if !strings.Contains(got, "MANGO-77") {
		t.Fatalf("%s forgot the conversation: %q", to, got)
	}
}
