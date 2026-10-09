package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Native chat. Instead of showing an agent's own text UI in a terminal, AIT
// runs it in its machine-readable streaming mode and draws the conversation
// itself. A provider opts in by implementing ChatProvider; one that doesn't
// keeps the terminal view, so every agent works either way.
//
// The page only ever sees normalised events (Ev), whatever the agent:
//
//	init    session, model, commands          the agent is up
//	status  s                                 "requesting" while waiting on the model
//	msg     id, ctx                           an assistant message begins (ctx = context tokens)
//	start   i, type, id?, name?               content block i begins: text | thinking | tool
//	delta   i, text                           streamed text for block i
//	tool    id, name, input                   a tool call, complete
//	result  id, ok, text                      that tool's result
//	ask     req, tool, desc, input            permission request; answer with ChatAnswer
//	done    ms, cost, error?                  the turn ended
//	quota   five, fiveReset, week, weekReset  usage windows, 0..1
//	user    text                              a user message (history and replays)
//	text    text                              a whole assistant text block (history)
//	error   text
type Ev map[string]any

type ChatProvider interface {
	// ChatArgs is the command line (after the binary) for streaming mode.
	// perm is AIT's permission setting: "ask" | "edits" | "never".
	ChatArgs(l Launch, perm string) []string
	// ChatStart is what to send as soon as the process is up (a protocol
	// handshake, say). Most agents need nothing: return nil.
	ChatStart(st *ChatState, l Launch, perm, cwd string) [][]byte
	// ChatUser encodes one user message for the agent's stdin. It may
	// return nil and send later via st.Send (e.g. once a handshake is done).
	ChatUser(st *ChatState, text string, images []Image) []byte
	// ChatDecode turns one line of the agent's stdout into events. It may
	// answer the agent through st.Send.
	ChatDecode(line []byte, st *ChatState) []Ev
	// ChatReply answers a permission request: decision is allow | always | deny.
	ChatReply(st *ChatState, req, decision string, ask json.RawMessage) []byte
	// ChatControl encodes a control message: "interrupt", or "model:<id>".
	ChatControl(st *ChatState, what string) []byte
	// ChatHistory replays a stored transcript as events.
	ChatHistory(path string) []Ev
}

// ChatState is per-process state a provider may keep.
type ChatState struct {
	Asks map[string]json.RawMessage // pending permission requests by id
	Send func([]byte)               // write a line to the agent
	Data map[string]any             // provider-specific (thread ids, queues …)
	mu   sync.Mutex
	seq  int
}

// Next returns a fresh request id.
func (s *ChatState) Next() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// Get / Put guard Data.
func (s *ChatState) Get(k string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Data[k]
}

func (s *ChatState) Put(k string, v any) {
	s.mu.Lock()
	s.Data[k] = v
	s.mu.Unlock()
}

type Image struct {
	Media string // image/png …
	Data  []byte
}

// chatProc is one agent process in streaming mode.
type chatProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	kill  func()
	wmu   sync.Mutex
}

func (c *chatProc) send(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.stdin.Write(b)
	return err
}

// startChat launches the agent in streaming mode. Caller holds t.mu.
func (a *App) startChat(t *Tab, cp ChatProvider, cmdline []string, env []string, l Launch, perm string) error {
	cmd := exec.Command(cmdline[0], cmdline[1:]...)
	cmd.Dir = t.cwd
	cmd.Env = env
	hideConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 8 << 10}
	if err := cmd.Start(); err != nil {
		return err
	}
	cp2 := &chatProc{cmd: cmd, stdin: stdin, kill: killTree(cmd.Process.Pid)}
	t.chat = cp2
	st := &ChatState{Asks: map[string]json.RawMessage{}, Data: map[string]any{}, Send: func(b []byte) { cp2.send(b) }}
	t.chatState = st
	for _, line := range cp.ChatStart(st, l, perm, t.cwd) {
		cp2.send(line)
	}
	gen := t.gen.Load()
	go a.chatPump(t, cp, cp2, st, stdout, &stderr, gen)
	return nil
}

