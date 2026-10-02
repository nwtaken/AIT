package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Tab is one terminal. An agent tab outlives its process: when the account
// runs out, the process is replaced and the conversation carries on.
type Tab struct {
	id      int
	profile string // provider id ("claude", "codex") or shell ("powershell", "cmd")
	agent   Provider
	cwd     string

	// gen counts processes. Output, exit and watcher events from an older
	// generation are dropped, so a replaced agent cannot speak over the new one.
	gen atomic.Int64

	mu         sync.Mutex
	pty        *PTY
	cols, rows int
	closed     bool
	acct       string    // the account in use
	session    string    // transcript path being watched
	launchedAt time.Time //
	trusted    bool      // the user already trusted cwd in this tab
	model      string    // model chosen in this tab ("" = default); survives handoffs
	effort     string    // thinking effort chosen in this tab ("" = default); survives handoffs
	handover   string    // first message for an AI taking over from another (crossai.go)
	reading    bool      // cross-AI preparation turn; interruption is disabled
	reviewing  bool      // a second AI is checking the current work
	wait       *time.Timer

	// adopted is false while the tab is a prewarmed standby with no id yet;
	// its output then goes to backlog instead of the page.
	adopted atomic.Bool
	backMu  sync.Mutex
	backlog []byte
	// Native chat (chat.go): the agent runs in streaming mode, no PTY.
	native    bool
	chat      *chatProc
	chatState *ChatState
	evBacklog []Ev // a standby's events, handed over on adoption

	// rememberedTrust: this tab's folder is already saved as trusted.
	rememberedTrust bool
	// autoTrust: answer the agent's folder-trust prompt without asking.
	// promptShown: the page has been asked to show the trust card.
	autoTrust, promptShown atomic.Bool
	// screen is the recent output while a set-up prompt may be up; total
	// counts every byte ever added, so a reader can ask "what is new since".
	scrMu  sync.Mutex
	screen []byte
	total  int

	// Keystrokes arrive numbered and possibly out of order — Wails runs every
	// call from the page on its own goroutine — and are written in order.
	inMu    sync.Mutex
	nextIn  int
	pending map[int]string
}

type App struct {
	ctx       context.Context
	store     *Store
	emit      func(event string, data ...any)
	allowQuit atomic.Bool

	mu      sync.Mutex
	tabs    map[int]*Tab
	claims  map[string]int // transcript path -> tab id that owns it
	standby *Tab           // prewarmed agent waiting for a tab (standby.go)

	checkNow              chan struct{} // UpdateNudge wakes the update checker (update.go)
	lastCheck             atomic.Int64
	updMu                 sync.Mutex
	prepared, preparedVer string // a verified installer waiting to run (update.go)
	cols, rows            int    // last terminal size the page reported
}

func NewApp(store *Store) *App {
	a := &App{store: store, tabs: map[int]*Tab{}, claims: map[string]int{}, checkNow: make(chan struct{}, 1)}
	a.emit = func(event string, data ...any) { runtime.EventsEmit(a.ctx, event, data...) }
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if cl, ok := registry["claude"].(*claude); ok {
		cl.loadCatalog(a.store.root) // model versions for the switcher, in the background
	}
	if a.store.Config().AlwaysOnTop {
		runtime.WindowSetAlwaysOnTop(ctx, true)
	}
	// The first agent starts now, while the window is still loading.
	go a.prepareStandby()
	a.startUpdateChecks()
	// Warm the history cache once the agent has started; doing it at once
	// would compete with the agent's own start-up on a small CPU.
	time.AfterFunc(20*time.Second, func() { a.History() })
}

// beforeClose turns every way of closing the window (the X, Alt+F4, the
// taskbar) into a question the front end asks first.
func (a *App) beforeClose(ctx context.Context) bool {
	if a.allowQuit.Load() {
		return false
	}
	a.emit("app:close-requested")
	return true
}

