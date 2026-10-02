package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// Claude's streaming mode, measured against Claude Code 2.1.286:
//
//	claude -p --input-format stream-json --output-format stream-json --verbose
//	       --include-partial-messages --permission-prompts host
//	       --permission-prompt-tool stdio
//
// stdout lines: system{init,status}, stream_event{message_start,
// content_block_start/delta/stop, message_delta, message_stop}, assistant
// (whole message), user (tool results), rate_limit_event, result, and
// control_request{can_use_tool} for permissions. Without
// --permission-prompt-tool stdio, "host" prompts are silently denied.

func (c *claude) ChatArgs(l Launch, perm string) []string {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--include-partial-messages",
		"--permission-prompts", "host", "--permission-prompt-tool", "stdio"}
	switch perm {
	case "review":
		args = append(args, "--permission-mode", "plan")
	case "never":
		args = append(args, "--permission-mode", "bypassPermissions")
	case "edits":
		args = append(args, "--permission-mode", "acceptEdits")
	default:
		args = append(args, "--permission-mode", "default")
	}
	args = append(args, l.Extra...)
	if l.ResumeID != "" {
		args = append(args, "--resume", l.ResumeID)
	} else if l.NewID != "" {
		args = append(args, "--session-id", l.NewID)
	}
	return args
}

// ChatStart asks for the model list (with each model's effort levels) and
// the effort in use; the answers come back as control_responses.
func (c *claude) ChatStart(st *ChatState, l Launch, perm, cwd string) [][]byte {
	st.Put("mcpOff", l.McpOff) // switched off once initialize is answered; earlier is ignored
	return [][]byte{claudeControl("ait-init", map[string]any{"subtype": "initialize"}), claudeSettings()}
}

func (c *claude) EffortArgs(level string) []string { return []string{"--effort", level} }

func claudeControl(id string, req map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"type": "control_request", "request_id": id, "request": req})
	return append(b, '\n')
}

// claudeSettings asks for the settings in effect; the answer reports the effort.
func claudeSettings() []byte {
	return claudeControl("ait-settings-"+newUUID(), map[string]any{"subtype": "get_settings"})
}

func (c *claude) ChatUser(st *ChatState, text string, images []Image) []byte {
	var content []map[string]any
	for _, im := range images {
		content = append(content, map[string]any{"type": "image", "source": map[string]any{
			"type": "base64", "media_type": im.Media, "data": base64.StdEncoding.EncodeToString(im.Data)}})
	}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	return append(b, '\n')
}

func (c *claude) ChatReply(st *ChatState, req, decision string, ask json.RawMessage) []byte {
	var a struct {
		Input       json.RawMessage `json:"input"`
		Suggestions json.RawMessage `json:"permission_suggestions"`
	}
	json.Unmarshal(ask, &a)
	resp := map[string]any{"behavior": "deny", "message": "The user declined this."}
	if decision != "deny" {
		resp = map[string]any{"behavior": "allow", "updatedInput": a.Input}
		if decision == "always" && len(a.Suggestions) > 0 {
			resp["updatedPermissions"] = a.Suggestions
		}
	}
	b, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": req, "response": resp}})
	return append(b, '\n')
}

func (c *claude) ChatControl(st *ChatState, what string) []byte {
	if m, ok := strings.CutPrefix(what, "model:"); ok {
		req := map[string]any{"subtype": "set_model"}
		if m != "" {
			req["model"] = m
		}
		// The effort may change with the model (per-model settings, levels it lacks).
		return append(claudeControl(newUUID(), req), claudeSettings()...)
	}
	if e, ok := strings.CutPrefix(what, "effort:"); ok {
		req := map[string]any{"subtype": "apply_flag_settings", "settings": map[string]any{"effortLevel": e}}
		return append(claudeControl(newUUID(), req), claudeSettings()...)
	}
	if what == "mcp-status" {
		return claudeControl("ait-mcp-"+newUUID(), map[string]any{"subtype": "mcp_status"})
	}
	return claudeControl(newUUID(), map[string]any{"subtype": "interrupt"})
}

