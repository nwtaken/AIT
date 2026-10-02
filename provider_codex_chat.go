package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Codex's streaming mode is its app-server: JSON-RPC over stdin/stdout, one
// JSON object per line (measured against codex-cli 0.155):
//
//	→ initialize · ← result · → initialized · → thread/start|thread/resume
//	→ turn/start {threadId, input}            one per user message
//	← item/started|completed {item}           agentMessage, reasoning,
//	                                          commandExecution, fileChange …
//	← item/agentMessage/delta                 streamed text
//	← item/*/requestApproval (a request)      answered with {decision}
//	← account/rateLimits/updated              live quota
//	← turn/completed
//
// Unlike Claude, nothing can be sent until the thread exists, so user
// messages that arrive early wait in a queue (ChatUser / onThread).

func (c *codex) ChatArgs(l Launch, perm string) []string {
	args := []string{"app-server"}
	for i := 0; i < len(l.Extra); i++ {
		switch a := l.Extra[i]; {
		case a == "-c" && i+1 < len(l.Extra): // rules (developer_instructions) and user config
			args = append(args, "-c", l.Extra[i+1])
			i++
		case (a == "-m" || a == "--model") && i+1 < len(l.Extra):
			args = append(args, "-c", "model="+tomlString(l.Extra[i+1]))
			i++
		case (a == "--sandbox" || a == "-s" || a == "--add-dir") && i+1 < len(l.Extra):
			i++ // handled per thread in ChatStart
		}
	}
	return args
}

func tomlString(s string) string { b, _ := json.Marshal(s); return string(b) }

func rpc(id int, method string, params any) []byte {
	m := map[string]any{"method": method, "params": params}
	if id > 0 {
		m["id"] = id
	}
	b, _ := json.Marshal(m)
	return append(b, '\n')
}

// approval settings for AIT's three permission choices.
func codexPolicy(perm string) (approval, sandbox string) {
	switch perm {
	case "never":
		return "never", "danger-full-access"
	case "edits":
		return "on-request", "workspace-write"
	default:
		return "untrusted", "workspace-write"
	}
}

func (c *codex) ChatStart(st *ChatState, l Launch, perm, cwd string) [][]byte {
	approval, sandbox := codexPolicy(perm)
	if hasFlag(l.Extra, "--sandbox") && sandbox != "read-only" {
		sandbox = "danger-full-access" // file access: everywhere
	}
	st.Put("start", map[string]any{"cwd": cwd, "approvalPolicy": approval, "sandbox": sandbox})
	st.Put("resume", l.ResumeID)
	id := st.Next()
	st.Put("initID", id)
	return [][]byte{rpc(id, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "AIT", "version": Version}, "capabilities": nil})}
}

func (c *codex) ChatUser(st *ChatState, text string, images []Image) []byte {
	input := []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}}
	for _, im := range images {
		// The app-server takes images as data URLs.
		input = append(input, map[string]any{"type": "image", "url": "data:" + im.Media + ";base64," + base64.StdEncoding.EncodeToString(im.Data)})
	}
	thread, _ := st.Get("thread").(string)
	if thread == "" {
		q, _ := st.Get("queue").([][]map[string]any)
		st.Put("queue", append(q, input))
		return nil
	}
	return c.turn(st, thread, input)
}

func (c *codex) turn(st *ChatState, thread string, input []map[string]any) []byte {
	p := map[string]any{"threadId": thread, "input": input}
	if m, _ := st.Get("model").(string); m != "" {
		p["model"] = m
	}
	if e, _ := st.Get("effort").(string); e != "" {
		p["effort"] = e
	}
	return rpc(st.Next(), "turn/start", p)
}