func (a *App) chatPump(t *Tab, cp ChatProvider, proc *chatProc, st *ChatState, stdout io.Reader, stderr *strings.Builder, gen int64) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 64<<20) // agent turns get large
	textBlocks := map[any]bool{}
	var turnText strings.Builder
	lastText := "" // the latest complete text block of the turn's last message
	planText := "" // what the AI said since its last tool call: the plan for the next one
	browserCalls := map[any]bool{}
	for sc.Scan() {
		if t.gen.Load() != gen {
			continue // replaced; drain quietly
		}
		evs := cp.ChatDecode(sc.Bytes(), st)
		if len(evs) == 0 {
			continue
		}
		requestReview := false
		for _, e := range evs {
			switch e["k"] {
			case "msg":
				if !t.working.Swap(true) {
					a.trayTooltip()
					a.browserTouch(t)
				}
				turnText.Reset()
				lastText = ""
			case "final":
				lastText, _ = e["text"].(string)
				planText = lastText
			case "tool":
				if name, _ := e["name"].(string); strings.HasPrefix(name, browserPrefix) {
					a.browserStep(t, planText, name, e["input"])
					browserCalls[e["id"]] = true
				}
				planText = ""
			case "result":
				if browserCalls[e["id"]] && e["ok"] == false {
					a.browserFailed(t, fmt.Sprint(e["text"]))
				}
			case "start":
				textBlocks[e["i"]] = e["type"] == "text"
			case "delta":
				if textBlocks[e["i"]] && turnText.Len() < 1024 {
					if s, _ := e["text"].(string); s != "" {
						turnText.WriteString(s)
					}
				}
			case "done":
				planText = ""
				t.working.Store(false)
				a.browserTouch(t)
				a.trayTooltip()
				text := strings.TrimSpace(turnText.String())
				if text == "" {
					text = strings.TrimSpace(lastText)
				}
				requestReview = text == reviewMarker || strings.TrimSpace(lastText) == reviewMarker
				turnText.Reset()
				if !requestReview {
					errText, _ := e["error"].(string)
					a.notifyTab(t, " finished", " stopped", text, errText)
				}
			case "ask":
				desc, _ := e["desc"].(string)
				if desc == "" {
					desc, _ = e["tool"].(string)
				}
				a.notifyTab(t, " needs your permission", "", desc, "")
			case "quota":
				a.store.SetQuota(t.acctID(), e)
			case "init":
				if id, _ := e["session"].(string); id != "" {
					t.mu.Lock()
					acct, _ := a.store.Account(t.acct)
					t.sessionID = id
					if p := t.agent.SessionFile(a.store.Home(acct), t.cwd, id); p != "" {
						a.claim(t, p)
					}
					t.mu.Unlock()
				}
			}
		}
		evs = a.handoverEvents(t, cp, proc, st, gen, evs)
		a.chatOut(t, evs)
		if requestReview && t.gen.Load() == gen {
			go func() {
				err := errors.New("it is switched off (/supereview)")
				if a.store.Config().Review {
					// One review per message from the user: an AI asking again
					// is told to finish instead of spending usage in a loop.
					if t.reviewed.Swap(true) {
						err = errors.New("this request was already reviewed; finish it now")
					} else {
						err = a.Review(t.id)
					}
				}
				// Never leave the AI waiting on a review that will not come.
				if err != nil {
					a.emit("review:error", t.id, err.Error())
					a.chatSend(t.id, "The requested independent review is unavailable: "+err.Error()+". Continue the user's request without it.", nil)
				}
			}()
		}
	}
	proc.cmd.Wait()
	if t.gen.Load() == gen {
		t.mu.Lock()
		if t.reading && t.gen.Load() == gen {
			t.reading, t.handover = false, ""
			a.chatOut(t, []Ev{{"k": "handover", "state": "failed"}})
		}
		t.mu.Unlock()
		msg := strings.TrimSpace(stderr.String())
		a.chatOut(t, []Ev{{"k": "exit", "text": lastLines(msg, 6)}})
		if !t.adopted.Load() {
			a.standbyExited(t)
		}
	}
}

// chatOut sends events to the page, or keeps them while the tab is a standby.
func (a *App) chatOut(t *Tab, evs []Ev) {
	t.backMu.Lock()
	defer t.backMu.Unlock()
	if t.adopted.Load() {
		a.emit("chat:ev", t.id, evs)
		return
	}
	t.evBacklog = append(t.evBacklog, evs...)
}

func (t *Tab) acctID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.acct
}

// ---- bindings ---------------------------------------------------------------

