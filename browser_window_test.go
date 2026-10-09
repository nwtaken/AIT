package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestBrowserTitle(t *testing.T) {
	if got := browserTitle("Claude", ""); got != "AIT Browser · Claude" {
		t.Errorf("%q", got)
	}
	if got := browserTitle("Claude", "about:blank"); got != "AIT Browser · Claude" {
		t.Errorf("blank page: %q", got)
	}
	if got := browserTitle("ChatGPT", "Example Domain"); got != "Example Domain — AIT Browser · ChatGPT" {
		t.Errorf("%q", got)
	}
}

// A browser the AI left open is closed once the AI has stopped, nothing waits
// for the user and it has not been used for a while; never earlier.
func TestBrowserIdleClose(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	tab := &Tab{id: 3, agent: registry["claude"], profile: "claude"}
	tab.adopted.Store(true)
	l := &browserLog{key: "k", tab: tab, ai: "Claude", allowed: map[string]bool{}}
	app.browser.logs = map[string]*browserLog{"k": l}
	now := time.Now()
	late := now.Add(browserIdleAfter + time.Second)
	if app.browserShouldReap(l, late) {
		t.Fatal("there is no window to close")
	}
	l.view = &browserView{key: "k"} // a window, as far as the check goes
	app.browserUsed(l)
	if app.browserShouldReap(l, now) {
		t.Error("closed a browser that was just used")
	}
	if !app.browserShouldReap(l, late) {
		t.Error("an unused browser is left open")
	}
	tab.working.Store(true)
	if app.browserShouldReap(l, late) {
		t.Error("closed a browser while the AI is working")
	}
	tab.working.Store(false)
	app.browser.mu.Lock()
	app.browser.asks = map[string]*browserAsk{"a": {n: 1, key: "k", help: true, message: "Solve the CAPTCHA"}}
	app.browser.mu.Unlock()
	if app.browserShouldReap(l, late) {
		t.Error("closed a browser while it waits for the user")
	}
	app.browser.mu.Lock()
	delete(app.browser.asks, "a")
	app.browser.mu.Unlock()
	if !app.browserShouldReap(l, late) {
		t.Error("not closed once the question is answered")
	}
}

// browser_close: the gate tells AIT, the window goes (the log stays), and the
// chat's button hides.
func TestBrowserCloseEndpoint(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	var events []string
	app.emit = func(event string, data ...any) { events = append(events, event) }
	tab := &Tab{id: 3, agent: registry["claude"], profile: "claude"}
	tab.adopted.Store(true)
	app.tabs[3] = tab
	l := &browserLog{key: "k", tab: tab, ai: "Claude", allowed: map[string]bool{}}
	app.browser.logs = map[string]*browserLog{"k": l}
	tab.browserKey.Store("k")
	app.browserLogStep(l, "do", "Open example.com")
	l.view = &browserView{key: "k"}
	base, _ := app.browserBase()
	res, err := http.Get(base + "/close?key=k")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "ok" || l.view != nil {
		t.Fatalf("not closed: %q %v", b, l.view)
	}
	if len(events) == 0 || events[len(events)-1] != "browser:gone" {
		t.Errorf("the chat is not told: %v", events)
	}
	if st := app.BrowserState(3); st["open"] != false || len(st["steps"].([]BrowserStep)) != 1 {
		t.Errorf("the steps stay after closing: %v", st)
	}
	if res, _ := http.Get(base + "/close?key=nope"); res.StatusCode != 200 {
		t.Errorf("an unknown key is ignored quietly: %d", res.StatusCode)
	}
}