func (c *codex) ChatReply(st *ChatState, req, decision string, ask json.RawMessage) []byte {
	var a struct {
		Method string `json:"method"`
		Params struct {
			Available []json.RawMessage `json:"availableDecisions"`
			Amend     json.RawMessage   `json:"proposedExecpolicyAmendment"`
		} `json:"params"`
	}
	json.Unmarshal(ask, &a)
	command := strings.Contains(a.Method, "commandExecution")
	var d any = "accept"
	switch decision {
	case "always":
		d = "acceptForSession"
		if command && len(a.Params.Amend) > 0 && string(a.Params.Amend) != "null" {
			d = map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": a.Params.Amend}}
		}
	case "deny":
		d = "decline"
		if command && !offers(a.Params.Available, "decline") {
			d = "cancel" // commands may only offer cancel
		}
	}
	id, err := strconv.Atoi(req)
	var rid any = req
	if err == nil {
		rid = id
	}
	b, _ := json.Marshal(map[string]any{"id": rid, "result": map[string]any{"decision": d}})
	return append(b, '\n')
}

func offers(list []json.RawMessage, s string) bool {
	if len(list) == 0 {
		return true
	}
	for _, x := range list {
		if string(x) == `"`+s+`"` {
			return true
		}
	}
	return false
}

func (c *codex) ChatControl(st *ChatState, what string) []byte {
	if m, ok := strings.CutPrefix(what, "model:"); ok {
		st.Put("model", m) // applies from the next turn
		return nil
	}
	if e, ok := strings.CutPrefix(what, "effort:"); ok {
		st.Put("effort", e) // applies from the next turn
		return nil
	}
	thread, _ := st.Get("thread").(string)
	turn, _ := st.Get("turn").(string)
	if thread == "" || turn == "" {
		return nil
	}
	return rpc(st.Next(), "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn})
}

type codexMsg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type codexItem struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	Command   string  `json:"command"`
	Output    *string `json:"aggregatedOutput"`
	ExitCode  *int    `json:"exitCode"`
	Status    string  `json:"status"`
	Server    string  `json:"server"`
	Tool      string  `json:"tool"`
	Arguments any     `json:"arguments"`
	Query     string  `json:"query"`
	Changes   []struct {
		Path string `json:"path"`
		Diff string `json:"diff"`
		Kind struct {
			Type string `json:"type"`
		} `json:"kind"`
	} `json:"changes"`
	Actions []struct {
		Command string `json:"command"`
	} `json:"commandActions"`
}

var psWrap = regexp.MustCompile(`(?is)^"?[^"]*\\(?:powershell|pwsh|cmd)(?:\.exe)?"?\s+(?:-NoProfile\s+)?(?:-Command|/c)\s+(.*)$`)

// shownCommand strips the shell wrapper Codex adds on Windows.
func (it codexItem) shownCommand() string {
	if len(it.Actions) == 1 && it.Actions[0].Command != "" {
		return it.Actions[0].Command
	}
	if m := psWrap.FindStringSubmatch(it.Command); m != nil {
		return strings.Trim(m[1], "'\"")
	}
	return it.Command
}

