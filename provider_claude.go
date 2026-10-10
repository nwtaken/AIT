package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// claude drives Anthropic's Claude Code CLI.
type claude struct{ userHome string }

func (c *claude) ID() string          { return "claude" }
func (c *claude) Name() string        { return "Claude" }
func (c *claude) HomeEnv() string     { return "CLAUDE_CONFIG_DIR" }
func (c *claude) DefaultHome() string { return filepath.Join(c.userHome, ".claude") }
func (c *claude) PresetsID() bool     { return true }
func (c *claude) LoginArgs() []string { return []string{"auth", "login"} }

func (c *claude) Command() []string {
	npm := ""
	if d, err := os.UserConfigDir(); err == nil {
		npm = filepath.Join(d, "npm", "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe")
	}
	if p := lookBinary("claude", npm, filepath.Join(c.userHome, ".local", "bin", "claude.exe")); strings.EqualFold(filepath.Ext(p), ".exe") {
		return []string{p}
	}
	return nil
}

func (c *claude) isMain(home string) bool {
	return strings.EqualFold(filepath.Clean(home), c.DefaultHome())
}

// stateFile is claude's .claude.json: in the user profile for the main login,
// inside the config dir for any other.
func (c *claude) stateFile(home string) string {
	if c.isMain(home) {
		return filepath.Join(c.userHome, ".claude.json")
	}
	return filepath.Join(home, ".claude.json")
}

func (c *claude) SignedIn(home string) bool {
	return fileExists(filepath.Join(home, ".credentials.json"))
}

