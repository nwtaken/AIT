package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The AI hands the user a step (a CAPTCHA): a card in the chat (and in the
// browser pane), and the AI waits until the user answers.
func TestBrowserAskUser(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	var mu sync.Mutex
	var evs []Ev
	app.emit = func(event string, data ...any) {
		if event == "chat:ev" {
			mu.Lock()
			evs = append(evs, data[1].([]Ev)...)
			mu.Unlock()
		}
	}
	asked := 0
	tab := &Tab{id: 7, agent: registry["claude"], profile: "claude"}
	tab.adopted.Store(true)
	app.tabs[7] = tab
	app.browser.logs = map[string]*browserLog{"k": {key: "k", tab: tab, ai: "Claude", allowed: map[string]bool{}}}
	tab.browserKey.Store("k")
	base, _ := app.browserBase()
	help := func(msg string) chan string {
		out := make(chan string, 1)
		go func() {
			res, err := http.Get(base + "/help?key=k&message=" + url.QueryEscape(msg))
			if err != nil {
				out <- err.Error()
				return
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			out <- string(b)
		}()
		return out
	}
	waitAsk := func() Ev { // the next card
		asked++
		for i := 0; i < 300; i++ {
			mu.Lock()
			n := 0
			for _, e := range evs {
				if e["k"] == "ask" {
					n++
					if n == asked {
						mu.Unlock()
						return e
					}
				}
			}
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("no card")
		return nil
	}
	// Answered on the card (the pane's buttons answer the same way).
	out := help("Solve the CAPTCHA")
	e := waitAsk()
	labels, _ := e["labels"].(map[string]any)
	if e["tool"] != "BrowserHelp" || e["desc"] != "Solve the CAPTCHA" || labels["allow"] != "I'm done" || labels["deny"] != "Can't do it" {
		t.Fatalf("card %v", e)
	}
	req := e["req"].(string)
	if !strings.HasPrefix(req, askPrefix+"help-") {
		t.Fatalf("request id %q", req)
	}
	if err := app.ChatAnswer(7, req, "allow"); err != nil {
		t.Fatal(err)
	}
	if r := <-out; r != "allow" {
		t.Fatalf("the AI is told: %s", r)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	last := evs[len(evs)-1]
	mu.Unlock()
	if last["k"] != "askclose" || last["req"] != req || !strings.HasPrefix(last["text"].(string), "✓") {
		t.Errorf("the card is closed: %v", last)
	}

	// Answered on the card in the chat.
	out = help("Sign in")
	e = waitAsk()
	if err := app.ChatAnswer(7, e["req"].(string), "deny"); err != nil {
		t.Fatal(err)
	}
	if r := <-out; r != "deny" {
		t.Fatalf("skipped: %s", r)
	}
	var texts []string
	for _, st := range app.BrowserState(7)["steps"].([]BrowserStep) {
		texts = append(texts, st.Kind+": "+st.Text)
	}
	want := "help: Waiting for you: Solve the CAPTCHA|do: You finished that step|help: Waiting for you: Sign in|error: That step was skipped"
	if strings.Join(texts, "|") != want {
		t.Errorf("steps %q, want %q", strings.Join(texts, "|"), want)
	}
	if got := browserAction("browser_ask_user", map[string]any{"message": "Solve the CAPTCHA"}); got != "Ask you: Solve the CAPTCHA" {
		t.Errorf("action %q", got)
	}
}

// Codex asks before an MCP tool runs, with an elicitation request that AIT
// used to ignore, which left the chat waiting forever.
func TestCodexMcpApproval(t *testing.T) {
	st := &ChatState{Asks: map[string]json.RawMessage{}, Data: map[string]any{}, Send: func([]byte) {}}
	line := []byte(`{"jsonrpc":"2.0","id":5,"method":"mcpServer/elicitation/request","params":{"threadId":"t","serverName":"elevenlabs","mode":"form","message":"Allow the elevenlabs MCP server to run tool \"play_audio\"?","requestedSchema":{"type":"object","properties":{}}}}`)
	evs := (&codex{}).ChatDecode(line, st)
	if len(evs) != 1 || evs[0]["k"] != "ask" || evs[0]["tool"] != "mcp__elevenlabs" || !strings.Contains(evs[0]["desc"].(string), "play_audio") || evs[0]["req"] != "5" {
		t.Fatalf("card %v", evs)
	}
	reply := func(d string) string {
		return strings.TrimSpace(string((&codex{}).ChatReply(st, "5", d, st.Asks["5"])))
	}
	if got := reply("allow"); got != `{"id":5,"result":{"action":"accept"}}` {
		t.Errorf("allow: %s", got)
	}
	if got := reply("deny"); got != `{"id":5,"result":{"action":"decline"}}` {
		t.Errorf("deny: %s", got)
	}
}