func (a *App) shutdown(ctx context.Context) {
	a.killStandby()
	a.installOnExit()
	// Lock order is always t.mu then a.mu, so collect first.
	a.mu.Lock()
	var tabs []*Tab
	for _, t := range a.tabs {
		tabs = append(tabs, t)
	}
	a.mu.Unlock()
	for _, t := range tabs {
		t.mu.Lock()
		t.closed = true
		if t.pty != nil {
			t.pty.Close()
		}
		t.stopChat()
		t.mu.Unlock()
	}
}

// ---- bindings --------------------------------------------------------------

type UIConfig struct {
	FontFamily     string    `json:"fontFamily"`
	FontSize       int       `json:"fontSize"`
	FontWeight     int       `json:"fontWeight"`
	DefaultProfile string    `json:"defaultProfile"`
	Profiles       []Profile `json:"profiles"`
	Theme          string    `json:"theme"`
	Renderer       string    `json:"renderer"`
	Settings       Settings  `json:"settings"`
}

// Profile is one entry in the new-tab menu.
type Profile struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Agent     bool    `json:"agent"`
	Chat      bool    `json:"chat"` // can run as a native chat
	Models    []Model `json:"models"`
	Installed bool    `json:"installed"`
}

func (a *App) Init() UIConfig {
	c := a.store.Config()
	return UIConfig{FontFamily: c.FontFamily, FontSize: c.FontSize, FontWeight: c.FontWeight, DefaultProfile: c.DefaultProfile, Profiles: a.profiles(), Theme: c.Theme, Renderer: c.Renderer, Settings: a.GetSettings()}
}

// SetTheme remembers the colour theme chosen from the menu.
func (a *App) SetTheme(name string) {
	c := a.store.Config()
	c.Theme = name
	a.store.saveConfig(c)
}

// profiles: every installed agent, then the shells.
func (a *App) profiles() []Profile {
	var out []Profile
	for _, id := range order {
		p := registry[id]
		out = append(out, Profile{ID: id, Name: p.Name(), Agent: true, Chat: chatOf(p) != nil, Models: p.Models(), Installed: p.Command() != nil})
	}
	return append(out, Profile{ID: "powershell", Name: "Windows PowerShell", Installed: true}, Profile{ID: "cmd", Name: "Command Prompt", Installed: true})
}

// Quit closes the app for real, after the front end has confirmed.
func (a *App) Quit() {
	a.allowQuit.Store(true)
	runtime.Quit(a.ctx)
}

type OpenRequest struct {
	ID      int    `json:"id"` // chosen by the front end, so its terminal exists before output does
	Profile string `json:"profile"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	Account string `json:"account"` // optional: start on this account
	Chat    string `json:"chat"`    // optional: a history ref to resume
	Model   string `json:"model"`   // optional: start on this model
}

type TabInfo struct {
	Profile string `json:"profile"`
	Agent   bool   `json:"agent"`
	Account string `json:"account"` // label, agent tabs only
	Cwd     string `json:"cwd"`
	// A prewarmed agent arrives with what it already drew, at the size it
	// drew it; the page writes that, then resizes to fit.
	Backlog []byte `json:"backlog"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	// Native chat tabs: no terminal; events so far (from a standby).
	Native  bool   `json:"native"`
	Events  []Ev   `json:"events"`
	Folder  string `json:"folder"`
	Trusted bool   `json:"trusted"`
	Model   string `json:"model"`
}

