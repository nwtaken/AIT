package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Prewarming. An agent takes seconds to start (measured: ~2.7s for Claude on
// a slow machine, before AIT's own window has even loaded), so one is always
// started ahead of time: the first at app launch, in parallel with the window,
// and the next a little after each one is taken. Opening a tab adopts it and
// the agent is simply there.
//
// A standby is a normal Tab that has no id yet. Its output is kept in a
// backlog instead of being sent to the page, and handed over on adoption.

const standbyDelay = 20 * time.Second // after an adoption, so it never competes with the tab just opened

func (a *App) prepareStandby() {
	cfg := a.store.Config()
	if !cfg.prewarm() {
		return
	}
	p := registry[cfg.DefaultProfile]
	if p == nil || p.Command() == nil {
		return
	}
	a.mu.Lock()
	busy := a.standby != nil
	a.mu.Unlock()
	if busy {
		return
	}
	acct, ok := a.store.Pick(p.ID(), "", time.Now())
	if !ok {
		return
	}
	cols, rows := a.lastSize()
	t := &Tab{profile: p.ID(), agent: p, cwd: cfg.StartingDir, cols: cols, rows: rows}
	t.trusted = a.store.Trusted(t.cwd)
	t.mu.Lock()
	err := a.launch(t, acct, "")
	t.mu.Unlock()
	if err != nil {
		return
	}
	a.mu.Lock()
	a.standby = t
	a.mu.Unlock()
}

// adoptStandby hands the prewarmed agent to a new tab when it fits the
// request. It returns nil when there is none, or it no longer fits.
func (a *App) adoptStandby(r OpenRequest) (*Tab, []byte, int, int, []Ev) {
	a.mu.Lock()
	t := a.standby
	if t == nil || t.profile != r.Profile || r.Chat != "" || r.Account != "" || r.Model != "" {
		a.mu.Unlock()
		return nil, nil, 0, 0, nil
	}
	a.standby = nil
	a.mu.Unlock()

	t.mu.Lock()
	acct, ok := a.store.Account(t.acct)
	model := a.store.Config().Models[t.profile]
	if acct.Model != nil {
		model = *acct.Model
	}
	if (t.pty == nil && t.chat == nil) || t.closed || !ok || !a.store.Available(acct, time.Now()) || model != t.model {
		t.closed = true
		if t.pty != nil {
			go t.pty.Close()
		}
		t.stopChat()
		t.mu.Unlock()
		return nil, nil, 0, 0, nil
	}
	cols, rows := t.cols, t.rows
	t.id = r.ID
	a.mu.Lock()
	a.tabs[r.ID] = t
	if t.session != "" && a.claims[t.session] == 0 {
		a.claims[t.session] = r.ID
	}
	a.mu.Unlock()
	t.backMu.Lock()
	back, evs := t.backlog, t.evBacklog
	t.backlog, t.evBacklog = nil, nil
	if evs == nil {
		evs = []Ev{}
	}
	t.adopted.Store(true)
	t.backMu.Unlock()
	t.mu.Unlock()

	if t.promptShown.Load() && !t.autoTrust.Load() {
		a.emit("tab:prompt", t.id, "trust", maskUser(t.cwd))
	}
	time.AfterFunc(standbyDelay, a.prepareStandby)
	return t, back, cols, rows, evs
}

// standbyExited forgets a standby whose process died before anyone took it.
func (a *App) standbyExited(t *Tab) {
	a.mu.Lock()
	if a.standby == t {
		a.standby = nil
	}
	a.mu.Unlock()
}

func (a *App) killStandby() {
	a.mu.Lock()
	t := a.standby
	a.standby = nil
	a.mu.Unlock()
	if t != nil {
		t.mu.Lock()
		t.closed = true
		if t.pty != nil {
			t.pty.Close()
		}
		t.stopChat()
		t.mu.Unlock()
	}
}

// lastSize is the terminal size the page last reported, so a standby starts
// at the size it will be shown at. Before any report: the default window.
func (a *App) lastSize() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cols > 0 {
		return a.cols, a.rows
	}
	cfg := a.store.Config()
	cw, ch := float64(cfg.FontSize)*0.6, float64(cfg.FontSize)*1.15+0.5
	return int((1100 - 16) / cw), int((680 - 38 - 12) / ch)
}

// ---- remembered folder trust -------------------------------------------------

// A folder is remembered as trusted once a conversation has started in it in
// AIT — answering the agent's trust prompt is what made that possible. From
// then on AIT answers the prompt itself in that folder.

func (s *Store) trustPath() string { return filepath.Join(s.root, "trusted.json") }

func (s *Store) trustedList() []string {
	var l []string
	if b, err := os.ReadFile(s.trustPath()); err == nil {
		json.Unmarshal(b, &l)
	}
	return l
}

func (s *Store) Trusted(dir string) bool {
	for _, d := range s.trustedList() {
		if strings.EqualFold(filepath.Clean(d), filepath.Clean(dir)) {
			return true
		}
	}
	return false
}

func (s *Store) Trust(dir string) {
	if dir == "" || s.Trusted(dir) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := append(s.trustedList(), filepath.Clean(dir))
	b, _ := json.MarshalIndent(l, "", "  ")
	os.WriteFile(s.trustPath(), b, 0o644)
}
