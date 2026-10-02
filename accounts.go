package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Account is one login to one agent. Dir is the agent's home for it; empty
// means the agent's normal login (~/.claude, ~/.codex), which AIT reads
// but never rewrites.
type Account struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider,omitempty"` // "" = claude
	Dir      string `json:"dir,omitempty"`
}

func (a Account) provider() Provider { return provider(a.Provider) }

// Config is config.json — the user's file. AIT writes it only to create it
// and to add an account.
type Config struct {
	Accounts       []Account           `json:"accounts"`
	Args           map[string][]string `json:"args"` // extra arguments per provider
	DefaultProfile string              `json:"defaultProfile"`
	StartingDir    string              `json:"startingDirectory"`
	FontFamily     string              `json:"fontFamily"`
	FontSize       int                 `json:"fontSize"`
	FontWeight     int                 `json:"fontWeight"`
	Theme          string              `json:"theme"`       // "campbell" | "powershell"
	Renderer       string              `json:"renderer"`    // "dom" | "webgl"
	Prewarm        *bool               `json:"prewarm"`     // keep an agent started ahead of time (default on)
	ChatView       string              `json:"chatView"`    // "native" | "terminal"
	Permissions    string              `json:"permissions"` // "ask" | "edits" | "never"
	Style          string              `json:"style"`       // "terminal" | "desktop"
	AlwaysOnTop    bool                `json:"alwaysOnTop"`
	Custom         map[string]string   `json:"customTheme"` // bg, fg, accent, …
	UserName       string              `json:"userName"`    // what agents call the user (from setup)
	Models         map[string]string   `json:"models"`      // default model per provider; "" = the agent's own default
	Onboarded      bool                `json:"onboarded"`   // first-run setup done
	Access         string              `json:"access"`      // "everywhere" (every drive) | "folder" (the working folder only)
	AutoUpdate     *bool               `json:"autoUpdate"`  // check GitHub for new versions (default on)
	MemoryDir      string              `json:"memoryDir"`   // shared memory folder; "" = automatic (memory.go)
	AutoInstall    bool                `json:"autoInstall"` // install a verified update when AIT closes
	LastVersion    string              `json:"lastVersion"` // the version that last ran (for "what's new")
	CrossAI        string              `json:"crossAI"`     // when every account is out: "switch" (default) | "ask" | "off"
	AIOrder        []string            `json:"aiOrder"`     // order to try other AIs in
}

func (c Config) aiOrder() []string {
	if len(c.AIOrder) > 0 {
		return c.AIOrder
	}
	return order // registration order: Claude, ChatGPT, Gemini
}

func (c Config) autoUpdate() bool { return c.AutoUpdate == nil || *c.AutoUpdate }

func (c Config) prewarm() bool { return c.Prewarm == nil || *c.Prewarm }

// acctState is what AIT learned at runtime. It lives in state.json so it
// never clobbers an edit the user has open in config.json.
type acctState struct {
	LimitedUntil int64  `json:"limitedUntil,omitempty"`
	Reason       string `json:"reason,omitempty"`
	SignedOut    bool   `json:"signedOut,omitempty"`
}

type Store struct {
	root string // %APPDATA%\AIT
	home string // user profile

	mu    sync.Mutex
	state map[string]*acctState
	quota map[string]Quota // latest usage reading per account (chat.go)
}

func NewStore() (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(base, "AIT")
	migrate(filepath.Join(base, "Baton"), root)
	return newStoreAt(root, home)
}