func (c *claude) Email(home string) string {
	var v struct {
		OAuth struct {
			Email string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if b, err := os.ReadFile(c.stateFile(home)); err == nil {
		json.Unmarshal(b, &v)
	}
	return v.OAuth.Email
}

// Provision: files are copied fresh on every launch; directories are
// junctions, so skills and memory stay live and shared.
func (c *claude) Provision(home, cwd string, trusted bool) {
	if c.isMain(home) {
		return
	}
	main := c.DefaultHome()
	os.MkdirAll(home, 0o755)
	for _, f := range []string{"CLAUDE.md", "settings.json"} {
		if fileExists(filepath.Join(main, f)) {
			copyFile(filepath.Join(main, f), filepath.Join(home, f))
		}
	}
	for _, d := range []string{"skills", "agents", "commands", "plugins"} {
		junction(filepath.Join(main, d), filepath.Join(home, d))
	}
	key := projectKey(cwd)
	os.MkdirAll(filepath.Join(home, "projects", key), 0o755)
	junction(filepath.Join(main, "projects", key, "memory"), filepath.Join(home, "projects", key, "memory"))
	c.mirrorState(home, cwd, trusted)
}

// ShareMemory: Claude keeps memory per working folder in
// projects/<folder>/memory. A folder that has none yet is linked to the
// shared memory, so Claude's own memory writes land there.
func (c *claude) ShareMemory(home, cwd, shared string) {
	linkMemory(filepath.Join(home, "projects", projectKey(cwd), "memory"), shared)
}

// Keys in .claude.json that belong to one login and must never be copied.
var claudePrivate = regexp.MustCompile(`(?i)oauth|userid|machineid|apikey|cache|subscription|passes|overage|extrausage|grove|s1m|projects`)

// mirrorState copies the main login's UI state (onboarding done, theme,
// dismissed notices, MCP servers…) so a secondary account opens straight to
// the prompt. Every first-run dialog it skips is one that would otherwise
// halt an automatic handoff. Writes only when something changed — claude
// rewrites this file itself while it runs.
func (c *claude) mirrorState(home, cwd string, trusted bool) {
	var src map[string]json.RawMessage
	b, err := os.ReadFile(c.stateFile(c.DefaultHome()))
	if err != nil || json.Unmarshal(b, &src) != nil {
		return
	}
	p := c.stateFile(home)
	dst := map[string]json.RawMessage{}
	if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &dst) != nil {
		return // never overwrite what we could not parse
	}
	before, _ := json.Marshal(dst)
	for k, v := range src {
		if !claudePrivate.MatchString(k) {
			dst[k] = v
		}
	}

	// Folder trust: main's trusted folders, plus this one when the user has
	// already trusted it in this tab.
	projects := map[string]map[string]any{}
	json.Unmarshal(dst["projects"], &projects)
	var mainProjects map[string]map[string]any
	json.Unmarshal(src["projects"], &mainProjects)
	for k, v := range mainProjects {
		if v["hasTrustDialogAccepted"] == true {
			ensure(projects, k)["hasTrustDialogAccepted"] = true
		}
		if servers, _ := v["mcpServers"].(map[string]any); len(servers) > 0 {
			mirrorProjectMcp(ensure(projects, k), servers, v["disabledMcpServers"])
		}
	}
	if trusted && cwd != "" {
		ensure(projects, strings.ReplaceAll(cwd, `\`, "/"))["hasTrustDialogAccepted"] = true
	}
	dst["projects"], _ = json.Marshal(projects)

	after, _ := json.Marshal(dst)
	if bytes.Equal(before, after) {
		return
	}
	out, _ := json.MarshalIndent(dst, "", "  ")
	os.WriteFile(p, out, 0o644)
}

// mirrorProjectMcp gives the account the MCP servers main set up for one
// folder ("local" scope). One main had switched off stays off when it first
// arrives; after that the account's own switches (AIT's MCP list) rule.
func mirrorProjectMcp(dst map[string]any, servers map[string]any, mainOff any) {
	have, _ := dst["mcpServers"].(map[string]any)
	if have == nil {
		have = map[string]any{}
	}
	off, _ := dst["disabledMcpServers"].([]any)
	offMain, _ := mainOff.([]any)
	for name, cfg := range servers {
		if _, ok := have[name]; !ok && slices.Contains(offMain, any(name)) && !slices.Contains(off, any(name)) {
			off = append(off, name)
		}
		have[name] = cfg
	}
	dst["mcpServers"] = have
	if len(off) > 0 {
		dst["disabledMcpServers"] = off
	}
}

func ensure(m map[string]map[string]any, k string) map[string]any {
	if m[k] == nil {
		m[k] = map[string]any{}
	}
	return m[k]
}

// TrustAnswer accepts the "do you trust this folder" prompt. Claude asks it
// on every launch in the home folder, because it never saves that answer —
// so a handoff there would otherwise stop at the prompt. Only used when the
// user already accepted it in this tab. The cursor starts on the first
// option, so the order on screen decides the keys.
func (c *claude) TrustAnswer(all, fresh string) (string, bool) {
	if !strings.Contains(all, "trust this folder") && !strings.Contains(all, "Do you trust") {
		return "", false
	}
	m := trustCursor.FindAllStringSubmatch(fresh, -1)
	if m == nil {
		return "", false
	}
	if m[len(m)-1][1] == "Yes" {
		return "\r", true
	}
	return "\x1b[B", false
}

// trustCursor finds the highlighted option: "❯ No, exit", "❯ 1. Yes, proceed".
var trustCursor = regexp.MustCompile(`❯ (?:\d\. )?(Yes|No)\b`)

// Models: every current version from the installed CLI's own catalog
// (models.go), newest first; the family aliases until that scan is done.
func (c *claude) Models() []Model {
	out := []Model{{ID: "", Name: "Default", Desc: "Your Claude settings"}}
	catalog.mu.Lock()
	ms := catalog.models
	catalog.mu.Unlock()
	if len(ms) > 0 {
		return append(out, ms...)
	}
	for _, f := range []string{"fable", "opus", "sonnet", "haiku"} {
		name := strings.ToUpper(f[:1]) + f[1:]
		out = append(out, Model{ID: f, Name: name + " (latest)", Family: name, Desc: claudeFamilies[f], Long: longContext[f]})
	}
	return out
}

func (c *claude) ModelArgs(id string) []string { return []string{"--model", id} }

// AccessArgs: --add-dir takes several values, so it must be followed by
// another flag, never a bare prompt; Args and ChatArgs always put
// --resume/--session-id after the extras.
func (c *claude) AccessArgs(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		out = append(out, "--add-dir", d)
	}
	return out
}

func (c *claude) RulesArgs(file, text string) []string {
	return []string{"--append-system-prompt-file", file}
}

func (c *claude) NestingEnv() []string {
	return []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SSE_PORT", "CLAUDE_CODE_CHILD_SESSION",
		"CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_EXECPATH", "CLAUDE_PID",
		"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_EFFORT"}
}

func (c *claude) Args(l Launch) []string {
	args := append([]string{}, l.Extra...)
	if l.ResumeID != "" {
		args = append(args, "--resume", l.ResumeID)
	} else if l.NewID != "" {
		args = append(args, "--session-id", l.NewID)
	}
	if l.Prompt != "" {
		args = append(args, l.Prompt)
	}
	return args
}

func (c *claude) projectDir(home, cwd string) string {
	return filepath.Join(home, "projects", projectKey(cwd))
}

func (c *claude) SessionFile(home, cwd, id string) string {
	return filepath.Join(c.projectDir(home, cwd), id+".jsonl")
}

func (c *claude) NewestSession(home, cwd string, after time.Time, skip map[string]bool) string {
	return newestCreated(c.projectDir(home, cwd), ".jsonl", false, after, skip)
}

func (c *claude) SessionID(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

func (c *claude) Carry(path, toHome, cwd string) (string, error) {
	dst := filepath.Join(c.projectDir(toHome, cwd), filepath.Base(path))
	if strings.EqualFold(dst, path) {
		return dst, nil
	}
	if err := copyFile(path, dst); err != nil {
		return "", err
	}
	if side := strings.TrimSuffix(path, ".jsonl"); fileExists(side) { // subagents, tool results
		copyDir(side, strings.TrimSuffix(dst, ".jsonl"))
	}
	return dst, nil
}

// Scan. Measured shape of the error claude records, from real transcripts:
//
//	{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit",
//	 "message":{"content":[{"type":"text",
//	   "text":"You've hit your session limit · resets 11pm (Europe/London)"}]}}
//
// Signed out is the same envelope with "error":"authentication_failed".
func (c *claude) Scan(path string, offset int64, since time.Time) (*limitHit, int64) {
	var hit *limitHit
	off := readLines(path, offset, func(line []byte) bool {
		if !bytes.Contains(line, []byte(`"isApiErrorMessage":true`)) {
			return true
		}
		var l struct {
			Timestamp string `json:"timestamp"`
			IsAPIErr  bool   `json:"isApiErrorMessage"`
			Error     string `json:"error"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &l) != nil || !l.IsAPIErr || stampedBefore(l.Timestamp, since) {
			return true
		}
		text := contentText(l.Message.Content)
		switch l.Error {
		case "authentication_failed":
			hit = &limitHit{SignedOut: true, Text: text}
		case "rate_limit":
			hit = &limitHit{Text: text, Until: resetTime(text, time.Now())}
		}
		return hit == nil
	})
	return hit, off
}

func (c *claude) Chats(home string) []Chat {
	var out []Chat
	dirs, _ := os.ReadDir(filepath.Join(home, "projects"))
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(home, "projects", d.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			p := filepath.Join(dir, f.Name())
			if ch, ok := cachedChat(p, c.readChat); ok {
				ch.Home = home
				out = append(out, ch)
			}
		}
	}
	return out
}