// The column is sent the page, the steps in order with their numbers, and the
// questions waiting, oldest first, each with the right wording and buttons.
func TestBrowserColumnState(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	tab := &Tab{id: 3, agent: registry["claude"], profile: "claude"}
	tab.working.Store(true)
	l := &browserLog{key: "k", tab: tab, ai: "Claude", allowed: map[string]bool{}}
	app.browser.logs = map[string]*browserLog{"k": l}
	app.browserLogStep(l, "plan", "I'll open the docs.")
	app.browserLogStep(l, "do", "Open example.com")
	app.browser.mu.Lock()
	app.browser.asks = map[string]*browserAsk{
		"b": {n: 2, key: "k", help: true, message: "Solve the CAPTCHA"},
		"a": {n: 1, key: "k", host: "example.org"},
		"x": {n: 3, key: "other", host: "elsewhere.com"},
	}
	app.browser.theme = `{"vars":{"--accent":"#d97757"},"theme":"campbell"}`
	app.browser.mu.Unlock()
	v := &browserView{key: "k", url: "https://example.com/", title: "Example Domain"}
	var s struct {
		AI    string
		URL   string
		Title string
		Busy  bool
		Keep  bool
		Steps []BrowserStep
		Asks  []struct {
			Req    string
			Help   bool
			Text   string
			Always bool
			Labels map[string]string
		}
		Theme struct {
			Vars  map[string]string
			Theme string
		}
	}
	if err := json.Unmarshal(app.browserStateJSON(l, v), &s); err != nil {
		t.Fatal(err)
	}
	if s.AI != "Claude" || s.URL != "https://example.com/" || s.Title != "Example Domain" || !s.Busy || s.Keep {
		t.Errorf("page: %+v", s)
	}
	if len(s.Steps) != 2 || s.Steps[0].Seq != 1 || s.Steps[1].Seq != 2 || s.Steps[1].Text != "Open example.com" {
		t.Errorf("steps: %+v", s.Steps)
	}
	if len(s.Asks) != 2 || s.Asks[0].Req != "a" || s.Asks[0].Text != "example.org" || !s.Asks[0].Always || s.Asks[0].Help {
		t.Fatalf("site question: %+v", s.Asks)
	}
	if h := s.Asks[1]; h.Req != "b" || !h.Help || h.Text != "Solve the CAPTCHA" || h.Always || h.Labels["allow"] != "I'm done" || h.Labels["deny"] != "Can't do it" {
		t.Errorf("help question: %+v", h)
	}
	if s.Theme.Vars["--accent"] != "#d97757" || s.Theme.Theme != "campbell" {
		t.Errorf("theme: %+v", s.Theme)
	}
	// Answers from the column only reach this browser's own questions.
	got := make(chan string, 1)
	app.browser.mu.Lock()
	app.browser.asks["a"].answer = got
	app.browser.mu.Unlock()
	app.browserColumnMessage(&browserLog{key: "other"}, v, "answer:a|allow")
	select {
	case <-got:
		t.Error("another browser's column answered it")
	case <-time.After(150 * time.Millisecond):
	}
	app.browserColumnMessage(l, v, "answer:a|always")
	select {
	case d := <-got:
		if d != "always" {
			t.Errorf("decision %q", d)
		}
	case <-time.After(2 * time.Second):
		t.Error("the column's answer did not arrive")
	}
}

// With permissions switched to "never ask" the AI browses freely: no question
// about a site, and the gate is told so at each call (the setting can change
// while a chat runs).
func TestBrowserFreeWithoutPermissions(t *testing.T) {
	store, _ := newStoreAt(t.TempDir(), t.TempDir())
	app := NewApp(store)
	var asked int
	app.emit = func(event string, data ...any) {
		if event == "chat:ev" {
			for _, e := range data[1].([]Ev) {
				if e["k"] == "ask" {
					asked++
				}
			}
		}
	}
	tab := &Tab{id: 3, agent: registry["claude"], profile: "claude"}
	tab.adopted.Store(true)
	app.tabs[3] = tab
	app.browser.logs = map[string]*browserLog{"k": {key: "k", tab: tab, ai: "Claude", allowed: map[string]bool{}}}
	base, _ := app.browserBase()
	get := func(path string) string {
		res, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	setPerm := func(p string) {
		c := store.Config()
		c.Permissions = p
		store.saveConfig(c)
	}
	setPerm("never")
	if m := get("/mode?key=k"); m != "free" {
		t.Errorf("mode with permissions off: %q", m)
	}
	if r := get("/ask?key=k&host=example.org"); r != "allow" || asked != 0 {
		t.Errorf("a site was asked about with permissions off: %q, %d cards", r, asked)
	}
	setPerm("ask")
	if m := get("/mode?key=k"); m != "ask" {
		t.Errorf("mode with permissions on: %q", m)
	}
	done := make(chan string, 1)
	go func() { done <- get("/ask?key=k&host=example.org") }()
	for i := 0; i < 300 && func() bool { app.browser.mu.Lock(); defer app.browser.mu.Unlock(); return len(app.browser.asks) == 0 }(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if err := app.ChatAnswer(3, askPrefix+"1", "deny"); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r != "deny" {
		t.Errorf("with permissions on the site is asked about: %q", r)
	}
}
