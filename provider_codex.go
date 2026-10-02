package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// codex drives OpenAI's Codex CLI — "ChatGPT" in the UI, since that is the
// subscription it signs in with.
//
// Transcripts are "rollout" files: sessions/YYYY/MM/DD/rollout-<time>-<uuid>.jsonl.
// Codex cannot be handed a session id up front, so AIT finds a new
// session's file by creation time, then resumes it with `codex resume <id>`.
type codex struct{ userHome string }

func (c *codex) ID() string          { return "codex" }
func (c *codex) Name() string        { return "ChatGPT" }
func (c *codex) HomeEnv() string     { return "CODEX_HOME" }
func (c *codex) DefaultHome() string { return filepath.Join(c.userHome, ".codex") }
func (c *codex) PresetsID() bool     { return false }
func (c *codex) LoginArgs() []string { return []string{"login"} }

// Command runs the npm launcher through node rather than the vendored exe:
// the launcher sets the env vars codex uses to manage its own updates.
func (c *codex) Command() []string {
	if d, err := os.UserConfigDir(); err == nil {
		js := filepath.Join(d, "npm", "node_modules", "@openai", "codex", "bin", "codex.js")
		if node, err := exec.LookPath("node.exe"); err == nil && fileExists(js) {
			return []string{node, js}
		}
	}
	if p := lookBinary("codex"); p != "" && strings.EqualFold(filepath.Ext(p), ".exe") {
		return []string{p}
	}
	return nil
}

func (c *codex) SignedIn(home string) bool { return fileExists(filepath.Join(home, "auth.json")) }

// Email is not shown for Codex: its address only exists inside the login
// token, and AIT never opens login or token files (it only checks that one
// exists, in SignedIn).
func (c *codex) Email(home string) string { return "" }

func (c *codex) Provision(home, cwd string, trusted bool) {
	main := c.DefaultHome()
	if strings.EqualFold(filepath.Clean(home), main) {
		return
	}
	os.MkdirAll(home, 0o755)
	for _, f := range []string{"config.toml", "AGENTS.md"} {
		if fileExists(filepath.Join(main, f)) {
			copyFile(filepath.Join(main, f), filepath.Join(home, f))
		}
	}
	for _, d := range []string{"skills", "prompts"} {
		junction(filepath.Join(main, d), filepath.Join(home, d))
	}
	if trusted && cwd != "" {
		p := filepath.Join(home, "config.toml")
		b, _ := os.ReadFile(p)
		table := "[projects.'" + cwd + "']"
		if !strings.Contains(string(b), table) {
			f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err == nil {
				f.WriteString("\n" + table + "\ntrust_level = \"trusted\"\n")
				f.Close()
			}
		}
	}
}