func (a *App) Open(r OpenRequest) (TabInfo, error) {
	if t, back, cols, rows, evs := a.adoptStandby(r); t != nil {
		acct, _ := a.store.Account(t.acct)
		return TabInfo{Profile: r.Profile, Agent: true, Account: acct.Label, Cwd: maskUser(t.cwd), Backlog: back, Cols: cols, Rows: rows,
			Native: t.native, Events: evs, Folder: maskUser(t.cwd), Trusted: a.store.Trusted(t.cwd), Model: t.model}, nil
	}
	cfg := a.store.Config()
	t := &Tab{id: r.ID, profile: r.Profile, cwd: cfg.StartingDir, cols: r.Cols, rows: r.Rows, model: r.Model}
	t.adopted.Store(true)
	info := TabInfo{Profile: r.Profile}
	a.mu.Lock()
	a.tabs[r.ID] = t
	a.mu.Unlock()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.agent = registry[r.Profile]
	if t.agent == nil {
		argv := shellArgv(r.Profile)
		p, err := StartPTY(argv, t.cwd, baseEnv(), r.Cols, r.Rows, false)
		if err != nil {
			return info, fmt.Errorf("could not start %s: %w", argv[0], err)
		}
		t.pty = p
		go a.pump(t, p, t.gen.Load(), nil)
		return info, nil
	}
	info.Agent = true
	t.trusted = a.store.Trusted(t.cwd)

	if r.Chat != "" {
		ch, ok := a.findChat(r.Chat)
		if !ok {
			return info, fmt.Errorf("that conversation is no longer on disk")
		}
		if fi, err := os.Stat(ch.Cwd); err == nil && fi.IsDir() {
			t.cwd = ch.Cwd
		}
		t.session = ch.Path
		t.trusted = true // it already ran in that folder
	}

	var acct Account
	if r.Account != "" {
		var ok bool
		if acct, ok = a.store.Account(r.Account); !ok {
			return info, fmt.Errorf("no account %q", r.Account)
		}
		a.store.ClearSignedOut(r.Account)
	} else if next, ok := a.store.Pick(t.profile, "", time.Now()); ok {
		acct = next
	} else if accts := a.store.AccountsOf(t.profile); len(accts) > 0 {
		acct = accts[0]
		if who, at, ok := a.store.EarliestReset(t.profile); ok {
			a.notice(t, fmt.Sprintf("Every %s account is limited. %s frees up at %s.", t.agent.Name(), who.Label, clock(at)))
		}
	} else {
		acct = Account{ID: t.profile + "-main", Label: t.agent.Name(), Provider: t.profile}
	}
	if r.Model != "" {
		if err := a.store.SetAccountModel(acct.ID, r.Model); err != nil {
			return info, err
		}
	}
	if err := a.launch(t, acct, ""); err != nil {
		return info, err
	}
	info.Account = acct.Label
	info.Model = t.model
	info.Cwd = maskUser(t.cwd)
	info.Native, info.Folder, info.Trusted = t.native, maskUser(t.cwd), a.store.Trusted(t.cwd)
	info.Events = []Ev{}
	return info, nil
}

func (a *App) findChat(ref string) (Chat, bool) {
	for _, acct := range a.store.Config().Accounts {
		for _, ch := range acct.provider().Chats(a.store.Home(acct)) {
			if ch.Path == ref {
				return ch, true
			}
		}
	}
	return Chat{}, false
}

// Input is fire-and-forget from the page: it never waits for one keystroke
// to land before sending the next, so typing has no round trip in it. seq
// starts at 1 per tab.
func (a *App) Input(id, seq int, data string) {
	t := a.tab(id)
	if t == nil {
		return
	}
	t.inMu.Lock()
	defer t.inMu.Unlock()
	if t.pending == nil {
		t.pending, t.nextIn = map[int]string{}, 1
	}
	if seq < t.nextIn {
		return
	}
	t.pending[seq] = data
	for {
		d, ok := t.pending[t.nextIn]
		if !ok {
			return
		}
		delete(t.pending, t.nextIn)
		t.nextIn++
		t.mu.Lock()
		p := t.pty
		t.mu.Unlock()
		if p != nil {
			p.Write([]byte(d))
		}
	}
}

func (a *App) Resize(id, cols, rows int) {
	a.mu.Lock()
	a.cols, a.rows = cols, rows
	a.mu.Unlock()
	if t := a.tab(id); t != nil {
		t.mu.Lock()
		t.cols, t.rows = cols, rows
		if t.pty != nil {
			t.pty.Resize(cols, rows)
		}
		t.mu.Unlock()
	}
}