// claudeMcpOff switches off the servers the user turned off in AIT. A server
// switched off while still connecting comes back on when it connects, so it
// waits until none is connecting (status nil = just started), then switches
// off what is on and checks once more.
func claudeMcpOff(st *ChatState, status []map[string]any) {
	off, _ := st.Get("mcpOff").([]string)
	tries, _ := st.Get("mcpTries").(int)
	if len(off) == 0 || tries >= 30 {
		return
	}
	st.Put("mcpTries", tries+1)
	check := func(after time.Duration) {
		time.AfterFunc(after, func() {
			st.Send(claudeControl(fmt.Sprintf("ait-mcpcheck-%d", tries), map[string]any{"subtype": "mcp_status"}))
		})
	}
	if status == nil {
		check(time.Second)
		return
	}
	on := map[string]bool{}
	for _, s := range status {
		if s["status"] == "pending" {
			check(time.Second)
			return
		}
		if s["status"] != "disabled" {
			on[s["name"].(string)] = true
		}
	}
	toggled := false
	for _, name := range off {
		if on[name] {
			st.Send(claudeControl(newUUID(), map[string]any{"subtype": "mcp_toggle", "serverName": name, "enabled": false}))
			toggled = true
		}
	}
	if toggled {
		check(2 * time.Second)
	}
}

// McpToggle switches an MCP server in the running chat, then asks for the
// new list.
func (c *claude) McpToggle(st *ChatState, name string, on bool) []byte {
	// Keep the start-up re-check from undoing this.
	off, _ := st.Get("mcpOff").([]string)
	off = slices.DeleteFunc(slices.Clone(off), func(n string) bool { return n == name })
	if !on {
		off = append(off, name)
	}
	st.Put("mcpOff", off)
	b := claudeControl(newUUID(), map[string]any{"subtype": "mcp_toggle", "serverName": name, "enabled": on})
	return append(b, claudeControl("ait-mcp-"+newUUID(), map[string]any{"subtype": "mcp_status"})...)
}

type claudeLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Model     string          `json:"model"`
	Status    string          `json:"status"`
	Parent    *string         `json:"parent_tool_use_id"`
	Event     json.RawMessage `json:"event"`
	Message   struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   map[string]any  `json:"usage"`
	} `json:"message"`
	Commands      []json.RawMessage `json:"slash_commands"`
	TUIOnly       json.RawMessage   `json:"terminal_slash_commands"`
	RequestID     string            `json:"request_id"`
	Request       json.RawMessage   `json:"request"`
	RateLimit     json.RawMessage   `json:"rate_limit_info"`
	IsError       bool              `json:"is_error"`
	Result        string            `json:"result"`
	DurationMS    float64           `json:"duration_ms"`
	Cost          float64           `json:"total_cost_usd"`
	IsAPIErr      bool              `json:"isApiErrorMessage"`
	ToolUseResult json.RawMessage   `json:"tool_use_result"`
	ModelUsage    map[string]struct {
		ContextWindow int `json:"contextWindow"`
	} `json:"modelUsage"`
}

func (c *claude) ChatDecode(line []byte, st *ChatState) []Ev {
	var l claudeLine
	if json.Unmarshal(line, &l) != nil {
		return nil
	}
	// A sub-agent's own work streams with its parent tool's id; the parent
	// tool row stands for all of it.
	if l.Parent != nil && *l.Parent != "" {
		return nil
	}
	switch l.Type {
	case "system":
		switch l.Subtype {
		case "init":
			var cmds []string
			for _, raw := range l.Commands {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					cmds = append(cmds, s)
				}
			}
			var tui []string
			json.Unmarshal(l.TUIOnly, &tui)
			return []Ev{{"k": "init", "session": l.SessionID, "model": l.Model, "commands": cmds, "tuiOnly": tui}}
		case "status":
			return []Ev{{"k": "status", "s": l.Status}}
		case "compact_boundary":
			var m struct {
				Meta struct {
					Pre  float64 `json:"pre_tokens"`
					Post float64 `json:"post_tokens"`
				} `json:"compact_metadata"`
			}
			json.Unmarshal(line, &m)
			return []Ev{{"k": "note", "text": "Conversation compacted", "pre": m.Meta.Pre, "post": m.Meta.Post}}
		}
	case "stream_event":
		return decodeStreamEvent(l.Event)
	case "assistant":
		// Slash commands (/context, /cost, /model …) answer with a
		// synthetic message that never streams; show it as command output.
		if l.Message.Model == "<synthetic>" {
			if t := strings.TrimSpace(contentText(l.Message.Content)); t != "" {
				return []Ev{{"k": "cmdout", "text": t}}
			}
			return nil
		}
		return assistantEvents(l.Message.Content, false)
	case "user":
		var s string
		if json.Unmarshal(l.Message.Content, &s) == nil {
			if out, ok := localStdout(s); ok && out != "" {
				return []Ev{{"k": "cmdout", "text": out}}
			}
			return nil // e.g. the summary a compaction feeds back in
		}
		return toolResults(l.Message.Content)
	case "control_response":
		if strings.Contains(string(line), `"ait-init"`) {
			claudeMcpOff(st, nil)
		}
		if strings.Contains(string(line), `"ait-mcpcheck`) {
			for _, e := range claudeAnswer(line) {
				if s, ok := e["servers"].([]map[string]any); ok {
					claudeMcpOff(st, s)
				}
			}
			return nil
		}
		return claudeAnswer(line)
	case "control_request":
		var r struct {
			Subtype string          `json:"subtype"`
			Tool    string          `json:"tool_name"`
			Display string          `json:"display_name"`
			Desc    string          `json:"description"`
			Input   json.RawMessage `json:"input"`
			Sugg    json.RawMessage `json:"permission_suggestions"`
		}
		if json.Unmarshal(l.Request, &r) != nil || r.Subtype != "can_use_tool" {
			return nil
		}
		st.Asks[l.RequestID] = l.Request
		var input any
		json.Unmarshal(r.Input, &input)
		name := r.Display
		if name == "" {
			name = r.Tool
		}
		return []Ev{{"k": "ask", "req": l.RequestID, "tool": name, "desc": r.Desc, "input": input, "always": len(r.Sugg) > 2}}
	case "rate_limit_event":
		var r struct {
			Status  string `json:"status"`
			Windows map[string]struct {
				Utilization float64 `json:"utilization"`
				ResetsAt    int64   `json:"resetsAt"`
			} `json:"unifiedWindows"`
		}
		if json.Unmarshal(l.RateLimit, &r) != nil {
			return nil
		}
		f, w := r.Windows["five_hour"], r.Windows["seven_day"]
		return []Ev{{"k": "quota", "status": r.Status, "five": f.Utilization, "fiveReset": f.ResetsAt, "week": w.Utilization, "weekReset": w.ResetsAt}}
	case "result":
		e := Ev{"k": "done", "ms": l.DurationMS, "cost": l.Cost}
		// Sub-agents report too; the largest window is the main model's.
		win := 0
		for _, u := range l.ModelUsage {
			win = max(win, u.ContextWindow)
		}
		if win > 0 {
			e["window"] = win
		}
		if l.IsError {
			e["error"] = l.Result
			if l.Result == "" {
				e["error"] = "The AI turn failed"
			}
		}
		return []Ev{e}
	}
	return nil
}