// Models reads the list Codex itself keeps for the signed-in account
// (models_cache.json), so it always matches what the account can use.
func (c *codex) Models() []Model {
	out := []Model{{ID: "", Name: "Default", Desc: "Your Codex settings"}}
	var cache struct {
		Models []struct {
			Slug, Description, Visibility string
			DisplayName                   string `json:"display_name"`
			Priority                      int
			Effort                        string `json:"default_reasoning_level"`
			Levels                        []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	b, err := os.ReadFile(filepath.Join(c.DefaultHome(), "models_cache.json"))
	if err != nil || json.Unmarshal(b, &cache) != nil {
		return out
	}
	sort.SliceStable(cache.Models, func(i, j int) bool { return cache.Models[i].Priority < cache.Models[j].Priority })
	for _, m := range cache.Models {
		if m.Visibility == "list" && m.Slug != "" {
			name := m.DisplayName
			if name == "" {
				name = m.Slug
			}
			fam := name
			if i := strings.LastIndex(name, "-"); i > 0 && !strings.ContainsAny(name[i+1:], "0123456789") {
				fam = name[:i]
			}
			var levels []string
			for _, l := range m.Levels {
				levels = append(levels, l.Effort)
			}
			out = append(out, Model{ID: m.Slug, Name: name, Desc: m.Description, Family: fam, Efforts: levels, Effort: m.Effort})
		}
	}
	return out
}

func (c *codex) ModelArgs(id string) []string { return []string{"-m", id} }

func (c *codex) EffortArgs(level string) []string {
	return []string{"-c", "model_reasoning_effort=" + tomlString(level)}
}

// ShareMemory: Codex keeps its own memories in a database; that is left
// alone and the shared folder is reached through its instructions.
func (c *codex) ShareMemory(home, cwd, shared string) {}

// AccessArgs: "everywhere" is Codex's full-access sandbox mode. Listing
// whole drives as writable roots instead was measured to stall Codex's
// Windows sandbox (commands never ran). Approvals still apply.
func (c *codex) AccessArgs(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	return []string{"--sandbox", "danger-full-access"}
}

// RulesArgs uses developer_instructions, which codex adds to its own. The
// value is a TOML basic string; JSON string escaping is a valid subset.
func (c *codex) RulesArgs(file, text string) []string {
	q, _ := json.Marshal(text)
	return []string{"-c", "developer_instructions=" + string(q)}
}

func (c *codex) NestingEnv() []string {
	return []string{"CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CODEX_MANAGED_BY_NPM", "CODEX_MANAGED_PACKAGE_ROOT", "CODEX_THREAD_ID"}
}

// Codex keeps folder trust in config.toml for every folder, so Provision
// covers it and there is never a prompt to answer.
func (c *codex) TrustAnswer(string, string) (string, bool) { return "", false }

func (c *codex) Args(l Launch) []string {
	if l.ResumeID != "" {
		args := append([]string{"resume"}, l.Extra...)
		args = append(args, l.ResumeID)
		if l.Prompt != "" {
			args = append(args, l.Prompt)
		}
		return args
	}
	args := append([]string{}, l.Extra...)
	if l.Prompt != "" {
		args = append(args, l.Prompt)
	}
	return args
}

var rolloutID = regexp.MustCompile(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$`)

func (c *codex) SessionID(path string) string {
	if m := rolloutID.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return ""
}

func (c *codex) SessionFile(home, cwd, id string) string {
	found := ""
	filepath.Walk(filepath.Join(home, "sessions"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, id+".jsonl") {
			found = p
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// NewestSession only looks in today's and yesterday's folders — a session
// just started cannot be anywhere else, and the archive grows forever.
func (c *codex) NewestSession(home, cwd string, after time.Time, skip map[string]bool) string {
	best := ""
	for _, day := range []time.Time{time.Now(), time.Now().AddDate(0, 0, -1)} {
		dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
		if p := newestCreated(dir, ".jsonl", false, after, skip); p != "" {
			if info, err := os.Stat(p); err == nil {
				best, after = p, fileCreated(info)
			}
		}
	}
	return best
}

func (c *codex) Carry(path, toHome, cwd string) (string, error) {
	rel := filepath.Base(path)
	if i := strings.LastIndex(strings.ToLower(path), string(filepath.Separator)+"sessions"+string(filepath.Separator)); i >= 0 {
		rel = path[i+len("/sessions/"):]
	}
	dst := filepath.Join(toHome, "sessions", rel)
	if strings.EqualFold(dst, path) {
		return dst, nil
	}
	return dst, copyFile(path, dst)
}

var (
	codexLimitText   = regexp.MustCompile(`(?i)usage limit|rate limit|hit your|quota`)
	codexSignedOutRe = regexp.MustCompile(`(?i)log ?in again|logged out|unauthori[sz]ed|\b401\b|sign in again`)
)

// Scan. Codex attaches the live quota to every token_count event:
//
//	{"type":"event_msg","payload":{"type":"token_count",…,"rate_limits":{
//	  "primary":{"used_percent":67.0,"window_minutes":300,"resets_at":1790093876},
//	  "secondary":{"used_percent":26.0,"window_minutes":10080,"resets_at":…},
//	  "rate_limit_reached_type":null}}}
//
// A window at 100% (or rate_limit_reached_type set) is a hit, with an exact
// reset time — better than parsing prose. An "error" event about limits or
// sign-in is the fallback.
func (c *codex) Scan(path string, offset int64, since time.Time) (*limitHit, int64) {
	var hit *limitHit
	off := readLines(path, offset, func(line []byte) bool {
		if !bytes.Contains(line, []byte(`"event_msg"`)) {
			return true
		}
		var l struct {
			Timestamp string `json:"timestamp"`
			Payload   struct {
				Type       string `json:"type"`
				Message    string `json:"message"`
				RateLimits *struct {
					Primary, Secondary *struct {
						Used     float64 `json:"used_percent"`
						ResetsAt int64   `json:"resets_at"`
					}
					Reached *string `json:"rate_limit_reached_type"`
				} `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil || stampedBefore(l.Timestamp, since) {
			return true
		}
		switch p := l.Payload; p.Type {
		case "token_count":
			rl := p.RateLimits
			if rl == nil {
				return true
			}
			var until int64
			for _, w := range []*struct {
				Used     float64 `json:"used_percent"`
				ResetsAt int64   `json:"resets_at"`
			}{rl.Primary, rl.Secondary} {
				if w != nil && w.Used >= 100 && w.ResetsAt > until {
					until = w.ResetsAt
				}
			}
			if until == 0 && rl.Reached != nil && *rl.Reached != "" {
				until = time.Now().Add(30 * time.Minute).Unix()
			}
			if until > 0 {
				hit = &limitHit{Text: "usage limit reached", Until: time.Unix(until, 0).Add(time.Minute)}
			}
		case "error":
			if codexSignedOutRe.MatchString(p.Message) {
				hit = &limitHit{SignedOut: true, Text: p.Message}
			} else if codexLimitText.MatchString(p.Message) {
				hit = &limitHit{Text: p.Message, Until: resetTime(p.Message, time.Now())}
			}
		}
		return hit == nil
	})
	return hit, off
}