// readChat pulls the title and folder from a transcript: the latest
// custom-title (/rename) or ai-title near the end, else the first thing the
// user typed near the start.
func (c *claude) readChat(p string) (Chat, bool) {
	ch := Chat{Provider: "claude", ID: c.SessionID(p), Path: p}
	head := headLines(p, 96<<10, 16<<20)
	_, tail := headTail(p, 0, 256<<10)
	var firstPrompt string
	for _, line := range bytes.Split(head, []byte{'\n'}) {
		var l struct {
			Type    string `json:"type"`
			Cwd     string `json:"cwd"`
			IsMeta  bool   `json:"isMeta"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		if ch.Cwd == "" && l.Cwd != "" {
			ch.Cwd = l.Cwd
		}
		if firstPrompt == "" && l.Type == "user" && !l.IsMeta {
			if t := strings.TrimSpace(contentText(l.Message.Content)); t != "" && !strings.HasPrefix(t, "<") {
				firstPrompt = t
			}
		}
		if ch.Cwd != "" && firstPrompt != "" {
			break
		}
	}
	var ai, custom string
	for _, line := range bytes.Split(tail, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`-title"`)) {
			continue
		}
		var l struct {
			Type   string `json:"type"`
			AI     string `json:"aiTitle"`
			Custom string `json:"customTitle"`
		}
		if json.Unmarshal(line, &l) == nil {
			if l.Type == "custom-title" && l.Custom != "" {
				custom = l.Custom
			} else if l.Type == "ai-title" && l.AI != "" {
				ai = l.AI
			}
		}
	}
	switch {
	case custom != "":
		ch.Title = custom
	case ai != "":
		ch.Title = ai
	default:
		ch.Title = firstPrompt
	}
	return ch, ch.Title != "" // no prompt yet: nothing worth listing
}

// projectKey matches how claude names a working directory under projects/:
// every character that is not a letter or digit becomes '-'.
func projectKey(dir string) string {
	b := []byte(dir)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

// ---- transcript reading helpers (shared) ------------------------------------

// readLines feeds each complete line appended since offset to fn, stopping
// early when fn returns false, and returns the offset to resume from. A
// half-written final line is left for next time.
func readLines(path string, offset int64, fn func([]byte) bool) int64 {
	f, err := os.Open(path)
	if err != nil {
		return offset
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return offset
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return offset
	}
	data = data[:end+1]
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) > 0 && !fn(line) {
			break
		}
	}
	return offset + int64(len(data))
}

func stampedBefore(ts string, since time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, ts)
	return err == nil && t.Before(since)
}

// headLines is the start of a file in whole lines: at least n bytes, with the
// line in progress at that point read to its end (to max in all). A chat that
// began as a handover has the whole earlier conversation in its first line;
// cutting that line in half left the chat without a title, so AIT hid it from
// History and could not reopen it.
func headLines(p string, n, max int64) []byte {
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var out []byte
	for int64(len(out)) < n {
		line, err := r.ReadBytes('\n')
		out = append(out, line...)
		if err != nil || int64(len(out)) >= max {
			break
		}
	}
	return out
}

func headTail(p string, headN, tailN int64) (head, tail []byte) {
	f, err := os.Open(p)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	info, _ := f.Stat()
	size := info.Size()
	head = make([]byte, min(headN, size))
	io.ReadFull(f, head)
	if size <= headN {
		return head, head
	}
	start := max(size-tailN, 0)
	tail = make([]byte, size-start)
	f.ReadAt(tail, start)
	return head, tail
}