// localStdout extracts a slash command's printed output.
func localStdout(s string) (string, bool) {
	const open, close = "<local-command-stdout>", "</local-command-stdout>"
	i := strings.Index(s, open)
	if i < 0 {
		return "", false
	}
	s = s[i+len(open):]
	if j := strings.Index(s, close); j >= 0 {
		s = s[:j]
	}
	return strings.TrimSpace(s), true
}

func decodeStreamEvent(raw json.RawMessage) []Ev {
	var e struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			ID    string             `json:"id"`
			Model string             `json:"model"`
			Usage map[string]float64 `json:"usage"`
		} `json:"message"`
		Block struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return nil
	}
	switch e.Type {
	case "message_start":
		u := e.Message.Usage
		ctx := u["input_tokens"] + u["cache_creation_input_tokens"] + u["cache_read_input_tokens"]
		return []Ev{{"k": "msg", "id": e.Message.ID, "ctx": ctx, "model": e.Message.Model}}
	case "content_block_start":
		t := e.Block.Type
		if t == "tool_use" || t == "server_tool_use" {
			return []Ev{{"k": "start", "i": e.Index, "type": "tool", "id": e.Block.ID, "name": e.Block.Name}}
		}
		if t == "text" || t == "thinking" {
			return []Ev{{"k": "start", "i": e.Index, "type": t}}
		}
	case "content_block_delta":
		switch e.Delta.Type {
		case "text_delta":
			return []Ev{{"k": "delta", "i": e.Index, "text": e.Delta.Text}}
		case "thinking_delta":
			if e.Delta.Thinking != "" {
				return []Ev{{"k": "delta", "i": e.Index, "text": e.Delta.Thinking}}
			}
		}
	case "content_block_stop":
		return []Ev{{"k": "stop", "i": e.Index}}
	}
	return nil
}

// assistantEvents: the complete message repeats what streamed; only tool
// calls are taken from it (their input arrives whole here). For history,
// text blocks are emitted too.
func assistantEvents(content json.RawMessage, history bool) []Ev {
	var blocks []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking string          `json:"thinking"`
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out []Ev
	for _, b := range blocks {
		switch b.Type {
		case "tool_use", "server_tool_use":
			var in any
			json.Unmarshal(b.Input, &in)
			out = append(out, Ev{"k": "tool", "id": b.ID, "name": b.Name, "input": in})
		case "text":
			if history && strings.TrimSpace(b.Text) != "" {
				out = append(out, Ev{"k": "text", "text": b.Text})
			}
		case "thinking":
			if history && strings.TrimSpace(b.Thinking) != "" {
				out = append(out, Ev{"k": "thinking", "text": b.Thinking})
			}
		}
	}
	return out
}