// ChatSend sends a message. files are paths: images go as images, anything
// else is named in the text so the agent can open it.
func (a *App) ChatSend(id int, text string, files []string) error {
	if t := a.tab(id); t != nil {
		t.reviewed.Store(false) // a new message from the user may be reviewed again
	}
	return a.chatSend(id, text, files)
}

// chatSend sends a message to the tab's AI; AIT's own notes to it use this
// directly, so they do not count as a new request from the user.
func (a *App) chatSend(id int, text string, files []string) error {
	t := a.tab(id)
	if t == nil {
		return fmt.Errorf("no tab")
	}
	t.mu.Lock()
	if t.reading {
		t.mu.Unlock()
		return fmt.Errorf("wait for the conversation handover to finish")
	}
	proc, cp := t.chat, chatOf(t.agent)
	t.mu.Unlock()
	if proc == nil || cp == nil {
		return fmt.Errorf("the agent is not running")
	}
	var imgs []Image
	var others []string
	for _, f := range files {
		if m := imageMedia(f); m != "" {
			if b, err := os.ReadFile(f); err == nil && len(b) < 5<<20 {
				imgs = append(imgs, Image{Media: m, Data: b})
				continue
			}
		}
		others = append(others, f)
	}
	for _, f := range others {
		text += "\n" + quotePath(f)
	}
	a.store.Trust(t.cwd) // sending in a folder is consent to work in it
	if b := cp.ChatUser(t.chatState, strings.TrimSpace(text), imgs); b != nil {
		t.working.Store(true)
		return proc.send(b)
	}
	return nil
}

// ChatAnswer answers a permission card: allow | always | deny.
func (a *App) ChatAnswer(id int, req, decision string) error {
	if strings.HasPrefix(req, askPrefix) { // a site the AI wants to open in its browser
		return a.browserAnswer(req, decision)
	}
	t := a.tab(id)
	if t == nil {
		return fmt.Errorf("no tab")
	}
	t.mu.Lock()
	proc, cp, st := t.chat, chatOf(t.agent), t.chatState
	t.mu.Unlock()
	if proc == nil || cp == nil || st == nil {
		return fmt.Errorf("the agent is not running")
	}
	ask := st.Asks[req]
	delete(st.Asks, req)
	return proc.send(cp.ChatReply(st, req, decision, ask))
}

// ChatControl: "interrupt", or "model:<id>".
func (a *App) ChatControl(id int, what string) error {
	t := a.tab(id)
	if t == nil {
		return fmt.Errorf("no tab")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reading {
		if what == "interrupt" {
			return nil
		}
		return fmt.Errorf("wait for the conversation handover to finish")
	}
	proc, cp, st := t.chat, chatOf(t.agent), t.chatState
	if proc == nil || cp == nil {
		return fmt.Errorf("the agent is not running")
	}
	if m, ok := strings.CutPrefix(what, "model:"); ok {
		if err := a.store.SetAccountModel(t.acct, m); err != nil {
			return err
		}
		t.model = m // a handoff relaunches on the same model
	}
	if e, ok := strings.CutPrefix(what, "effort:"); ok {
		t.effort = e // and the same effort
	}
	if b := cp.ChatControl(st, what); b != nil {
		return proc.send(b)
	}
	return nil
}

// ChatHistory replays the tab's conversation so far (after a resume or an
// account switch the page shows it again from the transcript).
func (a *App) ChatHistory(id int) []Ev {
	t := a.tab(id)
	if t == nil {
		return []Ev{}
	}
	t.mu.Lock()
	cp, path := chatOf(t.agent), t.session
	t.mu.Unlock()
	if cp == nil || path == "" || !fileExists(path) {
		return []Ev{}
	}
	return visibleHistory(cp.ChatHistory(path))
}

// summaryKeep is how many events after the summary a big chat shows.
const summaryKeep = 300

// ChatSummary is ChatHistory for a very big chat: the summary the AI wrote
// when it last compacted the conversation, then only the latest events. The
// AI itself still resumes the whole conversation.
func (a *App) ChatSummary(id int) []Ev {
	t := a.tab(id)
	if t == nil {
		return []Ev{}
	}
	t.mu.Lock()
	cp, path := chatOf(t.agent), t.session
	t.mu.Unlock()
	if tail, ok := cp.(interface{ ChatHistoryTail(string) []Ev }); ok && path != "" && fileExists(path) {
		return summarize(visibleHistory(tail.ChatHistoryTail(path)))
	}
	return summarize(a.ChatHistory(id))
}

func summarize(evs []Ev) []Ev {
	start := 0
	var out []Ev
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i]["k"] == "summary" {
			start = i + 1
			out = append(out, evs[i])
			break
		}
	}
	rest := evs[start:]
	if len(rest) > summaryKeep {
		cut := len(rest) - summaryKeep
		for i := cut; i < len(rest); i++ { // start at a message of the user's when there is one
			if rest[i]["k"] == "user" {
				cut = i
				break
			}
		}
		out = append(out, Ev{"k": "note", "text": fmt.Sprintf("%d earlier items hidden to keep AIT fast; the AI still has them", cut)})
		rest = rest[cut:]
	}
	return append(out, rest...)
}

