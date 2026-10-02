package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Reopening the tabs from last time. The open AI tabs (their conversation
// files, in order, and which one was active) are saved to tabs.json whenever
// that changes and when AIT quits; the next start reopens them.

// SavedTab is one tab to reopen.
type SavedTab struct {
	Provider string `json:"provider"`
	Ref      string `json:"ref"`  // the conversation's transcript, as History refs it
	Size     int64  `json:"size"` // for the page: very big chats reopen with their summary
	Active   bool   `json:"active"`
	Draft    string `json:"draft,omitempty"` // unsent text in the tab's box
}

type tabsSaver struct {
	sync.Mutex
	timer *time.Timer
	done  bool // AIT is quitting: the final save is written
}

func (s *Store) tabsPath() string { return filepath.Join(s.root, "tabs.json") }

// queueTabsSave saves the open tabs shortly. Callers often hold a tab's lock,
// which saving takes too, so it never saves inline.
func (a *App) queueTabsSave() {
	a.tabsSave.Lock()
	defer a.tabsSave.Unlock()
	if a.tabsSave.done {
		return
	}
	if a.tabsSave.timer != nil {
		a.tabsSave.timer.Stop()
	}
	a.tabsSave.timer = time.AfterFunc(500*time.Millisecond, a.saveOpenTabs)
}

// finalTabsSave writes the tabs as AIT quits; nothing saves after it, so
// closing them on the way out cannot empty the list.
func (a *App) finalTabsSave() {
	a.tabsSave.Lock()
	a.tabsSave.done = true
	if a.tabsSave.timer != nil {
		a.tabsSave.timer.Stop()
	}
	a.tabsSave.Unlock()
	a.saveOpenTabs()
}

// saveOpenTabs writes the AI tabs that have a conversation, oldest first.
func (a *App) saveOpenTabs() {
	a.mu.Lock()
	var tabs []*Tab
	for _, t := range a.tabs {
		tabs = append(tabs, t)
	}
	a.mu.Unlock()
	slices.SortFunc(tabs, func(x, y *Tab) int { return x.id - y.id })
	active := int(a.activeTab.Load())
	out := []SavedTab{}
	for _, t := range tabs { // t.mu is never taken while holding a.mu
		t.mu.Lock()
		if t.agent != nil && !t.closed && t.adopted.Load() && t.session != "" {
			out = append(out, SavedTab{Provider: t.profile, Ref: t.session, Active: t.id == active, Draft: t.draft})
		}
		t.mu.Unlock()
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	os.WriteFile(a.store.tabsPath(), b, 0o644)
}

// SetDraft keeps the unsent text in a tab's box.
func (a *App) SetDraft(id int, text string) {
	t := a.tab(id)
	if t == nil {
		return
	}
	t.mu.Lock()
	changed := t.draft != text
	t.draft = text
	t.mu.Unlock()
	if changed {
		a.queueTabsSave()
	}
}

// SetActiveTab is told by the page which tab is in front.
func (a *App) SetActiveTab(id int) {
	if a.activeTab.Swap(int64(id)) != int64(id) {
		a.queueTabsSave()
	}
}

// LastTabs lists the tabs to reopen: those whose conversation is still on
// disk, unless the user switched reopening off.
func (a *App) LastTabs() []SavedTab {
	out := []SavedTab{}
	if !a.store.Config().reopenTabs() {
		return out
	}
	b, err := os.ReadFile(a.store.tabsPath())
	if err != nil {
		return out
	}
	var saved []SavedTab
	json.Unmarshal(b, &saved)
	for _, s := range saved {
		if info, err := os.Stat(s.Ref); err == nil && provider(s.Provider) != nil {
			s.Size = info.Size()
			out = append(out, s)
		}
	}
	return out
}
