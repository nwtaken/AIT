package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Moving a conversation to another AI. Each AI stores conversations in its
// own format, so the history can't be handed over as a file the next AI
// resumes. Instead AIT writes the conversation out as plain markdown — what
// the user asked, the answers, the commands run, the files changed — and the
// next AI starts a fresh session told to read it and carry on.

const handoverLimit = 60 << 10 // keep handover files readable in one go

const handoverContinue = "Continue the user's latest request from the conversation you just reviewed. " +
	"Resume exactly where work stopped, preserve its constraints, and do not redo completed steps or recap the handover."

// Preparation has its own turn, so completion is a provider event rather than
// a guessed timer or a phrase matched in the assistant's output.
func (a *App) handoverEvents(t *Tab, cp ChatProvider, proc *chatProc, st *ChatState, gen int64, evs []Ev) []Ev {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gen.Load() != gen {
		return nil
	}
	if !t.reading {
		return evs
	}
	var out []Ev
	for _, e := range evs {
		switch e["k"] {
		case "init", "quota", "ctx", "efforts", "effort":
			out = append(out, e)
		case "ask":
			// Preparation only needs the supplied transcript, never file changes.
			req, _ := e["req"].(string)
			proc.send(cp.ChatReply(st, req, "deny", st.Asks[req]))
			delete(st.Asks, req)
		case "error":
			t.reading, t.handover = false, ""
			return append(out, Ev{"k": "handover", "state": "failed"}, e)
		case "done":
			t.reading, t.handover = false, ""
			if errText, _ := e["error"].(string); errText != "" || e["interrupted"] == true {
				return append(out, Ev{"k": "handover", "state": "failed"}, e)
			}
			if err := proc.send(cp.ChatUser(st, handoverContinue, nil)); err != nil {
				return append(out, Ev{"k": "handover", "state": "failed"}, Ev{"k": "error", "text": err.Error()})
			}
			return append(out, Ev{"k": "handover", "state": "complete"})
		}
	}
	return out
}

// nextAI finds the AI to continue on after every account of `from` is out:
// the next one in the user's order that is installed, can show a native
// chat, and has an account ready.
func (a *App) nextAI(from string) (Provider, Account, bool) {
	ids := a.store.Config().aiOrder()
	start := 0
	for i, id := range ids {
		if id == from {
			start = i + 1
		}
	}
	now := time.Now()
	for i := 0; i < len(ids); i++ {
		id := ids[(start+i)%len(ids)]
		p := registry[id]
		if id == from || p == nil || p.Command() == nil || chatOf(p) == nil {
			continue
		}
		if acct, ok := a.store.Pick(id, "", now); ok {
			return p, acct, true
		}
	}
	return nil, Account{}, false
}

// crossOver moves the tab's conversation to another AI. Caller holds t.mu.
func (a *App) crossOver(t *Tab, to Provider, acct Account, reason string) error {
	from := t.agent
	file, err := a.writeHandover(t, from, reason)
	if err != nil {
		return err
	}
	history, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	t.agent, t.profile = to, to.ID()
	t.session, t.model, t.effort = "", "", ""
	t.handover = fmt.Sprintf(
		"You are taking over a conversation from %s, which %s. The conversation so far, "+
			"including what was already done, is in this file: %s\n\n"+
			"This is the preparation turn only. Read the conversation included below, identify the latest "+
			"request and unfinished work, and retain the constraints and completed steps. Do not use tools "+
			"or begin work yet. Reply only READY when you have reviewed it. AIT will send a separate "+
			"message to continue automatically.\n\n<conversation>\n%s\n</conversation>", from.Name(), reason, file, history)
	if err := a.relaunch(t, acct, ""); err != nil {
		t.reading, t.handover = false, ""
		a.chatOut(t, []Ev{{"k": "handover", "state": "failed"}})
		return err
	}
	a.emit("tab:provider", t.id, to.ID(), to.Name(), from.Name(), reason)
	return nil
}

