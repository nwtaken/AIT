package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// Provider is everything AIT knows about one AI command-line agent. The
// rest of AIT — accounts, tabs, the handoff, history, the UI — only ever
// talks to this interface, so adding an agent (Gemini, …) is one new file
// that implements it plus one line in providers().
//
// Every agent AIT can drive shares three properties, and the interface is
// built on exactly those:
//
//  1. Its whole login + state lives in one directory, and an environment
//     variable can point it elsewhere (CLAUDE_CONFIG_DIR, CODEX_HOME). That
//     directory is what an "account" is.
//  2. It writes each conversation to a transcript file as it goes. AIT
//     tails that file to notice a usage limit, and copies it to carry the
//     conversation to another account.
//  3. It can resume a conversation by id, optionally with a first prompt.
type Provider interface {
	ID() string   // stable key stored in config.json: "claude", "codex"
	Name() string // shown in the UI: "Claude", "ChatGPT"

	// Command is the argv prefix that starts the agent, or nil when it is
	// not installed.
	Command() []string
	// HomeEnv names the variable that points the agent at an account's dir.
	HomeEnv() string
	// DefaultHome is the dir the agent uses when HomeEnv is unset — the
	// user's normal login, which AIT never modifies.
	DefaultHome() string

	SignedIn(home string) bool
	Email(home string) string

	// Provision makes a secondary account behave like the main one (same
	// instructions, settings, tools), and — when trusted — pre-accepts the
	// folder-trust prompt for cwd, so a handoff is never stopped by a dialog.
	Provision(home, cwd string, trusted bool)

	// Args builds the command line after the binary. Exactly one of
	// l.NewID / l.ResumeID is set; NewID is only set when PresetsID().
	Args(l Launch) []string
	// Models lists the models offered in AIT's model switcher. The first is
	// always the agent's own default ("").
	Models() []Model
	// ModelArgs selects a model at launch.
	ModelArgs(id string) []string
	// AccessArgs lets the agent work in these folders as well as the one it
	// starts in (AIT passes every fixed drive when file access is
	// "everywhere"). Approval prompts still apply.
	AccessArgs(dirs []string) []string
	// ShareMemory links the agent's own memory folder for cwd (if it has
	// one, and it does not exist yet) to the shared memory folder. Agents
	// without a memory folder do nothing; their instructions cover it.
	ShareMemory(home, cwd, shared string)
	// RulesArgs passes AIT's rules (rules.md) through the agent's own flag
	// for extra instructions. file is the path, text its contents.
	RulesArgs(file, text string) []string
	// PresetsID reports whether the agent accepts a session id chosen by
	// AIT up front. When false, AIT discovers it from the new transcript.
	PresetsID() bool

	// SessionFile is where the transcript for id lives in home, or "" when
	// it is not there.
	SessionFile(home, cwd, id string) string
	// NewestSession returns the transcript created most recently after
	// `after` for a session started in cwd, skipping paths in skip.
	NewestSession(home, cwd string, after time.Time, skip map[string]bool) string
	// SessionID extracts the resumable id from a transcript path.
	SessionID(path string) string
	// Carry copies a transcript (and anything that belongs to it) into
	// another account's home and returns the new path.
	Carry(path, toHome, cwd string) (string, error)

	// Scan reads complete lines appended to a transcript since offset and
	// reports the first usage-limit or signed-out error stamped after since.
	Scan(path string, offset int64, since time.Time) (*limitHit, int64)

	// Chats lists the conversations stored in one account's home.
	Chats(home string) []Chat

	// NestingEnv lists environment variables the agent sets on its own child
	// processes. AIT may be started from inside an agent, and an agent that
	// inherits these believes it is a sub-session — Claude then stops saving
	// its transcript, which would break handoff and history.
	NestingEnv() []string
	// TrustAnswer steps through a folder-trust prompt that cannot be
	// pre-accepted in config (Claude never saves trust for the home folder).
	// all is the screen text since launch, fresh is what arrived since the
	// last key AIT sent. It returns the next key to press ("" for none yet)
	// and whether that key confirms the prompt. One key at a time, each
	// decided from the redrawn screen, because keys sent in a burst get lost.
	// Only used for folders the user has already trusted. Agents without
	// such a prompt return "", false.
	TrustAnswer(all, fresh string) (key string, confirms bool)
}

// Model is one entry in the model switcher.
type Model struct {
	ID     string `json:"id"` // "" = the agent's own default
	Name   string `json:"name"`
	Desc   string `json:"desc"`
	Family string `json:"family"` // groups versions in the switcher ("Opus")
	Long   bool   `json:"long"`   // accepts a 1M-token context ("<id>[1m]")
	// Thinking effort levels, lowest first, and the model's own default.
	// Empty when the AI reports them at runtime instead (Claude: "efforts" event).
	Efforts []string `json:"efforts,omitempty"`
	Effort  string   `json:"effort,omitempty"`
}

// efforter is implemented by AIs whose thinking effort can be chosen.
type efforter interface {
	EffortArgs(level string) []string
}

// Launch describes one start of an agent process.
type Launch struct {
	NewID    string
	ResumeID string
	Prompt   string      // sent as the first message; "" for none
	Extra    []string    // user's extra arguments from config.json
	McpOff   []string    // MCP servers the user switched off for this AI
	Mcp      []McpServer // servers AIT adds to this start (the browser, browser.go)
}

// McpServer is a stdio MCP server AIT gives an AI for one start.
type McpServer struct {
	Name, Command string
	Args          []string
}

// limitHit is a usage-limit or signed-out error found in a transcript.
type limitHit struct {
	SignedOut bool
	Text      string
	Until     time.Time
}

// Chat is one past conversation, for the history panel.
type Chat struct {
	Provider string `json:"provider"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Cwd      string `json:"cwd"`
	Updated  int64  `json:"updated"` // unix seconds
	Size     int64  `json:"size"`    // transcript bytes: how much conversation it holds
	Path     string `json:"-"`
	Home     string `json:"-"`
}

var registry = map[string]Provider{}
var order []string

func register(p Provider) {
	registry[p.ID()] = p
	order = append(order, p.ID())
}

func providers(userHome string) {
	registry, order = map[string]Provider{}, nil
	register(&claude{userHome: userHome})
	register(&codex{userHome: userHome})
	register(&gemini{userHome: userHome})
}

func provider(id string) Provider {
	if id == "" {
		id = "claude"
	}
	return registry[id]
}

// ---- helpers shared by providers ------------------------------------------

// lookBinary finds name on PATH, preferring a real .exe over an npm shim (the
// shim routes arguments through cmd.exe, which mangles quoting), then tries
// the given fallbacks.
func lookBinary(name string, fallbacks ...string) string {
	if p, err := exec.LookPath(name + ".exe"); err == nil {
		return p
	}
	for _, c := range fallbacks {
		if c != "" && fileExists(c) {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// newestCreated returns the file under dir (walking subdirectories when deep)
// with the latest creation time after `after`, matching ext.
func newestCreated(dir, ext string, deep bool, after time.Time, skip map[string]bool) string {
	best, bestT := "", after
	visit := func(p string, info os.FileInfo) {
		if info.IsDir() || filepath.Ext(p) != ext || skip[p] {
			return
		}
		if c := fileCreated(info); c.After(bestT) {
			best, bestT = p, c
		}
	}
	if deep {
		filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err == nil {
				visit(p, info)
			}
			return nil
		})
		return best
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if info, err := e.Info(); err == nil {
			visit(filepath.Join(dir, e.Name()), info)
		}
	}
	return best
}

func sortChats(cs []Chat) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Updated > cs[j].Updated })
}
