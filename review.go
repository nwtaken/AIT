package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const reviewMarker = "[[AIT_SUPEREVIEW]]"

// CanReview requires another signed-in, available AI with native chat support.
func (a *App) CanReview(id int) bool {
	t := a.tab(id)
	if t == nil {
		return false
	}
	t.mu.Lock()
	profile, native, closed := t.profile, t.native, t.closed
	t.mu.Unlock()
	if !native || closed {
		return false
	}
	_, _, ok := a.nextAI(profile)
	return ok
}

// ToggleReview switches /supereview: while on, the AIs are told they may ask
// another AI to review their work once they think it is finished. The
// instruction is part of the rules an AI gets at start, so the tab restarts
// on the same conversation (carrying on if it was working); other chats get
// it when they next start.
func (a *App) ToggleReview(id int) (bool, error) {
	a.store.mu.Lock()
	c := a.store.Config()
	c.Review = !c.Review
	on := c.Review
	err := a.store.saveConfig(c)
	a.store.mu.Unlock()
	if err != nil {
		return !on, err
	}
	t := a.tab(id)
	if t == nil {
		return on, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	acct, ok := a.store.Account(t.acct)
	if !ok || t.closed || t.reading || !t.native {
		return on, nil
	}
	prompt := ""
	if t.working.Load() {
		prompt = "continue"
	}
	return on, a.relaunch(t, acct, prompt)
}

// ReviewOn reports whether /supereview is switched on.
func (a *App) ReviewOn() bool { return a.store.Config().Review }

// Review starts one independent review; the current AI keeps the tab.
func (a *App) Review(id int) error {
	t := a.tab(id)
	if t == nil {
		return fmt.Errorf("no chat")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || !t.native || t.reading || t.reviewing {
		return fmt.Errorf("the chat is not ready for a review")
	}
	p, acct, ok := a.nextAI(t.profile)
	if !ok {
		return fmt.Errorf("/supereview needs 2+ connected accounts on different AIs")
	}
	file, err := a.writeHandover(t, t.agent, "requested a review")
	if err != nil {
		return err
	}
	history, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	prompt := reviewPrompt(t.cwd, string(history))
	t.reviewing = true
	gen := t.gen.Load()
	a.emit("review:start", id, p.Name())
	go a.finishReview(t, gen, p, acct, prompt)
	return nil
}

func reviewPrompt(cwd, history string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-ext-diff", "HEAD")
	cmd.Dir = cwd
	diff, _ := cmd.Output()
	if len(diff) > 40<<10 {
		diff = []byte(cutText(string(diff), 40<<10) + "\n[diff truncated; inspect the workspace for the rest]")
	}
	return "You are a second AI reviewing another AI's work. Read the user's exact request and the work so far. " +
		"Compare the result to that request and inspect the workspace if needed. For a simple request, still give your best concrete improvements. " +
		"Point out real defects, missing requirements, and worthwhile refinements; do not invent problems. " +
		"Do not edit files. Give the primary AI a concise actionable review with evidence, all of it in your final message (only that message is passed on).\n\n" +
		"<conversation>\n" + history + "\n</conversation>\n\n<git-diff>\n" + string(diff) + "\n</git-diff>"
}

func (a *App) finishReview(t *Tab, gen int64, p Provider, acct Account, prompt string) {
	feedback, err := a.runHelper(p, acct, t.cwd, "review", nil, 5*time.Minute, prompt, func(s string) { a.emit("review:step", t.id, s) }, nil)
	t.mu.Lock()
	if t.closed || t.gen.Load() != gen {
		t.reviewing = false
		t.mu.Unlock()
		return
	}
	t.reviewing = false
	if err == nil {
		cp := chatOf(t.agent)
		if cp == nil || t.chat == nil {
			err = errors.New("the original AI is no longer running")
		} else {
			err = t.chat.send(cp.ChatUser(t.chatState,
				"Independent review from "+p.Name()+":\n\n"+feedback+"\n\nAddress valid findings, then finish the user's request.", nil))
		}
	}
	t.mu.Unlock()
	if err != nil {
		a.emit("review:error", t.id, err.Error())
		// The AI ended its turn waiting for this review: tell it to go on.
		a.chatSend(t.id, "The independent review failed: "+err.Error()+". Continue the user's request without it.", nil)
		return
	}
	a.emit("review:done", t.id, p.Name(), feedback)
}

// runHelper runs an AI out of sight (a review, an MCP fix) with its own
// login, permission mode and deadline. step gets what it is doing; text gets
// each complete text block; the result is its last text block. A helper in
// "review" mode is denied every permission it asks for; others are allowed.
func (a *App) runHelper(p Provider, acct Account, cwd, perm string, extra []string, limit time.Duration, prompt string, step, text func(string)) (string, error) {
	cp := chatOf(p)
	if cp == nil || p.Command() == nil {
		return "", errors.New("the AI is unavailable")
	}
	l := Launch{Extra: extra}
	if acct.Model != nil && *acct.Model != "" {
		l.Extra = append(l.Extra, p.ModelArgs(*acct.Model)...)
	}
	cmdline := append(append([]string{}, p.Command()...), cp.ChatArgs(l, perm)...)
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, cmdline[0], cmdline[1:]...)
	cmd.Dir = cwd
	cmd.Env = baseEnv()
	if acct.Dir != "" {
		cmd.Env = append(cmd.Env, p.HomeEnv()+"="+acct.Dir)
	}
	hideConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	kill := killTree(cmd.Process.Pid)
	defer func() { kill(); cmd.Wait() }()
	proc := &chatProc{cmd: cmd, stdin: stdin}
	st := &ChatState{Asks: map[string]json.RawMessage{}, Data: map[string]any{}, Send: func(b []byte) { proc.send(b) }}
	for _, b := range cp.ChatStart(st, l, perm, cwd) {
		if err := proc.send(b); err != nil {
			return "", err
		}
	}
	if err := proc.send(cp.ChatUser(st, prompt, nil)); err != nil {
		return "", err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	textBlocks := map[any]bool{}
	// The result is the helper's last text block: earlier ones are its
	// narration ("I'll inspect…"). A block is its streamed text, or its
	// complete text ("final") when it did not stream.
	var cur strings.Builder // the block being streamed
	last := ""
	for sc.Scan() {
		for _, e := range cp.ChatDecode(sc.Bytes(), st) {
			switch e["k"] {
			case "tool": // what the helper is doing, for its progress
				name, _ := e["name"].(string)
				step(toolLine(name, e["input"]))
			case "final":
				block := cur.String()
				if strings.TrimSpace(block) == "" {
					block, _ = e["text"].(string)
				}
				if strings.TrimSpace(block) != "" {
					last = block
					if text != nil {
						text(block)
					}
				}
				cur.Reset()
			case "start":
				textBlocks[e["i"]] = e["type"] == "text"
			case "delta":
				if textBlocks[e["i"]] {
					if s, _ := e["text"].(string); s != "" && cur.Len()+len(s) <= 32<<10 {
						cur.WriteString(s)
					}
				}
			case "ask":
				req, _ := e["req"].(string)
				answer := "allow"
				if perm == "review" {
					answer = "deny"
				}
				proc.send(cp.ChatReply(st, req, answer, st.Asks[req]))
				delete(st.Asks, req)
			case "error":
				return "", fmt.Errorf("%s: %v", p.Name(), e["text"])
			case "done":
				if s, _ := e["error"].(string); s != "" || e["interrupted"] == true {
					return "", fmt.Errorf("%s: %s", p.Name(), s)
				}
				if strings.TrimSpace(cur.String()) != "" { // streamed, with no complete text after it
					last = cur.String()
					if text != nil {
						text(last)
					}
				}
				result := strings.TrimSpace(last)
				if result == "" {
					return "", fmt.Errorf("%s returned nothing", p.Name())
				}
				return result, nil
			}
		}
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s ran out of time", p.Name())
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s stopped before finishing", p.Name())
}
