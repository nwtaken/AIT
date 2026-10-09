package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The AIs' own web browser: a window of AIT's, like a live stream. Playwright's
// MCP server (installed once into AIT's folder) drives the page in it through
// its debugging port (browserview.go); the column beside the page, with the
// steps and the questions, is AIT's own. gate.cjs sits between the AI and
// Playwright: it opens the window on first use, closes it with browser_close,
// asks the user before a site is opened for the first time and lets the AI
// hand a step to the user.
//
//go:embed browserassets/gate.cjs
var browserAssets embed.FS

const (
	browserServer = "ait-browser"
	browserPrefix = "mcp__" + browserServer + "__"
	askPrefix     = "ait-browser-" // permission cards for sites, answered through ChatAnswer
)

// BrowserStep is one line of the browser window's steps.
type BrowserStep struct {
	Seq  int    `json:"seq"`
	Kind string `json:"kind"` // plan | do | error
	Text string `json:"text"`
}

// browserLog is what one launch of one chat has done in its browser.
type browserLog struct {
	key     string
	tab     *Tab
	ai      string
	port    int // the surface's debugging port
	steps   []BrowserStep
	seq     int
	allowed map[string]bool // sites the user allowed for this chat's browser

	viewMu sync.Mutex
	view   *browserView // nil until the AI first uses the browser, and again once it is closed
	last   time.Time    // the AI's last use of the browser (a.browser.mu)
}

type browserAsk struct {
	n         int // order of asking
	key, host string
	help      bool   // the AI asks the user to do a step in the browser (host is empty)
	message   string // what the user is asked to do
	answer    chan string
}

type browserHub struct {
	mu      sync.Mutex
	base    string
	logs    map[string]*browserLog // by key, one per launch
	asks    map[string]*browserAsk // pending, by request id
	nextAsk int
	theme   string // AIT's colours as the main page last sent them (JSON)
}

// sortedAsks lists a browser's pending questions in the order they were asked.
func sortedAsks(asks map[string]*browserAsk, key string) []string {
	var reqs []string
	for req, p := range asks {
		if p.key == key {
			reqs = append(reqs, req)
		}
	}
	slices.SortFunc(reqs, func(x, y string) int { return asks[x].n - asks[y].n })
	return reqs
}

func (a *App) browserPending(req string) *browserAsk {
	a.browser.mu.Lock()
	defer a.browser.mu.Unlock()
	return a.browser.asks[req]
}

func (s *Store) browserDir() string { return filepath.Join(s.root, "browser") }

func (s *Store) browserCLI() string {
	return filepath.Join(s.browserDir(), "node_modules", "@playwright", "mcp", "cli.js")
}

func nodeBinary() string {
	return lookBinary("node", filepath.Join(os.Getenv("ProgramFiles"), "nodejs", "node.exe"))
}

// browserReady reports whether the AIs can have a browser on this PC (Node.js
// runs the server; the surface is WebView2, which AIT itself needs).
func (s *Store) browserReady() bool {
	return nodeBinary() != "" && fileExists(s.browserCLI())
}

// browserInstall fetches Playwright's MCP server once, in the background. It
// never installs Node.js itself: without it the AIs just have no browser.
func (a *App) browserInstall() {
	dir := a.store.browserDir()
	os.MkdirAll(dir, 0o755)
	cleanBrowserFiles(dir)
	if fileExists(a.store.browserCLI()) {
		return
	}
	npm := lookBinary("npm.cmd", filepath.Join(os.Getenv("ProgramFiles"), "nodejs", "npm.cmd"))
	if npm == "" || nodeBinary() == "" {
		return
	}
	run(npm, "install", "--prefix", dir, "--no-audit", "--no-fund", "--loglevel=error", "@playwright/mcp@latest")
}

// cleanBrowserFiles removes what earlier runs left: per-chat files, private
// profiles and what Playwright saved.
func cleanBrowserFiles(dir string) {
	for _, pat := range []string{"overlay-*.js", "config-*.json"} {
		old, _ := filepath.Glob(filepath.Join(dir, pat))
		for _, f := range old {
			os.Remove(f)
		}
	}
	os.Remove(filepath.Join(dir, "overlay.js"))
	os.RemoveAll(filepath.Join(dir, "output"))
	os.RemoveAll(filepath.Join(dir, "tmp"))
}