func (c *codex) ChatDecode(line []byte, st *ChatState) []Ev {
	var m codexMsg
	if json.Unmarshal(line, &m) != nil {
		return nil
	}
	// Responses to our requests.
	if m.Method == "" && len(m.ID) > 0 {
		id, _ := strconv.Atoi(string(m.ID))
		if m.Error != nil {
			return []Ev{{"k": "error", "text": m.Error.Message}}
		}
		if init, _ := st.Get("initID").(int); id == init {
			st.Send(rpc(0, "initialized", nil))
			start, _ := st.Get("start").(map[string]any)
			if resume, _ := st.Get("resume").(string); resume != "" {
				p := map[string]any{"threadId": resume}
				for k, v := range start {
					p[k] = v
				}
				st.Send(rpc(st.Next(), "thread/resume", p))
			} else {
				st.Send(rpc(st.Next(), "thread/start", start))
			}
			return nil
		}
		var r struct {
			Thread *struct {
				ID string `json:"id"`
			} `json:"thread"`
			Model  string `json:"model"`
			Effort string `json:"reasoningEffort"`
		}
		if json.Unmarshal(m.Result, &r) == nil && r.Thread != nil && r.Thread.ID != "" {
			evs := c.onThread(st, r.Thread.ID, r.Model)
			if r.Effort != "" {
				evs = append(evs, Ev{"k": "effort", "effort": r.Effort})
			}
			return evs
		}
		return nil
	}

	switch m.Method {
	case "turn/started":
		var p struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		json.Unmarshal(m.Params, &p)
		st.Put("turn", p.Turn.ID)
		return []Ev{{"k": "status", "s": "requesting"}, {"k": "msg", "id": p.Turn.ID}}
	case "item/started", "item/completed":
		var p struct {
			Item codexItem `json:"item"`
		}
		json.Unmarshal(m.Params, &p)
		return c.item(st, p.Item, m.Method == "item/completed")
	case "item/agentMessage/delta", "item/reasoning/summaryTextDelta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		json.Unmarshal(m.Params, &p)
		return []Ev{{"k": "delta", "i": p.ItemID, "text": p.Delta}}
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "execCommandApproval", "applyPatchApproval":
		return c.ask(st, line, m)
	case "account/rateLimits/updated":
		var p struct {
			RL struct {
				Primary, Secondary *struct {
					Used     float64 `json:"usedPercent"`
					ResetsAt int64   `json:"resetsAt"`
				}
			} `json:"rateLimits"`
		}
		json.Unmarshal(m.Params, &p)
		e := Ev{"k": "quota"}
		if p.RL.Primary != nil {
			e["five"], e["fiveReset"] = p.RL.Primary.Used/100, p.RL.Primary.ResetsAt
		}
		if p.RL.Secondary != nil {
			e["week"], e["weekReset"] = p.RL.Secondary.Used/100, p.RL.Secondary.ResetsAt
		}
		return []Ev{e}
	case "thread/tokenUsage/updated":
		var p struct {
			U struct {
				Last struct {
					Input int `json:"inputTokens"`
				} `json:"last"`
				Window int `json:"modelContextWindow"`
			} `json:"tokenUsage"`
		}
		json.Unmarshal(m.Params, &p)
		e := Ev{"k": "ctx", "ctx": p.U.Last.Input}
		if p.U.Window > 0 {
			e["window"] = p.U.Window
		}
		return []Ev{e}
	case "turn/completed":
		var p struct {
			Turn struct {
				Status     string `json:"status"`
				DurationMS float64
				Error      *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		json.Unmarshal(m.Params, &p)
		st.Put("turn", "")
		e := Ev{"k": "done", "ms": p.Turn.DurationMS}
		if p.Turn.Error != nil && p.Turn.Status == "failed" {
			e["error"] = p.Turn.Error.Message
		}
		return []Ev{e}
	case "error":
		var p struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		json.Unmarshal(m.Params, &p)
		if !p.WillRetry {
			return []Ev{{"k": "error", "text": p.Error.Message}}
		}
	}
	return nil
}

// onThread: the thread exists — say so, and send what the user typed meanwhile.
func (c *codex) onThread(st *ChatState, thread, model string) []Ev {
	st.Put("thread", thread)
	q, _ := st.Get("queue").([][]map[string]any)
	st.Put("queue", nil)
	for _, input := range q {
		st.Send(c.turn(st, thread, input))
	}
	return []Ev{{"k": "init", "session": thread, "model": model}}
}

func (c *codex) item(st *ChatState, it codexItem, done bool) []Ev {
	switch it.Type {
	case "agentMessage":
		if !done {
			return []Ev{{"k": "start", "i": it.ID, "type": "text"}}
		}
		return []Ev{{"k": "stop", "i": it.ID}}
	case "reasoning":
		if !done {
			return []Ev{{"k": "start", "i": it.ID, "type": "thinking"}}
		}
		return []Ev{{"k": "stop", "i": it.ID}}
	case "commandExecution":
		if !done {
			return []Ev{{"k": "tool", "id": it.ID, "name": "Bash", "input": map[string]any{"command": it.shownCommand()}}}
		}
		out := ""
		if it.Output != nil {
			out = *it.Output
		}
		ok := it.Status == "completed" && (it.ExitCode == nil || *it.ExitCode == 0)
		return []Ev{{"k": "result", "id": it.ID, "ok": ok, "text": out}}
	case "fileChange":
		if !done {
			st.Put("change:"+it.ID, it)
			var files []map[string]any
			for _, ch := range it.Changes {
				files = append(files, map[string]any{"path": ch.Path, "diff": ch.Diff, "kind": ch.Kind.Type})
			}
			in := map[string]any{"changes": files}
			if len(it.Changes) > 0 {
				in["file_path"] = it.Changes[0].Path
			}
			return []Ev{{"k": "tool", "id": it.ID, "name": "Patch", "input": in}}
		}
		return []Ev{{"k": "result", "id": it.ID, "ok": it.Status == "completed", "text": ""}}
	case "mcpToolCall":
		if !done {
			return []Ev{{"k": "tool", "id": it.ID, "name": "mcp__" + it.Server + "__" + it.Tool, "input": it.Arguments}}
		}
		return []Ev{{"k": "result", "id": it.ID, "ok": it.Status == "completed", "text": ""}}
	case "webSearch":
		if !done {
			return []Ev{{"k": "tool", "id": it.ID, "name": "WebSearch", "input": map[string]any{"query": it.Query}}}
		}
		return []Ev{{"k": "result", "id": it.ID, "ok": true, "text": ""}}
	}
	return nil
}

// ask turns an approval request into a permission card.
func (c *codex) ask(st *ChatState, line []byte, m codexMsg) []Ev {
	req := strings.Trim(string(m.ID), `"`)
	st.Asks[req] = json.RawMessage(append([]byte(nil), line...))
	var p struct {
		ItemID  string `json:"itemId"`
		Command string `json:"command"`
		Reason  string `json:"reason"`
		Actions []struct {
			Command string `json:"command"`
		} `json:"commandActions"`
	}
	json.Unmarshal(m.Params, &p)
	if strings.Contains(m.Method, "ommand") || m.Method == "execCommandApproval" {
		it := codexItem{Command: p.Command}
		for _, a := range p.Actions {
			it.Actions = append(it.Actions, struct {
				Command string `json:"command"`
			}{a.Command})
		}
		return []Ev{{"k": "ask", "req": req, "tool": "Bash", "desc": p.Reason, "input": map[string]any{"command": it.shownCommand()}, "always": true}}
	}
	in := map[string]any{}
	if it, ok := st.Get("change:" + p.ItemID).(codexItem); ok && len(it.Changes) > 0 {
		var files []map[string]any
		for _, ch := range it.Changes {
			files = append(files, map[string]any{"path": ch.Path, "diff": ch.Diff, "kind": ch.Kind.Type})
		}
		in = map[string]any{"file_path": it.Changes[0].Path, "changes": files}
	}
	return []Ev{{"k": "ask", "req": req, "tool": "Patch", "desc": p.Reason, "input": in, "always": true}}
}

// ChatHistory replays a Codex rollout: what the user typed, the answers,
// and the commands run.
func (c *codex) ChatHistory(path string) []Ev {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var out []Ev
	n := 0
	for sc.Scan() {
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Message string `json:"message"`
				Command any    `json:"command"`
				CallID  string `json:"call_id"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Type != "event_msg" {
			continue
		}
		switch l.Payload.Type {
		case "user_message":
			if t := strings.TrimSpace(l.Payload.Message); t != "" {
				out = append(out, Ev{"k": "user", "text": t})
			}
		case "agent_message":
			if t := strings.TrimSpace(l.Payload.Message); t != "" {
				n++
				out = append(out, Ev{"k": "msg", "id": fmt.Sprintf("h%d", n)}, Ev{"k": "text", "text": t})
			}
		}
	}
	return out
}