func toolResults(content json.RawMessage) []Ev {
	var blocks []struct {
		Type    string          `json:"type"`
		ID      string          `json:"tool_use_id"`
		Content json.RawMessage `json:"content"`
		IsError bool            `json:"is_error"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out []Ev
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		text := contentText(b.Content)
		if len(text) > 20000 {
			text = cutText(text, 20000) + "\n…"
		}
		out = append(out, Ev{"k": "result", "id": b.ID, "ok": !b.IsError, "text": text})
	}
	return out
}

func (c *claude) ChatHistory(path string) []Ev {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var out []Ev
	for sc.Scan() {
		var l struct {
			Type      string `json:"type"`
			IsMeta    bool   `json:"isMeta"`
			IsAPIErr  bool   `json:"isApiErrorMessage"`
			Sidechain bool   `json:"isSidechain"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				ID      string          `json:"id"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.IsMeta || l.Sidechain {
			continue
		}
		switch l.Type {
		case "user":
			var s string
			if json.Unmarshal(l.Message.Content, &s) == nil {
				if t := strings.TrimSpace(s); t != "" && !strings.HasPrefix(t, "<") {
					out = append(out, Ev{"k": "user", "text": t})
				}
				continue
			}
			if r := toolResults(l.Message.Content); len(r) > 0 {
				out = append(out, r...)
				continue
			}
			if t := strings.TrimSpace(contentText(l.Message.Content)); t != "" && !strings.HasPrefix(t, "<") {
				out = append(out, Ev{"k": "user", "text": t})
			}
		case "assistant":
			if l.IsAPIErr {
				out = append(out, Ev{"k": "error", "text": contentText(l.Message.Content)})
				continue
			}
			out = append(out, Ev{"k": "msg", "id": l.Message.ID})
			out = append(out, assistantEvents(l.Message.Content, true)...)
		}
	}
	// Merge consecutive "msg" of the same id (one message is stored as one
	// line per content block).
	var merged []Ev
	lastID := ""
	for _, e := range out {
		if e["k"] == "msg" {
			if id, _ := e["id"].(string); id == lastID && id != "" {
				continue
			} else {
				lastID = id
			}
		} else if e["k"] == "user" {
			lastID = ""
		}
		merged = append(merged, e)
	}
	return merged
}

// claudeAnswer reads the answers to AIT's own requests: the model list from
// initialize ("efforts": levels by model id, alias and name; "" is the
// default model) and the effort in effect from get_settings ("effort"; ""
// when the model has no effort levels).
func claudeAnswer(line []byte) []Ev {
	var r struct {
		Response struct {
			ID       string `json:"request_id"`
			Response struct {
				Models []struct {
					Value  string   `json:"value"`
					Name   string   `json:"displayName"`
					Levels []string `json:"supportedEffortLevels"`
				} `json:"models"`
				Applied *struct {
					Effort *string `json:"effort"`
				} `json:"applied"`
				McpServers []struct {
					Name   string            `json:"name"`
					Status string            `json:"status"`
					Error  string            `json:"error"`
					Scope  string            `json:"scope"`
					Tools  []json.RawMessage `json:"tools"`
				} `json:"mcpServers"`
			} `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &r) != nil {
		return nil
	}
	resp := r.Response.Response
	switch {
	case r.Response.ID == "ait-init" && len(resp.Models) > 0:
		levels := map[string][]string{}
		for _, m := range resp.Models {
			key := m.Value
			if key == "default" {
				key = ""
			}
			levels[key] = m.Levels
			if m.Name != "" && !strings.HasPrefix(m.Name, "Default") {
				levels[m.Name] = m.Levels
			}
		}
		return []Ev{{"k": "efforts", "models": levels}}
	case strings.HasPrefix(r.Response.ID, "ait-mcp"): // ait-mcp-… and ait-mcpcheck-…
		servers := []map[string]any{}
		for _, m := range resp.McpServers {
			servers = append(servers, map[string]any{"name": m.Name, "status": m.Status, "error": m.Error, "scope": m.Scope, "tools": len(m.Tools)})
		}
		return []Ev{{"k": "mcp", "servers": servers}}
	case strings.HasPrefix(r.Response.ID, "ait-settings") && resp.Applied != nil:
		e := ""
		if resp.Applied.Effort != nil {
			e = *resp.Applied.Effort
		}
		return []Ev{{"k": "effort", "effort": e}}
	}
	return nil
}