// browserMcp is the browser server for one launch of a chat, or nil when it
// is not available or the user switched it off for this AI. The surface
// itself starts only when the AI first uses the browser.
func (a *App) browserMcp(t *Tab, p Provider) *McpServer {
	if slices.Contains(a.store.Config().McpOff[p.ID()], browserServer) || !a.store.browserReady() {
		return nil
	}
	base, err := a.browserBase()
	if err != nil {
		return nil
	}
	port, err := freePort()
	if err != nil {
		return nil
	}
	dir := a.store.browserDir()
	if old, _ := t.browserKey.Load().(string); old != "" { // a relaunch: the old surface and log are gone
		a.closeBrowser(old)
		os.Remove(filepath.Join(dir, "config-"+old+".json"))
	}
	key := newUUID()
	t.browserKey.Store(key)
	a.browser.mu.Lock()
	if a.browser.logs == nil {
		a.browser.logs = map[string]*browserLog{}
	}
	a.browser.logs[key] = &browserLog{key: key, tab: t, ai: p.Name(), port: port, allowed: map[string]bool{}}
	a.browser.mu.Unlock()

	gate, _ := browserAssets.ReadFile("browserassets/gate.cjs")
	gatePath := filepath.Join(dir, "gate.cjs")
	cfgPath := filepath.Join(dir, "config-"+key+".json")
	cfg, _ := json.Marshal(map[string]any{
		"browser":   map[string]any{"browserName": "chromium", "cdpEndpoint": fmt.Sprintf("http://127.0.0.1:%d", port)},
		"outputDir": filepath.Join(dir, "output"),
	})
	if os.WriteFile(gatePath, gate, 0o644) != nil || os.WriteFile(cfgPath, cfg, 0o644) != nil {
		return nil
	}
	return &McpServer{Name: browserServer, Command: nodeBinary(),
		Args: []string{gatePath, "--ait", base, "--key", key, "--cli", a.store.browserCLI(), "--config", cfgPath}}
}

