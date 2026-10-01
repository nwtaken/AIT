//go:build manual

package main

import (
	"encoding/base64"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real CLI: the trust prompt becomes a card event; answering it reaches the prompt.
func TestRealTrustCard(t *testing.T) {
	home, _ := os.UserHomeDir()
	store, err := newStoreAt(t.TempDir(), home)
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(store)
	var mu sync.Mutex
	var raw strings.Builder
	prompted := make(chan string, 1)
	a.emit = func(ev string, d ...any) {
		switch ev {
		case "pty:out":
			b, _ := base64.StdEncoding.DecodeString(d[1].(string))
			mu.Lock()
			raw.Write(b)
			mu.Unlock()
		case "tab:prompt":
			prompted <- d[2].(string)
		}
	}
	start := time.Now()
	if _, err := a.Open(OpenRequest{ID: 1, Profile: "claude", Cols: 120, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	defer a.Close(1)
	select {
	case f := <-prompted:
		t.Logf("trust card asked after %v for %q", time.Since(start).Round(time.Millisecond), f)
	case <-time.After(20 * time.Second):
		t.Fatal("no trust card event")
	}
	answered := time.Now()
	a.AnswerTrust(1, true)
	for time.Since(answered) < 15*time.Second {
		mu.Lock()
		s := plain([]byte(raw.String()))
		mu.Unlock()
		if strings.Contains(s[max(0, len(s)-3000):], "for shortcuts") || strings.Contains(s[max(0, len(s)-3000):], "mode on") {
			t.Logf("ready %v after answering; trusted now: %v", time.Since(answered).Round(time.Millisecond), store.Trusted(home))
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	s := plain([]byte(raw.String()))
	mu.Unlock()
	t.Fatalf("not ready after answering: %q", s[max(0, len(s)-500):])
}