func (c *codex) Chats(home string) []Chat {
	var out []Chat
	filepath.Walk(filepath.Join(home, "sessions"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(p) != ".jsonl" {
			return nil
		}
		if ch, ok := cachedChat(p, c.readChat); ok {
			ch.Home = home
			out = append(out, ch)
		}
		return nil
	})
	return out
}

func (c *codex) readChat(p string) (Chat, bool) {
	ch := Chat{Provider: "codex", ID: c.SessionID(p), Path: p}
	head, _ := headTail(p, 256<<10, 0)
	for _, line := range bytes.Split(head, []byte{'\n'}) {
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Cwd     string `json:"cwd"`
				Message string `json:"message"`
				Item    struct {
					Type    string `json:"type"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		if l.Type == "session_meta" && ch.Cwd == "" {
			ch.Cwd = l.Payload.Cwd
		}
		if l.Type == "event_msg" && l.Payload.Type == "user_message" && ch.Title == "" {
			ch.Title = strings.TrimSpace(l.Payload.Message)
		}
		// Codex 0.155+ records the prompt as an item_completed UserMessage.
		if l.Type == "event_msg" && l.Payload.Type == "item_completed" && l.Payload.Item.Type == "UserMessage" &&
			ch.Title == "" && len(l.Payload.Item.Content) > 0 {
			ch.Title = strings.TrimSpace(l.Payload.Item.Content[0].Text)
		}
		if ch.Cwd != "" && ch.Title != "" {
			break
		}
	}
	return ch, ch.Title != "" && ch.ID != ""
}

var codexMcpHeader = regexp.MustCompile(`(?m)^\[mcp_servers\.([A-Za-z0-9_-]+)\]`)

// McpKnown keeps the names that are [mcp_servers.NAME] tables in the
// account's config.toml.
func (c *codex) McpKnown(home string, names []string) []string {
	b, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	have := map[string]bool{}
	for _, m := range codexMcpHeader.FindAllStringSubmatch(string(b), -1) {
		have[m[1]] = true
	}
	var out []string
	for _, n := range names {
		if have[n] {
			out = append(out, n)
		}
	}
	return out
}