// TrustFolder records that the user lets agents work in this tab's folder.
func (a *App) TrustFolder(id int) {
	if t := a.tab(id); t != nil {
		a.store.Trust(t.cwd)
	}
}

// ImageData returns a small image as a data URL, for attachment previews.
func (a *App) ImageData(path string) string {
	m := imageMedia(path)
	if m == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 8<<20 {
		return ""
	}
	return "data:" + m + ";base64," + base64.StdEncoding.EncodeToString(b)
}

// ChatNew starts a fresh conversation in the same tab (/clear).
func (a *App) ChatNew(id int) error {
	t := a.tab(id)
	if t == nil {
		return fmt.Errorf("no tab")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	acct, ok := a.store.Account(t.acct)
	if !ok {
		return fmt.Errorf("no account")
	}
	t.session = ""
	return a.relaunch(t, acct, "")
}

// ChatFolder moves the tab to another working folder, starting fresh there.
func (a *App) ChatFolder(id int) (string, error) {
	t := a.tab(id)
	if t == nil {
		return "", fmt.Errorf("no tab")
	}
	dir, err := pickFolder(a.ctx, t.cwd)
	if err != nil || dir == "" {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	acct, ok := a.store.Account(t.acct)
	if !ok {
		return "", fmt.Errorf("no account")
	}
	t.cwd, t.session, t.trusted = dir, "", a.store.Trusted(dir)
	return maskUser(dir), a.relaunch(t, acct, "")
}

// ---- helpers --------------------------------------------------------------------

func chatOf(p Provider) ChatProvider {
	cp, _ := p.(ChatProvider)
	return cp
}

func imageMedia(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

func quotePath(p string) string {
	if strings.ContainsAny(p, " &()") {
		return `"` + p + `"`
	}
	return p
}

func lastLines(s string, n int) string {
	l := strings.Split(s, "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	if l.n <= 0 {
		return len(b), nil
	}
	k := min(len(b), l.n)
	l.n -= k
	l.w.Write(b[:k])
	return len(b), nil
}

// ---- quota (per account, from the agent's own usage reports) -----------------

type Quota struct {
	Five      float64 `json:"five"`
	FiveReset int64   `json:"fiveReset"`
	Week      float64 `json:"week"`
	WeekReset int64   `json:"weekReset"`
	At        int64   `json:"at"`
}

func (s *Store) SetQuota(id string, e Ev) {
	q := Quota{At: time.Now().Unix()}
	q.Five, _ = e["five"].(float64)
	q.Week, _ = e["week"].(float64)
	q.FiveReset, _ = e["fiveReset"].(int64)
	q.WeekReset, _ = e["weekReset"].(int64)
	s.mu.Lock()
	if s.quota == nil {
		s.quota = map[string]Quota{}
	}
	s.quota[id] = q
	b, _ := json.MarshalIndent(s.quota, "", "  ")
	s.mu.Unlock()
	os.WriteFile(s.quotaPath(), b, 0o644) // so usage shows at once after a restart
}

func (s *Store) quotaPath() string { return filepath.Join(s.root, "usage.json") }

func (s *Store) Quota(id string) (Quota, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.quota[id]
	now := time.Now().Unix()
	if ok && q.FiveReset > 0 && q.FiveReset < now {
		q.Five = 0 // that window has reset since the reading
	}
	if ok && q.WeekReset > 0 && q.WeekReset < now {
		q.Week = 0
	}
	return q, ok
}

// unixTime turns a transcript's RFC 3339 timestamp into unix seconds (0 when
// missing), so a reopened chat shows when each message was sent.
func unixTime(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}