func (a *App) Close(id int) {
	t := a.tab(id)
	if t == nil {
		return
	}
	a.mu.Lock()
	delete(a.tabs, id)
	for p, owner := range a.claims {
		if owner == id {
			delete(a.claims, p)
		}
	}
	a.mu.Unlock()

	t.mu.Lock()
	t.closed = true
	t.gen.Add(1)
	if t.wait != nil {
		t.wait.Stop()
	}
	p := t.pty
	t.pty = nil
	t.stopChat()
	t.mu.Unlock()
	if p != nil {
		go p.Close() // can take a moment on Windows 10; never block the UI on it
	}
}

// stopChat ends a streaming agent. Caller holds t.mu.
func (t *Tab) stopChat() {
	if t.chat != nil {
		t.chat.stdin.Close()
		t.chat.kill()
		t.chat = nil
	}
}

type AccountView struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider"`
	Email    string `json:"email"`
	Status   string `json:"status"` // ready | limited | signed out
	Detail   string `json:"detail"`
	Current  bool   `json:"current"`
	// Last usage reading (from the agent's own reports), when there is one.
	Five      float64 `json:"five"`
	Week      float64 `json:"week"`
	FiveReset int64   `json:"fiveReset"`
	WeekReset int64   `json:"weekReset"`
	HasQuota  bool    `json:"hasQuota"`
}

// Accounts lists the accounts tabID can move between, in the order AIT tries
// them when one runs out: this AI's accounts first (they resume the exact
// conversation), then, for chat tabs, the other installed AIs' in the
// user's AI order (they continue from a handover). The one in use is marked.
func (a *App) Accounts(tabID int) []AccountView {
	t := a.tab(tabID)
	if t == nil || t.agent == nil {
		return []AccountView{}
	}
	t.mu.Lock()
	cur, from, native := t.acct, t.profile, t.native
	t.mu.Unlock()
	ids := []string{from}
	if native {
		ord := a.store.Config().aiOrder()
		start := 0
		for i, id := range ord {
			if id == from {
				start = i + 1
			}
		}
		for i := range ord {
			id := ord[(start+i)%len(ord)]
			if p := registry[id]; id != from && p != nil && p.Command() != nil && chatOf(p) != nil {
				ids = append(ids, id)
			}
		}
	}
	now := time.Now()
	out := []AccountView{}
	for _, id := range ids {
		out = append(out, a.accountViews(id, cur, now)...)
	}
	return out
}

func (a *App) accountViews(provider, cur string, now time.Time) []AccountView {
	var out []AccountView
	for _, acct := range a.store.AccountsOf(provider) {
		v := AccountView{ID: acct.ID, Label: acct.Label, Provider: acct.provider().ID(), Email: maskEmail(a.store.Email(acct)), Status: "ready", Current: acct.ID == cur}
		st := a.store.State(acct.ID)
		switch {
		case !a.store.signedIn(acct) || st.SignedOut:
			v.Status, v.Detail = "signed out", "sign in"
		case st.LimitedUntil > now.Unix():
			v.Status, v.Detail = "limited", "resets "+clock(time.Unix(st.LimitedUntil, 0))
		}
		if q, ok := a.store.Quota(acct.ID); ok {
			v.Five, v.Week, v.FiveReset, v.WeekReset, v.HasQuota = q.Five, q.Week, q.FiveReset, q.WeekReset, true
		}
		out = append(out, v)
	}
	return out
}

// Switch moves an agent tab to another account by hand, keeping the
// conversation; an account of another AI takes the chat over.
func (a *App) Switch(tabID int, acctID string) error {
	t := a.tab(tabID)
	if t == nil || t.agent == nil {
		return fmt.Errorf("not an agent tab")
	}
	acct, ok := a.store.Account(acctID)
	if !ok {
		return fmt.Errorf("no account %q", acctID)
	}
	a.store.ClearSignedOut(acctID)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	if t.reading {
		return fmt.Errorf("wait for the conversation handover to finish")
	}
	if to := acct.provider(); to.ID() != t.profile {
		if !t.native || chatOf(to) == nil {
			return fmt.Errorf("this tab can't move to %s", to.Name())
		}
		return a.crossOver(t, to, acct, "was switched out by the user")
	}
	return a.relaunch(t, acct, "")
}

