//go:build manual

package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Real Codex in native chat: handshake, a message, an approval, a reply.
func TestRealCodexNativeChat(t *testing.T) {
	home, _ := os.UserHomeDir()
	store, err := newStoreAt(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Config()
	c.Args = map[string][]string{"codex": {"-m", "gpt-5.6-luna"}}
	c.StartingDir = t.TempDir()
	c.MemoryDir = t.TempDir()
	if os.Getenv("ACCESS") != "" {
		c.Access = os.Getenv("ACCESS")
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
	info, err := a.Open(OpenRequest{ID: 1, Profile: "codex", Cols: 100, Rows: 30})
	if err != nil || !info.Native {
		t.Fatalf("open: %+v %v", info, err)
	}
	defer a.Close(1)
	a.ChatSend(1, "Run the command `echo hello-ait`, then reply with exactly: done", nil)
	kinds := map[string]int{}
	deadline := time.After(150 * time.Second)
	for {
		select {
		case e := <-evs:
			k := e["k"].(string)
			kinds[k]++
			if k == "ask" || k == "tool" || k == "result" || k == "error" || k == "init" {
				b, _ := json.Marshal(e)
				t.Logf("%s", b)
			}
			if k == "ask" {
				tb := a.tab(1)
				ask := tb.chatState.Asks[e["req"].(string)]
				t.Logf("reply line: %s", (&codex{}).ChatReply(tb.chatState, e["req"].(string), "allow", ask))
				t.Logf("answer err: %v", a.ChatAnswer(1, e["req"].(string), "allow"))
			}
			if k == "done" {
				t.Logf("counts %v", kinds)
				if kinds["init"] == 0 || kinds["delta"] == 0 || kinds["result"] == 0 {
					t.Fatal("missing init, streamed text or a tool result")
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out; seen %v", kinds)
		}
	}
}
