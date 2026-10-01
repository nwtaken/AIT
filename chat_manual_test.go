//go:build manual

package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Real CLI, native chat: a message, a permission card answered, a reply.
func TestRealNativeChat(t *testing.T) {
	home, _ := os.UserHomeDir()
	root := t.TempDir()
	store, err := newStoreAt(root, home)
	if err != nil {
		t.Fatal(err)
	}
	c := store.Config()
	c.Args = map[string][]string{"claude": {"--model", "haiku"}}
	c.StartingDir = t.TempDir()
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
	info, err := a.Open(OpenRequest{ID: 1, Profile: "claude", Cols: 100, Rows: 30})
	if err != nil || !info.Native {
		t.Fatalf("open: %+v %v", info, err)
	}
	defer a.Close(1)
	a.ChatSend(1, "Create a file named hello.txt containing the word hi, using your Write tool. Then reply with exactly: done", nil)
	kinds := map[string]int{}
	deadline := time.After(120 * time.Second)
	for {
		select {
		case e := <-evs:
			k := e["k"].(string)
			kinds[k]++
			if k == "ask" {
				b, _ := json.Marshal(e)
				t.Logf("ask: %s", b)
				a.ChatAnswer(1, e["req"].(string), "allow")
			}
			if k == "done" {
				t.Logf("event counts: %v", kinds)
				if kinds["ask"] == 0 || kinds["delta"] == 0 || kinds["result"] == 0 {
					t.Fatal("missing ask, text or tool result")
				}
				if _, err := os.Stat(c.StartingDir + `\hello.txt`); err != nil {
					t.Fatal("the allowed write did not happen")
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out; seen %v", kinds)
		}
	}
}