// AddAccount creates an empty account for an AI; the page then signs it in.
func (a *App) AddAccount(profile string) (AccountView, error) {
	acct, err := a.store.NewAccount(profile)
	return AccountView{ID: acct.ID, Label: acct.Label, Provider: acct.Provider}, err
}

func (a *App) OpenSettings() {
	exec.Command("notepad.exe", a.store.configPath()).Start()
}

// ---- agents ----------------------------------------------------------------

// launch starts the agent on acct. When the tab already has a transcript it
// is carried into acct's home and resumed; prompt, when set, is sent as the
// first message. Caller holds t.mu.
func (a *App) launch(t *Tab, acct Account, prompt string) error {
	p := t.agent
	home := a.store.Home(acct)
	p.Provision(home, t.cwd, t.trusted)
	p.ShareMemory(home, t.cwd, a.store.ensureMemory())

	l := Launch{Extra: a.store.Config().Args[p.ID()]}
	m := t.model
	if acct.Model != nil {
		m = *acct.Model
	} else if m == "" || (t.acct != "" && t.acct != acct.ID) {
		m = a.store.Config().Models[p.ID()]
	}
	t.model = m
	if m != "" && !hasFlag(l.Extra, "--model", "-m") {
		l.Extra = append(l.Extra, p.ModelArgs(m)...)
	}
	if ef, ok := p.(efforter); ok && t.effort != "" {
		l.Extra = append(l.Extra, ef.EffortArgs(t.effort)...)
	}
	if a.store.Config().Access == "everywhere" {
		l.Extra = append(l.Extra, p.AccessArgs(fixedDrives())...)
	}
	if file, text := a.store.RulesFor(); text != "" {
		l.Extra = append(p.RulesArgs(file, text), l.Extra...)
	}
	if t.session != "" && fileExists(t.session) {
		// Sessions live inside each account's home, and an agent cannot
		// resume one it does not have — so the transcript goes first.
		if dst, err := p.Carry(t.session, home, t.cwd); err == nil {
			l.ResumeID = p.SessionID(dst)
			a.claim(t, dst)
		}
	}
	if l.ResumeID == "" {
		prompt = "" // nothing to continue
		if p.PresetsID() {
			l.NewID = newUUID()
		}
	}
	l.Prompt = prompt

	cmd := p.Command()
	if cmd == nil {
		return fmt.Errorf("%s is not installed", p.Name())
	}
	env := baseEnv()
	if acct.Dir != "" {
		env = append(env, p.HomeEnv()+"="+acct.Dir)
	}

	cfg := a.store.Config()
	if cp := chatOf(p); cp != nil && cfg.ChatView != "terminal" {
		t.native = true
		argv := append(append([]string{}, cmd...), cp.ChatArgs(l, cfg.Permissions)...)
		if err := a.startChat(t, cp, argv, env, l, cfg.Permissions); err != nil {
			return fmt.Errorf("could not start %s: %w", p.Name(), err)
		}
		t.acct = acct.ID
		t.launchedAt = time.Now()
		if l.NewID != "" {
			a.claim(t, p.SessionFile(home, t.cwd, l.NewID))
		}
		if prompt != "" && t.handover == "" {
			t.chat.send(cp.ChatUser(t.chatState, prompt, nil))
		}
		if t.handover != "" {
			t.reading = true
			a.chatOut(t, []Ev{{"k": "handover", "state": "reading"}})
			if err := t.chat.send(cp.ChatUser(t.chatState, t.handover, nil)); err != nil {
				t.reading, t.handover = false, ""
				a.chatOut(t, []Ev{{"k": "handover", "state": "failed"}})
				return err
			}
		}
		go a.watch(t, t.gen.Load())
		return nil
	}
	t.native = false
	argv := append(append([]string{}, cmd...), p.Args(l)...)

	pty, err := StartPTY(argv, t.cwd, env, t.cols, t.rows, true)
	if err != nil {
		return fmt.Errorf("could not start %s: %w", p.Name(), err)
	}
	t.pty = pty
	t.acct = acct.ID
	t.launchedAt = time.Now()
	if l.NewID != "" {
		a.claim(t, p.SessionFile(home, t.cwd, l.NewID))
	}
	gen := t.gen.Load()
	t.autoTrust.Store(t.trusted)
	t.promptShown.Store(false)
	go a.pump(t, pty, gen, p)
	go a.watch(t, gen)
	return nil
}