// browserBase starts the small local server the gate talks to (127.0.0.1
// only; every launch has its own secret key).
func (a *App) browserBase() (string, error) {
	h := &a.browser
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.base != "" {
		return h.base, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/open", a.browserOpenHTTP)
	mux.HandleFunc("/ask", a.browserAskHTTP)
	mux.HandleFunc("/help", a.browserHelpHTTP)
	mux.HandleFunc("/front", a.browserFrontHTTP)
	mux.HandleFunc("/close", a.browserCloseHTTP)
	go http.Serve(ln, mux)
	h.base = "http://" + ln.Addr().String()
	return h.base, nil
}

// browserTouch tells the browser window that the chat started or stopped working.
func (a *App) browserTouch(t *Tab) {
	if key, _ := t.browserKey.Load().(string); key != "" {
		if l := a.browserLogFor(key); l != nil {
			a.browserPush(l)
		}
	}
}

func (a *App) browserLogFor(key string) *browserLog {
	a.browser.mu.Lock()
	defer a.browser.mu.Unlock()
	return a.browser.logs[key]
}

// browserCloseHTTP is the gate telling AIT the AI closed its browser: the
// window goes. The next browser call opens a fresh one.
func (a *App) browserCloseHTTP(w http.ResponseWriter, r *http.Request) {
	if l := a.browserLogFor(r.URL.Query().Get("key")); l != nil {
		a.browserCloseView(l)
	}
	fmt.Fprint(w, "ok")
}

// browserCloseView closes the browser's window but keeps the chat's log.
func (a *App) browserCloseView(l *browserLog) {
	l.viewMu.Lock()
	v := l.view
	l.view = nil
	l.viewMu.Unlock()
	if v == nil {
		return
	}
	a.destroyBrowserView(v)
	if l.tab != nil {
		a.emit("browser:gone", l.tab.id)
	}
}

// browserIdleAfter is how long an unused browser stays open once the AI has
// stopped working, in case it forgot to close it.
const browserIdleAfter = 3 * time.Minute

// browserShouldReap: the AI is not working, nothing is waiting for the user,
// and the browser has not been used for a while.
func (a *App) browserShouldReap(l *browserLog, now time.Time) bool {
	l.viewMu.Lock()
	open := l.view != nil
	l.viewMu.Unlock()
	if !open || l.tab == nil || l.tab.working.Load() {
		return false
	}
	a.browser.mu.Lock()
	defer a.browser.mu.Unlock()
	return len(sortedAsks(a.browser.asks, l.key)) == 0 && !l.last.IsZero() && now.Sub(l.last) > browserIdleAfter
}

// browserReap closes browsers the AI left open.
func (a *App) browserReap() {
	for now := range time.Tick(20 * time.Second) {
		a.browser.mu.Lock()
		logs := make([]*browserLog, 0, len(a.browser.logs))
		for _, l := range a.browser.logs {
			logs = append(logs, l)
		}
		a.browser.mu.Unlock()
		for _, l := range logs {
			if a.browserShouldReap(l, now) {
				a.browserLogStep(l, "do", "Closed the browser: it was not used for a while")
				a.browserCloseView(l)
			}
		}
	}
}

// browserUsed notes that the AI is using its browser.
func (a *App) browserUsed(l *browserLog) {
	a.browser.mu.Lock()
	l.last = time.Now()
	a.browser.mu.Unlock()
}

// browserFrontHTTP is the gate asking for the window to be on screen before
// the AI takes a screenshot: a minimised page does not draw, so there would be
// nothing to photograph. It comes back without taking the keyboard.
func (a *App) browserFrontHTTP(w http.ResponseWriter, r *http.Request) {
	l := a.browserLogFor(r.URL.Query().Get("key"))
	if l == nil {
		http.NotFound(w, r)
		return
	}
	l.viewMu.Lock()
	v := l.view
	l.viewMu.Unlock()
	if v != nil {
		restored := make(chan bool, 1)
		uiPost(func() {
			iconic, _, _ := procIsIconic.Call(v.hwnd)
			if iconic != 0 {
				const swShowNoActivate = 4
				procShowWindow.Call(v.hwnd, swShowNoActivate)
			}
			restored <- iconic != 0
		})
		if <-restored {
			time.Sleep(500 * time.Millisecond) // the page starts drawing again
		}
	}
	fmt.Fprint(w, "ok")
}

// browserOpenHTTP is the gate asking for the surface before the AI's first
// browser call; it answers once the surface is up.
func (a *App) browserOpenHTTP(w http.ResponseWriter, r *http.Request) {
	l := a.browserLogFor(r.URL.Query().Get("key"))
	if l == nil {
		http.NotFound(w, r)
		return
	}
	if err := a.browserEnsureView(l); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, "ok")
}

// browserHost is the part of an address that names the site.
func browserHost(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "www.")
}

// browserAskHTTP is the gate asking whether the AI may open a site. It waits
// for the user's answer on a permission card in the chat.
func (a *App) browserAskHTTP(w http.ResponseWriter, r *http.Request) {
	key, host := r.URL.Query().Get("key"), browserHost(r.URL.Query().Get("host"))
	l := a.browserLogFor(key)
	if l == nil || host == "" {
		http.NotFound(w, r)
		return
	}
	ok := a.browserAllow(r.Context(), l, key, host)
	if ok {
		fmt.Fprint(w, "allow")
	} else {
		fmt.Fprint(w, "deny")
	}
}