// migrate moves data from AIT's working name (Baton) once, rewriting the
// account folders recorded in config.json to the new location.
func migrate(old, root string) {
	if fileExists(root) || !fileExists(old) {
		return
	}
	if os.Rename(old, root) != nil {
		return
	}
	p := filepath.Join(root, "config.json")
	if b, err := os.ReadFile(p); err == nil {
		from, _ := json.Marshal(old + `\`)
		to, _ := json.Marshal(root + `\`)
		b = []byte(strings.ReplaceAll(string(b), strings.Trim(string(from), `"`), strings.Trim(string(to), `"`)))
		os.WriteFile(p, b, 0o644)
	}
}

func newStoreAt(root, home string) (*Store, error) {
	providers(home)
	s := &Store{root: root, home: home, state: map[string]*acctState{}}
	if err := os.MkdirAll(filepath.Join(s.root, "profiles"), 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(s.statePath()); err == nil {
		json.Unmarshal(b, &s.state)
	}
	if b, err := os.ReadFile(s.quotaPath()); err == nil {
		json.Unmarshal(b, &s.quota)
	}
	if !fileExists(s.configPath()) {
		s.firstRun()
	}
	s.addMainLogins()
	return s, nil
}

func (s *Store) configPath() string { return filepath.Join(s.root, "config.json") }
func (s *Store) statePath() string  { return filepath.Join(s.root, "state.json") }

// Config is re-read on every use, so edits apply to the next tab without a restart.
func (s *Store) Config() Config {
	c := Config{}
	if b, err := os.ReadFile(s.configPath()); err == nil {
		json.Unmarshal(b, &c)
	}
	if c.DefaultProfile == "" {
		c.DefaultProfile = "claude"
	}
	if c.StartingDir == "" {
		c.StartingDir = s.home
	}
	if c.FontFamily == "" {
		c.FontFamily = "Cascadia Mono"
	}
	if c.FontSize <= 0 {
		c.FontSize = 13
	}
	if c.FontWeight <= 0 {
		c.FontWeight = 500 // Chromium draws greyscale only; 400 reads spindly
	}
	if c.CrossAI == "" {
		c.CrossAI = "switch"
	}
	if c.Access == "" {
		c.Access = "everywhere"
	}
	if c.ChatView == "" {
		c.ChatView = "native"
	}
	if c.Permissions == "" {
		c.Permissions = "ask"
	}
	if c.Style == "" {
		c.Style = "terminal"
	}
	if c.Theme == "" {
		c.Theme = "campbell"
	}
	if c.Renderer == "" {
		c.Renderer = "dom"
	}
	var known []Account
	for _, a := range c.Accounts {
		if a.provider() != nil {
			known = append(known, a)
		}
	}
	c.Accounts = known
	return c
}

func (s *Store) saveConfig(c Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(s.configPath(), b, 0o644)
}

// Home is the directory the agent uses for this account.
func (s *Store) Home(a Account) string {
	if a.Dir == "" {
		return a.provider().DefaultHome()
	}
	return a.Dir
}

func (s *Store) Account(id string) (Account, bool) {
	for _, a := range s.Config().Accounts {
		if a.ID == id {
			return a, true
		}
	}
	return Account{}, false
}

func (s *Store) Email(a Account) string  { return a.provider().Email(s.Home(a)) }
func (s *Store) signedIn(a Account) bool { return a.provider().SignedIn(s.Home(a)) }
func (s *Store) samePID(a Account, p string) bool {
	return a.provider().ID() == provider(p).ID()
}

func (s *Store) State(id string) acctState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.state[id]; st != nil {
		return *st
	}
	return acctState{}
}

func (s *Store) setState(id string, st acctState) {
	s.mu.Lock()
	s.state[id] = &st
	b, _ := json.MarshalIndent(s.state, "", "  ")
	s.mu.Unlock()
	os.WriteFile(s.statePath(), b, 0o644)
}

func (s *Store) MarkLimited(id string, until time.Time, reason string) {
	s.setState(id, acctState{LimitedUntil: until.Unix(), Reason: reason})
}

func (s *Store) MarkSignedOut(id string) {
	s.setState(id, acctState{SignedOut: true, Reason: "signed out"})
}

// ClearSignedOut runs whenever an account is opened by hand — the user may
// have just signed in again.
func (s *Store) ClearSignedOut(id string) {
	if st := s.State(id); st.SignedOut {
		s.setState(id, acctState{})
	}
}

// Available reports whether an account can take a turn right now.
func (s *Store) Available(a Account, now time.Time) bool {
	st := s.State(a.ID)
	return s.signedIn(a) && !st.SignedOut && st.LimitedUntil <= now.Unix()
}

// Pick returns the first available account of provider p after `after` in
// config order, wrapping around; `after` itself is tried last.
func (s *Store) Pick(p, after string, now time.Time) (Account, bool) {
	var accts []Account
	for _, a := range s.Config().Accounts {
		if s.samePID(a, p) {
			accts = append(accts, a)
		}
	}
	start := 0
	for i, a := range accts {
		if a.ID == after {
			start = i + 1
		}
	}
	for i := range accts {
		a := accts[(start+i)%len(accts)]
		if s.Available(a, now) {
			return a, true
		}
	}
	return Account{}, false
}

// EarliestReset is when the first limited account of provider p frees up.
func (s *Store) EarliestReset(p string) (Account, time.Time, bool) {
	var best Account
	var at time.Time
	for _, a := range s.Config().Accounts {
		st := s.State(a.ID)
		if !s.samePID(a, p) || st.SignedOut || st.LimitedUntil == 0 || !s.signedIn(a) {
			continue
		}
		if t := time.Unix(st.LimitedUntil, 0); at.IsZero() || t.Before(at) {
			best, at = a, t
		}
	}
	return best, at, !at.IsZero()
}

// Accounts of provider p, in config order.
func (s *Store) AccountsOf(p string) []Account {
	var out []Account
	for _, a := range s.Config().Accounts {
		if s.samePID(a, p) {
			out = append(out, a)
		}
	}
	return out
}

// NewAccount creates an empty home; the agent runs its own sign-in on first launch.
func (s *Store) NewAccount(p string) (Account, error) {
	prov := provider(p)
	if prov == nil {
		return Account{}, fmt.Errorf("unknown provider %q", p)
	}
	c := s.Config()
	n := 1
	id := fmt.Sprintf("%s-%02d", prov.ID(), n)
	for s.hasID(c, id) {
		n++
		id = fmt.Sprintf("%s-%02d", prov.ID(), n)
	}
	a := Account{
		ID:       id,
		Label:    fmt.Sprintf("%s %d", prov.Name(), len(s.AccountsOf(p))+1),
		Provider: prov.ID(),
		Dir:      filepath.Join(s.root, "profiles", id),
	}
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		return a, err
	}
	c.Accounts = append(c.Accounts, a)
	return a, s.saveConfig(c)
}

// RemoveUnused drops an account that was never signed in, with its folder.
func (s *Store) RemoveUnused(id string) error {
	c := s.Config()
	for i, a := range c.Accounts {
		if a.ID != id {
			continue
		}
		if a.Dir == "" || s.signedIn(a) {
			return fmt.Errorf("%s is signed in; not removing it", a.Label)
		}
		c.Accounts = append(c.Accounts[:i], c.Accounts[i+1:]...)
		if err := s.saveConfig(c); err != nil {
			return err
		}
		return os.RemoveAll(a.Dir)
	}
	return nil
}

func (s *Store) hasID(c Config, id string) bool {
	for _, a := range c.Accounts {
		if a.ID == id {
			return true
		}
	}
	return false
}

// addMainLogins makes sure every agent the user is signed in to normally
// has an account entry — so installing a new agent needs no setup at all.
func (s *Store) addMainLogins() {
	c := s.Config()
	changed := false
	for _, id := range order {
		p := registry[id]
		has := false
		for _, a := range c.Accounts {
			if s.samePID(a, id) && a.Dir == "" {
				has = true
			}
		}
		if has || !p.SignedIn(p.DefaultHome()) {
			continue
		}
		acctID := "main"
		if id != "claude" {
			acctID = id + "-main"
		}
		c.Accounts = append(c.Accounts, Account{ID: acctID, Label: p.Name() + " 1", Provider: id})
		changed = true
	}
	if changed {
		s.saveConfig(c)
	}
}

// firstRun writes config.json with the defaults spelled out. The agents'
// normal logins are added by addMainLogins; more accounts are added from
// the account menu.
func (s *Store) firstRun() {
	s.saveConfig(Config{
		Accounts:       []Account{},
		Args:           map[string][]string{"claude": {}, "codex": {}},
		DefaultProfile: "claude",
		StartingDir:    s.home,
		FontFamily:     "Cascadia Mono",
		FontSize:       13,
	})
}

// ---- file helpers -------------------------------------------------------------

func junction(target, link string) {
	if !fileExists(target) || fileExists(link) {
		return
	}
	cmd := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target)
	hideConsole(cmd)
	cmd.Run()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(to), 0o755)
	return os.WriteFile(to, b, 0o644)
}

func copyDir(from, to string) error {
	return filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		return copyFile(p, dst)
	})
}