// relaunch replaces the agent process with one on acct, carrying the
// conversation across. Caller holds t.mu.
func (a *App) relaunch(t *Tab, to Account, prompt string) error {
	if t.wait != nil {
		t.wait.Stop()
		t.wait = nil
	}
	t.gen.Add(1)
	if t.pty != nil {
		t.pty.Close()
		t.pty = nil
	}
	t.stopChat()
	t.trusted = true // it was running in this folder a moment ago
	a.emit("tab:reset", t.id)
	if err := a.launch(t, to, prompt); err != nil {
		a.emit("pty:out", t.id, base64.StdEncoding.EncodeToString([]byte("\r\n\x1b[31m"+err.Error()+"\x1b[0m\r\n")))
		return err
	}
	a.emit("tab:account", t.id, to.Label, t.model)
	return nil
}

// watch follows the tab's transcript and hands off when the agent records a
// usage-limit or signed-out error. One watcher per process generation.
func (a *App) watch(t *Tab, gen int64) {
	offsets := map[string]int64{}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		t.mu.Lock()
		if t.closed || t.gen.Load() != gen {
			t.mu.Unlock()
			return
		}
		acct, _ := a.store.Account(t.acct)
		home := a.store.Home(acct)
		since, cur := t.launchedAt, t.session
		t.mu.Unlock()

		// Follow a new transcript: the first one for agents that pick their
		// own id, or a fresh one after /clear.
		after := since
		if info, err := os.Stat(cur); err == nil && fileCreated(info).After(after) {
			after = fileCreated(info)
		}
		if newer := t.agent.NewestSession(home, t.cwd, after, a.claimedByOthers(t.id)); newer != "" && newer != cur {
			t.mu.Lock()
			if t.gen.Load() == gen {
				a.claim(t, newer)
			}
			t.mu.Unlock()
			cur = newer
		}
		if cur == "" {
			continue
		}

		if !t.rememberedTrust && cur != "" && fileExists(cur) {
			t.rememberedTrust = true
			a.store.Trust(t.cwd)
		}

		hit, off := t.agent.Scan(cur, offsets[cur], since)
		offsets[cur] = off
		if hit != nil {
			a.handoff(t, gen, hit)
			return
		}
	}
}

func (a *App) handoff(t *Tab, gen int64, hit *limitHit) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.gen.Load() != gen {
		return
	}
	from, _ := a.store.Account(t.acct)
	why := "hit its usage limit"
	if hit.SignedOut {
		a.store.MarkSignedOut(from.ID)
		why = "is signed out"
	} else {
		a.store.MarkLimited(from.ID, hit.Until, hit.Text)
	}
	a.emit("tab:account", t.id, from.Label) // refresh the pill's state

	if next, ok := a.store.Pick(t.profile, from.ID, time.Now()); ok && next.ID != from.ID {
		if a.relaunch(t, next, "continue") == nil {
			a.notice(t, fmt.Sprintf("%s %s — continued on %s.", from.Label, why, next.Label))
		}
		return
	}

	// Every account of this AI is out: another AI may carry on.
	if mode := a.store.Config().CrossAI; t.native && mode != "off" {
		if to, acct, ok := a.nextAI(t.profile); ok {
			if mode == "ask" {
				a.emit("tab:crossask", t.id, to.ID(), to.Name(), t.agent.Name())
			} else if name := t.agent.Name(); a.crossOver(t, to, acct, "ran out of usage on every account") == nil {
				a.notice(t, fmt.Sprintf("Every %s account is at its limit — continued on %s.", name, to.Name()))
				return
			}
		}
	}

	who, at, ok := a.store.EarliestReset(t.profile)
	if !ok {
		a.notice(t, fmt.Sprintf("%s %s and no other %s account is available. Add one from the account menu.", from.Label, why, t.agent.Name()))
		return
	}
	a.notice(t, fmt.Sprintf("Every %s account is limited. Continuing on %s at %s.", t.agent.Name(), who.Label, clock(at)))
	t.wait = time.AfterFunc(time.Until(at), func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.closed || t.gen.Load() != gen {
			return
		}
		if acct, ok := a.store.Account(who.ID); ok && a.relaunch(t, acct, "continue") == nil {
			a.notice(t, fmt.Sprintf("%s reset — continued.", acct.Label))
		}
	})
}