func (a *App) browserAllow(ctx context.Context, l *browserLog, key, host string) bool {
	h := &a.browser
	h.mu.Lock()
	if l.allowed[host] || slices.Contains(a.store.Config().BrowserSites, host) {
		h.mu.Unlock()
		return true
	}
	h.nextAsk++
	req := askPrefix + strconv.Itoa(h.nextAsk)
	pending := &browserAsk{n: h.nextAsk, key: key, host: host, answer: make(chan string, 1)}
	if h.asks == nil {
		h.asks = map[string]*browserAsk{}
	}
	h.asks[req] = pending
	h.mu.Unlock()

	t := l.tab
	a.chatOut(t, []Ev{{"k": "ask", "req": req, "tool": "WebFetch", "desc": "open " + host, "input": map[string]any{"url": "https://" + host}, "always": true}})
	a.notifyTab(t, " needs your permission", "", "open "+host, "")
	a.browserPush(l)
	var d string
	select {
	case d = <-pending.answer:
	case <-ctx.Done():
	case <-time.After(10 * time.Minute):
	}
	h.mu.Lock()
	delete(h.asks, req)
	h.mu.Unlock()
	a.chatOut(t, []Ev{{"k": "askclose", "req": req, "text": map[bool]string{true: "✓ Allowed", false: "✕ Denied"}[d == "allow" || d == "always"]}})
	a.browserPush(l)
	switch d {
	case "always":
		a.store.mu.Lock()
		c := a.store.Config()
		if !slices.Contains(c.BrowserSites, host) {
			c.BrowserSites = append(c.BrowserSites, host)
			a.store.saveConfig(c)
		}
		a.store.mu.Unlock()
	case "allow":
	default:
		a.browserLogStep(l, "error", "Not allowed to open "+host)
		return false
	}
	h.mu.Lock()
	l.allowed[host] = true
	h.mu.Unlock()
	return true
}

// browserHelpHTTP is the gate handing the user a step the AI cannot do. It
// waits until the user has done it (or says they cannot) — in the browser
// window or on the card in the chat.
func (a *App) browserHelpHTTP(w http.ResponseWriter, r *http.Request) {
	key, message := r.URL.Query().Get("key"), strings.TrimSpace(r.URL.Query().Get("message"))
	l := a.browserLogFor(key)
	if l == nil || message == "" {
		http.NotFound(w, r)
		return
	}
	h := &a.browser
	h.mu.Lock()
	h.nextAsk++
	req := askPrefix + "help-" + strconv.Itoa(h.nextAsk)
	pending := &browserAsk{n: h.nextAsk, key: key, help: true, message: message, answer: make(chan string, 1)}
	if h.asks == nil {
		h.asks = map[string]*browserAsk{}
	}
	h.asks[req] = pending
	h.mu.Unlock()
	t := l.tab
	a.browserLogStep(l, "help", "Waiting for you: "+message)
	a.chatOut(t, []Ev{{"k": "ask", "req": req, "tool": "BrowserHelp", "desc": message, "input": map[string]any{}, "labels": map[string]any{"allow": "I'm done", "deny": "Can't do it"}}})
	a.notifyTab(t, " needs your help", "", message, "")
	a.browserAttention(l)
	var d string
	select {
	case d = <-pending.answer:
	case <-r.Context().Done():
	case <-time.After(15 * time.Minute):
	}
	h.mu.Lock()
	delete(h.asks, req)
	h.mu.Unlock()
	a.chatOut(t, []Ev{{"k": "askclose", "req": req, "text": map[bool]string{true: "✓ Done in the browser", false: "✕ Skipped"}[d == "allow"]}})
	a.browserPush(l)
	if d == "allow" {
		a.browserLogStep(l, "do", "You finished that step")
		fmt.Fprint(w, "allow")
		return
	}
	a.browserLogStep(l, "error", "That step was skipped")
	fmt.Fprint(w, "deny")
}

// browserAnswer is the user's answer to a question, from the chat's card or
// the browser window.
func (a *App) browserAnswer(req, decision string) error {
	a.browser.mu.Lock()
	p := a.browser.asks[req]
	a.browser.mu.Unlock()
	if p == nil {
		return fmt.Errorf("that question is no longer open")
	}
	select {
	case p.answer <- decision:
	default:
	}
	return nil
}

func (a *App) browserLogStep(l *browserLog, kind, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.browser.mu.Lock()
	l.seq++
	l.steps = append(l.steps, BrowserStep{Seq: l.seq, Kind: kind, Text: text})
	if len(l.steps) > 300 {
		l.steps = l.steps[len(l.steps)-300:]
	}
	a.browser.mu.Unlock()
	a.browserPush(l)
}

// browserStep records the AI's browser call, with the plan it voiced just
// before it, for the browser window.
func (a *App) browserStep(t *Tab, plan, tool string, input any) {
	key, _ := t.browserKey.Load().(string)
	l := a.browserLogFor(key)
	if l == nil {
		return
	}
	a.browserUsed(l)
	if p := browserPlan(plan); p != "" {
		a.browserLogStep(l, "plan", p)
	}
	a.browserLogStep(l, "do", browserAction(strings.TrimPrefix(tool, browserPrefix), input))
}

