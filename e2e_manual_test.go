//go:build manual

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real CLI, real accounts: a turn on the main login, then the same
// conversation carried to the second account and continued there.
func TestRealCarryAcrossAccounts(t *testing.T) {
	home, _ := os.UserHomeDir()
	s, err := newStoreAt(filepath.Join(os.Getenv("APPDATA"), "AIT"), home)
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp(s)
	var mu sync.Mutex
	var screen strings.Builder
	a.emit = func(ev string, d ...any) {
		if ev == "pty:out" {
			b, _ := base64.StdEncoding.DecodeString(d[1].(string))
			mu.Lock()
			screen.Write(b)
			mu.Unlock()
		}
	}
	plain := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(strings.Fields(ansiRe.ReplaceAllString(screen.String(), " ")), " ")
	}
	tail := func() string { p := plain(); return p[max(0, len(p)-900):] }

	cfgArgs := s.Config()
	_ = cfgArgs
	t.Setenv("AIT_TEST", "1")
	// Haiku keeps the two test turns cheap.
	cl := registry["claude"]
	registry["claude"] = withArgs{cl, []string{"--model", "haiku"}}

	tab := &Tab{id: 1, profile: "claude", agent: registry["claude"], cwd: home, cols: 120, rows: 40, trusted: true}
	a.tabs[1] = tab
	main, _ := s.Account("main")
	second, _ := s.Account("claude-02")

	tab.mu.Lock()
	if err := a.launch(tab, main, ""); err != nil {
		t.Fatal(err)
	}
	tab.mu.Unlock()
	defer a.Close(1)

	time.Sleep(8 * time.Second)
	tab.mu.Lock()
	tab.pty.Write([]byte("Reply with only the word: pong"))
	time.Sleep(300 * time.Millisecond)
	tab.pty.Write([]byte("\r"))
	first := tab.session
	tab.mu.Unlock()

	if !waitFor(60*time.Second, func() bool { return assistantSaid(first, "pong") }) {
		t.Fatalf("no reply on main. session=%s\nscreen: %s", first, tail())
	}
	t.Logf("main replied in %s", first)

	tab.mu.Lock()
	if err := a.relaunch(tab, second, "Reply with only the word: ping"); err != nil {
		t.Fatal(err)
	}
	moved := tab.session
	tab.mu.Unlock()
	if !strings.Contains(moved, "claude-02") {
		t.Fatalf("transcript not moved: %s", moved)
	}
	if !waitFor(90*time.Second, func() bool { return assistantSaid(moved, "ping") }) {
		t.Fatalf("no reply on claude-02. session=%s\nscreen: %s", moved, tail())
	}
	t.Logf("claude-02 continued the same conversation: %s", moved)
}

type withArgs struct {
	Provider
	extra []string
}

func (w withArgs) Args(l Launch) []string {
	l.Extra = append(w.extra, l.Extra...)
	return w.Provider.Args(l)
}

func waitFor(d time.Duration, f func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return true
		}
		time.Sleep(time.Second)
	}
	return false
}

func assistantSaid(path, word string) bool {
	b, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, `"type":"assistant"`) && !strings.Contains(l, "isApiErrorMessage\":true") && strings.Contains(strings.ToLower(l), `"text":"`+word) {
			return true
		}
	}
	return false
}
