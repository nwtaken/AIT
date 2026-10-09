//go:build manual

package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Real AIs asking the user a question: the question card arrives with its
// options, and the answer reaches the AI. Run with:
//
//	go test -tags manual -run TestRealQuestion -v
func TestRealQuestionChatGPT(t *testing.T) { realQuestion(t, "codex") }
func TestRealQuestionClaude(t *testing.T)  { realQuestion(t, "claude") }

func realQuestion(t *testing.T, profile string) {
	forgetTestChats(t)
	home, _ := os.UserHomeDir()
	store, err := newStoreAt(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Config()
	c.Args = map[string][]string{"codex": {"-m", "gpt-5.6-luna"}, "claude": {"--model", "haiku"}}
	c.StartingDir = t.TempDir()
	c.MemoryDir = t.TempDir()
	acct := ""
	if dir := os.Getenv("AIT_TEST_CLAUDE_DIR"); dir != "" && profile == "claude" { // another Claude account, when the main one is out of usage
		c.Accounts = []Account{{ID: "alt", Label: "alt", Dir: dir}}
		acct = "alt"
	}
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
	if _, err := a.Open(OpenRequest{ID: 1, Profile: profile, Account: acct, Cols: 100, Rows: 30}); err != nil {
		t.Fatal(err)
	}
	defer a.Close(1)
	tool := "AskUserQuestion"
	if profile == "codex" {
		tool = "request_user_input"
	}
	a.ChatSend(1, "Use your "+tool+" tool to ask me exactly one question, \"Pick a colour\", with the options Red and Blue. After I answer, reply with only the colour I chose and nothing else.", nil)
	var text strings.Builder
	asked := false
	deadline := time.After(180 * time.Second)
	for done := false; !done; {
		select {
		case e := <-evs:
			if k := e["k"]; k == "done" || k == "cmdout" || k == "exit" || k == "error" {
				t.Logf("event %v", e)
			}
			switch e["k"] {
			case "question":
				qs := e["questions"].([]map[string]any)
				t.Logf("question: %v", qs)
				if len(qs) < 1 {
					t.Fatal("no questions")
				}
				asked = true
				opts := qs[0]["options"].([]map[string]any)
				var blue string
				for _, o := range opts {
					if strings.EqualFold(strings.TrimSpace(o["label"].(string)), "blue") {
						blue = o["label"].(string)
					}
				}
				if blue == "" {
					t.Fatalf("no Blue option in %v", opts)
				}
				if err := a.ChatAnswerQuestion(1, e["req"].(string), map[string][]string{qs[0]["id"].(string): {blue}}, false); err != nil {
					t.Fatal(err)
				}
			case "ask":
				t.Logf("a permission was asked instead: %v", e)
				a.ChatAnswer(1, e["req"].(string), "allow")
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
	t.Logf("asked=%v answer: %s", asked, strings.TrimSpace(text.String()))
	if !asked {
		t.Error("the AI never asked a question")
	}
	if !strings.Contains(strings.ToLower(text.String()), "blue") {
		t.Errorf("the AI did not get the answer: %q", text.String())
	}
}