func (a *App) browserFailed(t *Tab, text string) {
	key, _ := t.browserKey.Load().(string)
	if l := a.browserLogFor(key); l != nil {
		text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "### Error"))
		text, _, _ = strings.Cut(text, "\n")
		a.browserLogStep(l, "error", "Failed: "+shorten(strings.TrimSpace(text), 140))
	}
}

var planMarkup = regexp.MustCompile("[`*#>]+")

// browserPlan shortens what the AI said before a step to a sentence or two.
func browserPlan(text string) string {
	text = strings.Join(strings.Fields(planMarkup.ReplaceAllString(text, "")), " ")
	if text == "" || strings.Contains(text, reviewMarker) {
		return ""
	}
	if i := strings.Index(text, ". "); i > 0 && i < 160 {
		if j := strings.Index(text[i+2:], ". "); j > 0 && i+2+j < 200 {
			return text[:i+2+j+1]
		}
	}
	return shorten(text, 200)
}

// browserAction says in a few words what a browser tool call does.
func browserAction(tool string, input any) string {
	in, _ := input.(map[string]any)
	str := func(k string) string { s, _ := in[k].(string); return strings.TrimSpace(s) }
	what := func() string {
		if e := str("element"); e != "" {
			return shorten(e, 80)
		}
		return "the page"
	}
	switch tool {
	case "browser_navigate":
		return "Open " + shorten(strings.TrimPrefix(strings.TrimPrefix(str("url"), "https://"), "http://"), 90)
	case "browser_navigate_back":
		return "Go back"
	case "browser_click":
		if in["doubleClick"] == true {
			return "Double-click " + what()
		}
		return "Click " + what()
	case "browser_hover":
		return "Hover over " + what()
	case "browser_type":
		return "Type \"" + shorten(str("text"), 60) + "\" into " + what()
	case "browser_press_key":
		return "Press " + str("key")
	case "browser_fill_form":
		n, _ := in["fields"].([]any)
		return fmt.Sprintf("Fill in %d form field%s", len(n), map[bool]string{true: "", false: "s"}[len(n) == 1])
	case "browser_select_option":
		return "Choose an option in " + what()
	case "browser_snapshot":
		return "Read the page"
	case "browser_find":
		return "Look for \"" + shorten(str("text"), 60) + "\" on the page"
	case "browser_take_screenshot":
		return "Take a screenshot"
	case "browser_wait_for":
		if t := str("text"); t != "" {
			return "Wait for \"" + shorten(t, 60) + "\""
		}
		return "Wait a moment"
	case "browser_evaluate":
		return "Run a small script on the page"
	case "browser_tabs":
		switch str("action") {
		case "new":
			return "Open a new tab"
		case "close":
			return "Close a tab"
		case "select":
			return "Switch tab"
		}
		return "Look at the tabs"
	case "browser_drag":
		return "Drag " + shorten(str("startElement"), 60)
	case "browser_resize":
		return "Resize the window"
	case "browser_handle_dialog":
		return "Answer a dialog"
	case "browser_close":
		return "Close the browser"
	case "browser_ask_user":
		return "Ask you: " + shorten(str("message"), 120)
	case "browser_console_messages":
		return "Read the page's console"
	case "browser_network_requests", "browser_network_request":
		return "Look at the page's network requests"
	}
	return strings.ReplaceAll(strings.TrimPrefix(tool, "browser_"), "_", " ")
}

// BrowserForget removes a site from the always-allowed list.
func (a *App) BrowserForget(host string) error {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	c := a.store.Config()
	c.BrowserSites = slices.DeleteFunc(slices.Clone(c.BrowserSites), func(h string) bool { return h == host })
	return a.store.saveConfig(c)
}

// BrowserClearData deletes the kept browsing data (cookies, logins, history).
func (a *App) BrowserClearData() error {
	return os.RemoveAll(filepath.Join(a.store.browserDir(), "profile"))
}

// BrowserAvailable tells settings whether this PC can give the AIs a browser.
func (a *App) BrowserAvailable() bool { return a.store.browserReady() }

// shorten cuts s to at most n bytes, with an ellipsis, without splitting a character.
func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return cutText(s, n) + "…"
}
