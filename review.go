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
		"Do not edit files. Give the primary AI a concise actionable review with evidence.\n\n" +
		"<conversation>\n" + history + "\n</conversation>\n\n<git-diff>\n" + string(diff) + "\n</git-diff>"
}

func (a *App) finishReview(t *Tab, gen int64, p Provider, acct Account, prompt string) {
	feedback, err := a.runReviewer(p, acct, t.cwd, prompt, func(s string) { a.emit("review:step", t.id, s) })
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
		return
	}
	a.emit("review:done", t.id, p.Name(), feedback)
}

// The review process has its own login, read-only policy, and deadline.
func (a *App) runReviewer(p Provider, acct Account, cwd, prompt string, step func(string)) (string, error) {
	cp := chatOf(p)
	if cp == nil || p.Command() == nil {
		return "", errors.New("the reviewing AI is unavailable")
	}
	l := Launch{}
	if acct.Model != nil && *acct.Model != "" {
		l.Extra = p.ModelArgs(*acct.Model)
	}
	cmdline := append(append([]string{}, p.Command()...), cp.ChatArgs(l, "review")...)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	for _, b := range cp.ChatStart(st, l, "review", cwd) {
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
	// The answer is every text block in order. A block's complete text
	// ("final", after its deltas) fills in for a block that did not stream.
	var answer strings.Builder
	streamed := false // deltas since the last complete block
	add := func(s string) {
		if answer.Len()+len(s) <= 32<<10 {
			answer.WriteString(s)
		}
	}
	for sc.Scan() {
		for _, e := range cp.ChatDecode(sc.Bytes(), st) {
			switch e["k"] {
			case "tool": // what the reviewer is doing, for the review card
				name, _ := e["name"].(string)
				step(toolLine(name, e["input"]))
			case "final":
				if s, _ := e["text"].(string); !streamed && strings.TrimSpace(s) != "" {
					add(s)
				}
				add("\n\n")
				streamed = false
			case "start":
				textBlocks[e["i"]] = e["type"] == "text"
			case "delta":
				if textBlocks[e["i"]] {
					if s, _ := e["text"].(string); s != "" {
						add(s)
						streamed = true
					}
				}
			case "ask":
				req, _ := e["req"].(string)
				proc.send(cp.ChatReply(st, req, "deny", st.Asks[req]))
				delete(st.Asks, req)
			case "error":
				return "", fmt.Errorf("reviewer: %v", e["text"])
			case "done":
				if s, _ := e["error"].(string); s != "" || e["interrupted"] == true {
					return "", fmt.Errorf("reviewer: %s", s)
				}
				feedback := strings.TrimSpace(answer.String())
				if feedback == "" {
					return "", errors.New("the reviewer returned no feedback")
				}
				return feedback, nil
			}
		}
	}
	if ctx.Err() != nil {
		return "", errors.New("review timed out")
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errors.New("the reviewer stopped before completing")
}