// ---- plumbing --------------------------------------------------------------

var ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-_]`)

// pump streams one process's output to the front end, then reports its exit.
// For agents it also watches the first minute for the folder-trust prompt:
// in a trusted folder it answers it, otherwise it asks the page to show its
// own trust card (AnswerTrust) in place of the agent's text screen.
func (a *App) pump(t *Tab, p *PTY, gen int64, ans Provider) {
	var fresh []byte
	answerUntil := time.Now().Add(60 * time.Second)

	// Reads are coalesced for a few milliseconds: a full-screen redraw
	// arrives as dozens of small writes, and one message to the page per
	// frame is far cheaper than one per write.
	chunks := make(chan []byte, 256)
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		var batch []byte
		for c := range chunks {
			batch = append(batch[:0], c...)
			timer := time.NewTimer(4 * time.Millisecond)
		gather:
			for len(batch) < 256<<10 {
				select {
				case more, ok := <-chunks:
					if !ok {
						break gather
					}
					batch = append(batch, more...)
				case <-timer.C:
					break gather
				}
			}
			timer.Stop()
			if t.gen.Load() == gen {
				a.out(t, batch)
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(chunks)
		buf := make([]byte, 64*1024)
		for {
			n, err := p.Read(buf)
			if n > 0 && t.gen.Load() == gen {
				chunks <- append([]byte(nil), buf[:n]...)
				if ans != nil {
					screen := t.addScreen(buf[:n])
					fresh = append(fresh, buf[:n]...)
					key, confirms := ans.TrustAnswer(plain(screen), plain(fresh))
					switch {
					case key != "" && t.autoTrust.Load():
						if t.adopted.Load() {
							a.emit("tab:autotrust", t.id) // the page covers the prompt while AIT answers it
						}
						time.Sleep(trustSettle) // only while the prompt is up
						p.Write([]byte(key))
						fresh = fresh[:0] // judge the next key from the redraw only
						if confirms {
							ans = nil
						}
					case key != "" && !t.promptShown.Swap(true):
						a.prompt(t)
					case time.Now().After(answerUntil):
						ans = nil
					}
				}
			}
			if err != nil {
				return
			}
		}
	}()
	code, _ := p.Wait()
	p.Close()
	<-done
	<-flushed
	p.release()
	if t.gen.Load() == gen {
		if t.adopted.Load() {
			a.emit("pty:exit", t.id, code)
		} else {
			a.standbyExited(t)
		}
	}
}

// prompt asks the page to show the trust card; a standby asks on adoption.
func (a *App) prompt(t *Tab) {
	if t.adopted.Load() {
		a.emit("tab:prompt", t.id, "trust", maskUser(t.cwd))
	}
}

// AnswerTrust is the trust card's answer. Yes: AIT remembers the folder
// and steps through the agent's own prompt, one key at a time, judging each
// from the screen it has. No: the page closes the tab.
func (a *App) AnswerTrust(id int, yes bool) {
	t := a.tab(id)
	if t == nil || !yes || t.agent == nil {
		return
	}
	a.store.Trust(t.cwd)
	go func() {
		mark := 0 // everything on screen counts as fresh for the first key
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(30 * time.Millisecond) {
			all, fresh, total := t.readScreen(mark)
			key, confirms := t.agent.TrustAnswer(all, fresh)
			if key == "" {
				continue
			}
			time.Sleep(trustSettle)
			t.mu.Lock()
			p := t.pty
			t.mu.Unlock()
			if p == nil {
				return
			}
			p.Write([]byte(key))
			if confirms {
				return
			}
			mark = total
		}
	}()
}

// addScreen keeps the recent output (plain text is derived on demand).
func (t *Tab) addScreen(b []byte) []byte {
	t.scrMu.Lock()
	defer t.scrMu.Unlock()
	t.screen = append(t.screen, b...)
	t.total += len(b)
	if len(t.screen) > 32<<10 {
		t.screen = t.screen[len(t.screen)-32<<10:]
	}
	return t.screen
}

// readScreen returns all recent text, the text added since byte mark, and
// the mark to use next.
func (t *Tab) readScreen(mark int) (string, string, int) {
	t.scrMu.Lock()
	defer t.scrMu.Unlock()
	start := len(t.screen) - (t.total - mark)
	if start < 0 {
		start = 0
	}
	return plain(t.screen), plain(t.screen[start:]), t.total
}

// out sends output to the page, or keeps it while the tab is a standby.
func (a *App) out(t *Tab, b []byte) {
	if t.adopted.Load() {
		a.emit("pty:out", t.id, base64.StdEncoding.EncodeToString(b))
		return
	}
	t.backMu.Lock()
	defer t.backMu.Unlock()
	if t.adopted.Load() { // adopted while we waited for the lock
		a.emit("pty:out", t.id, base64.StdEncoding.EncodeToString(b))
		return
	}
	t.backlog = append(t.backlog, b...)
	if len(t.backlog) > 1<<20 {
		t.backlog = t.backlog[len(t.backlog)-(1<<20):]
	}
}

// trustSettle is how long Claude's menu needs after a redraw before it takes
// the next key: Enter sent the instant "Yes" was highlighted was dropped.
const trustSettle = 150 * time.Millisecond

// plain is terminal output as text: escapes removed, and the styling and
// cursor moves that sit between words collapsed to single spaces.
func plain(b []byte) string {
	return strings.Join(strings.Fields(ansiRe.ReplaceAllString(string(b), " ")), " ")
}

func (a *App) tab(id int) *Tab {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tabs[id]
}

// claim records that t is watching path. Caller holds t.mu.
func (a *App) claim(t *Tab, path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t.session != "" && a.claims[t.session] == t.id {
		delete(a.claims, t.session)
	}
	t.session = path
	a.claims[path] = t.id
}

func (a *App) claimedByOthers(id int) map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := map[string]bool{}
	for p, owner := range a.claims {
		if owner != id {
			m[p] = true
		}
	}
	return m
}

func (a *App) notice(t *Tab, text string) {
	a.emit("tab:notice", t.id, text)
}

func shellArgv(kind string) []string {
	if kind == "cmd" {
		return []string{"cmd.exe"}
	}
	if p, err := exec.LookPath("pwsh.exe"); err == nil {
		return []string{p, "-NoLogo"}
	}
	return []string{"powershell.exe", "-NoLogo"}
}

// baseEnv is the user's environment minus anything that would make an agent
// think it is nested inside another, or pin it to one account.
func baseEnv() []string {
	drop := map[string]bool{}
	for _, p := range registry {
		drop[p.HomeEnv()] = true
		for _, k := range p.NestingEnv() {
			drop[k] = true
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "" || !drop[strings.ToUpper(k)] {
			env = append(env, kv)
		}
	}
	return append(env, "COLORTERM=truecolor")
}

func hasFlag(args []string, names ...string) bool {
	for _, a := range args {
		for _, n := range names {
			if a == n || strings.HasPrefix(a, n+"=") {
				return true
			}
		}
	}
	return false
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func clock(t time.Time) string {
	t = t.Local()
	now := time.Now()
	if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
		return t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}

func maskEmail(e string) string {
	user, domain, ok := strings.Cut(e, "@")
	if !ok {
		return ""
	}
	if len(user) > 3 {
		user = user[:3]
	}
	return user + "•••@" + domain
}