// ContinueOn is the "ask me" choice: move this tab's conversation to another AI now.
func (a *App) ContinueOn(id int, providerID string) error {
	t := a.tab(id)
	if t == nil {
		return fmt.Errorf("no tab")
	}
	to := registry[providerID]
	if to == nil || chatOf(to) == nil {
		return fmt.Errorf("%s can't take over a chat", providerID)
	}
	acct, ok := a.store.Pick(providerID, "", time.Now())
	if !ok {
		return fmt.Errorf("no %s account is available", to.Name())
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reading {
		return fmt.Errorf("wait for the conversation handover to finish")
	}
	if t.wait != nil {
		t.wait.Stop()
		t.wait = nil
	}
	return a.crossOver(t, to, acct, "ran out of usage")
}

// writeHandover renders the conversation as markdown for the next AI.
func (a *App) writeHandover(t *Tab, from Provider, reason string) (string, error) {
	var evs []Ev
	if cp := chatOf(from); cp != nil && t.session != "" && fileExists(t.session) {
		evs = cp.ChatHistory(t.session)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Conversation handed over from %s\n\n", from.Name())
	fmt.Fprintf(&b, "%s %s. Working folder: %s\n\n", from.Name(), reason, t.cwd)
	tools := map[string]string{}
	for _, e := range evs {
		switch e["k"] {
		case "user":
			fmt.Fprintf(&b, "## User\n\n%s\n\n", e["text"])
		case "text":
			fmt.Fprintf(&b, "## %s\n\n%s\n\n", from.Name(), e["text"])
		case "tool":
			name, _ := e["name"].(string)
			id, _ := e["id"].(string)
			tools[id] = name
			fmt.Fprintf(&b, "- %s\n", toolLine(name, e["input"]))
		case "result":
			if ok, _ := e["ok"].(bool); !ok {
				id, _ := e["id"].(string)
				text, _ := e["text"].(string)
				fmt.Fprintf(&b, "  - %s failed: %s\n", tools[id], firstLine(text))
			}
		}
	}
	text := b.String()
	if len(text) > handoverLimit {
		// Keep the start (the original request) and the most recent work.
		head := text[:8<<10]
		tail := text[len(text)-(handoverLimit-(8<<10)):]
		text = head + "\n\n[… earlier part of the conversation left out …]\n\n" + tail
	}
	dir := filepath.Join(a.store.root, "handovers")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, time.Now().Format("2006-01-02_15-04-05")+"-"+from.ID()+".md")
	return p, os.WriteFile(p, []byte(text), 0o644)
}

func toolLine(name string, input any) string {
	in, _ := input.(map[string]any)
	str := func(k string) string { s, _ := in[k].(string); return s }
	switch name {
	case "Bash", "PowerShell":
		return "Ran: `" + firstLine(str("command")) + "`"
	case "Read":
		return "Read " + str("file_path")
	case "Write":
		return "Created " + str("file_path")
	case "Edit", "MultiEdit", "Patch":
		return "Edited " + str("file_path")
	case "Grep", "Glob":
		return "Searched for " + str("pattern")
	case "WebFetch":
		return "Opened " + str("url")
	case "WebSearch":
		return "Searched the web for " + str("query")
	}
	return "Used " + name
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Hide AIT's internal preparation and continuation prompts when a chat is reopened.
func visibleHistory(evs []Ev) []Ev {
	out := make([]Ev, 0, len(evs))
	preparing := false
	for _, e := range evs {
		if e["k"] == "user" {
			text, _ := e["text"].(string)
			if strings.Contains(text, "This is the preparation turn only.") && strings.Contains(text, "<conversation>") {
				preparing = true
				continue
			}
			if strings.TrimSpace(text) == handoverContinue {
				preparing = false
				continue
			}
		}
		if !preparing {
			out = append(out, e)
		}
	}
	return out
}
