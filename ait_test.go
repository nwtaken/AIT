package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary doubles as a fake claude CLI, so the whole handoff — PTY,
// transcript watcher, copy, relaunch — runs without an account or a network.
func TestMain(m *testing.M) {
	if os.Getenv("AIT_FAKE_CLI") == "1" {
		fakeClaude()
		return
	}
	os.Exit(m.Run())
}

// fakeClaude prints what it was started with, and when its config dir holds a
// "limit-me" marker it records a usage-limit error the way claude does.
func fakeClaude() {
	if len(os.Args) > 2 && os.Args[1] == "auth" && os.Args[2] == "login" {
		fmt.Println("Opening browser to sign in…")
		fmt.Println("[2mIf the browser didn't open, visit: https://example.com/oauth?x=1[0m")
		os.WriteFile(filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), ".credentials.json"), []byte("{}"), 0o644)
		fmt.Println("Login successful.")
		return
	}
	for _, a := range os.Args {
		if a == "--input-format" {
			fakeClaudeStream()
			return
		}
	}
	var session, prompt string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session-id", "--resume":
			session = args[i+1]
			i++
		default:
			prompt = args[i]
		}
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(os.Getenv("AIT_FAKE_HOME"), ".claude")
	}
	cwd, _ := os.Getwd()
	fmt.Printf("FAKE dir=%s session=%s prompt=%s\r\n", filepath.Base(filepath.Dir(dir))+"/"+filepath.Base(dir), session, prompt)

	if fileExists(filepath.Join(dir, "limit-me")) {
		time.Sleep(300 * time.Millisecond)
		p := filepath.Join(dir, "projects", projectKey(cwd), session+".jsonl")
		os.MkdirAll(filepath.Dir(p), 0o755)
		f, _ := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		fmt.Fprintf(f, `{"type":"user","timestamp":%q,"message":{"content":"build it"}}`+"\n", now)
		fmt.Fprintf(f, `{"type":"assistant","timestamp":%q,"isApiErrorMessage":true,"error":"rate_limit","message":{"content":[{"type":"text","text":"You've hit your session limit · resets 11pm (Europe/London)"}]}}`+"\n", now)
		f.Close()
	}
	time.Sleep(60 * time.Second)
}

// fakeCLI is a real provider whose command is this test binary.
type fakeCLI struct{ Provider }

func (f fakeCLI) Command() []string { return []string{os.Args[0]} }

// fakeClaudeStream speaks Claude's streaming protocol: it echoes each user
// message as a streamed reply, and "ask" triggers a permission round-trip.
func fakeClaudeStream() {
	w := func(s string) { fmt.Println(s) }
	w(`{"type":"system","subtype":"init","session_id":"s-1","model":"fake","slash_commands":["compact","review"]}`)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct{ Text string } `json:"content"`
			} `json:"message"`
		}
		json.Unmarshal(sc.Bytes(), &m)
		if m.Type != "user" || len(m.Message.Content) == 0 {
			continue
		}
		text := m.Message.Content[len(m.Message.Content)-1].Text
		if text == "hang" { // a turn that is still running
			w(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m0","usage":{"input_tokens":5}}}}`)
			continue
		}
		if text == "review quietly" { // the marker only in the complete message, no deltas
			w(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"mq","usage":{"input_tokens":5}}}}`)
			w(`{"type":"assistant","message":{"id":"mq","content":[{"type":"text","text":"[[AIT_SUPEREVIEW]]"}]}}`)
			w(`{"type":"result","subtype":"success","is_error":false,"duration_ms":12,"total_cost_usd":0}`)
			continue
		}
		if text == "review me" { // asks for a review the way real Claude does: thinking, then the marker
			w(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"mr","usage":{"input_tokens":5}}}}`)
			w(`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}`)
			w(`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`)
			w(`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"text"}}}`)
			w(`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"[[AIT_SUPER"}}}`)
			w(`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"EVIEW]]"}}}`)
			w(`{"type":"stream_event","event":{"type":"content_block_stop","index":1}}`)
			w(`{"type":"result","subtype":"success","is_error":false,"duration_ms":12,"total_cost_usd":0}`)
			continue
		}
		if text == "ask" {
			w(`{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Write","input":{"file_path":"x.txt"},"permission_suggestions":[{"type":"setMode"}]}}`)
			sc.Scan() // the control_response
			var r struct {
				Response struct {
					Response struct{ Behavior string } `json:"response"`
				} `json:"response"`
			}
			json.Unmarshal(sc.Bytes(), &r)
			text = "permission " + r.Response.Response.Behavior
		}
		q, _ := json.Marshal("echo: " + text)
		w(`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","usage":{"input_tokens":5}}}}`)
		w(`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`)
		w(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(q) + `}}}`)
		w(`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`)
		w(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{"five_hour":{"utilization":0.4,"resetsAt":4102444800},"seven_day":{"utilization":0.2,"resetsAt":4103049600}}}}`)
		w(`{"type":"result","subtype":"success","is_error":false,"duration_ms":12,"total_cost_usd":0}`)
	}
}

// fakeChat is the real Claude provider (chat methods included) run as this binary.
type fakeChat struct{ *claude }

func (f fakeChat) Command() []string { return []string{os.Args[0]} }

// Signing a new account in runs the AI's own sign-in against that account's
// folder, passes the sign-in page on, and reports success.
func TestSignInNewAccount(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	t.Setenv("AIT_FAKE_CLI", "1")
	store, _ := newStoreAt(root, home)
	registry["claude"] = fakeChat{registry["claude"].(*claude)}
	app := NewApp(store)
	got := make(chan map[string]any, 8)
	app.emit = func(name string, d ...any) {
		if name == "signin" {
			got <- d[0].(map[string]any)
		}
	}
	acct, err := store.NewAccount("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.SignIn(acct.ID); err != nil {
		t.Fatal(err)
	}
	var url string
	for {
		select {
		case e := <-got:
			if u, ok := e["url"].(string); ok {
				url = u
			}
			if e["done"] == true {
				if e["ok"] != true || url != "https://example.com/oauth?x=1" {
					t.Fatalf("sign-in result %v, url %q", e, url)
				}
				if store.RemoveUnused(acct.ID) == nil {
					t.Fatal("a signed-in account was removed")
				}
				spare, _ := store.NewAccount("claude")
				if err := store.RemoveUnused(spare.ID); err != nil || fileExists(spare.Dir) {
					t.Fatalf("unused account kept: %v", err)
				}
				if _, ok := store.Account(spare.ID); ok {
					t.Fatal("unused account still listed")
				}
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("sign-in never finished")
		}
	}
}

func TestNativeChatRoundTrip(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644)
	b, _ := json.Marshal(Config{Accounts: []Account{{ID: "main", Label: "Main"}}, StartingDir: cwd})
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	t.Setenv("AIT_FAKE_CLI", "1")
	store, _ := newStoreAt(root, home)
	registry["claude"] = fakeChat{registry["claude"].(*claude)}
	app := NewApp(store)
	evs := make(chan Ev, 256)
	app.emit = func(name string, d ...any) {
		if name == "chat:ev" {
			for _, e := range d[1].([]Ev) {
				evs <- e
			}
		}
	}
	info, err := app.Open(OpenRequest{ID: 1, Profile: "claude", Cols: 80, Rows: 24})
	if err != nil || !info.Native {
		t.Fatalf("not a native tab: %+v %v", info, err)
	}
	defer app.Close(1)
	waitEv := func(pred func(Ev) bool) Ev {
		for {
			select {
			case e := <-evs:
				if pred(e) {
					return e
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for an event")
			}
		}
	}
	waitEv(func(e Ev) bool { return e["k"] == "init" })
	app.ChatSend(1, "hello", nil)
	if e := waitEv(func(e Ev) bool { return e["k"] == "delta" }); e["text"] != "echo: hello" {
		t.Fatalf("delta %v", e)
	}
	waitEv(func(e Ev) bool { return e["k"] == "done" })
	if q, ok := store.Quota("main"); !ok || q.Five != 0.4 {
		t.Errorf("quota not recorded: %+v", q)
	}

	app.ChatSend(1, "ask", nil)
	ask := waitEv(func(e Ev) bool { return e["k"] == "ask" })
	if ask["tool"] != "Write" || ask["always"] != true {
		t.Fatalf("ask %v", ask)
	}
	app.ChatAnswer(1, ask["req"].(string), "always")
	if e := waitEv(func(e Ev) bool { return e["k"] == "delta" }); e["text"] != "echo: permission allow" {
		t.Fatalf("after answer: %v", e)
	}
}

func TestHandoffContinuesOnNextAccount(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	mainDir := filepath.Join(home, ".claude")
	second := filepath.Join(root, "profiles", "acct-02")
	for _, d := range []string{mainDir, second} {
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, ".credentials.json"), []byte("{}"), 0o644)
	}
	os.WriteFile(filepath.Join(mainDir, "limit-me"), nil, 0o644)
	os.WriteFile(filepath.Join(mainDir, "CLAUDE.md"), []byte("shared brief"), 0o644)
	cfg := Config{
		Accounts:    []Account{{ID: "main", Label: "Main"}, {ID: "acct-02", Label: "Account 2", Provider: "claude", Dir: second}},
		StartingDir: cwd,
	}
	b, _ := json.Marshal(cfg)
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)

	t.Setenv("AIT_FAKE_CLI", "1")
	t.Setenv("AIT_FAKE_HOME", home)
	store, err := newStoreAt(root, home)
	if err != nil {
		t.Fatal(err)
	}
	registry["claude"] = fakeCLI{registry["claude"]}
	app := NewApp(store)

	var mu sync.Mutex
	var out strings.Builder
	switched := make(chan string, 4)
	app.emit = func(event string, data ...any) {
		switch event {
		case "pty:out":
			raw, _ := base64.StdEncoding.DecodeString(data[1].(string))
			mu.Lock()
			out.Write(raw)
			mu.Unlock()
		case "tab:account":
			switched <- data[1].(string)
		}
	}

	info, err := app.Open(OpenRequest{ID: 1, Profile: "claude", Cols: 120, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(1)
	if info.Account != "Main" {
		t.Fatalf("started on %q, want Main", info.Account)
	}

	deadline := time.After(8 * time.Second)
	for done := false; !done; {
		select {
		case label := <-switched:
			done = label == "Account 2"
		case <-deadline:
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("no handoff to Account 2; output so far:\n%s", out.String())
		}
	}

	if st := store.State("main"); st.LimitedUntil <= time.Now().Unix() || !strings.Contains(st.Reason, "session limit") {
		t.Errorf("main not marked limited: %+v", st)
	}

	tab := app.tab(1)
	tab.mu.Lock()
	session := tab.session
	tab.mu.Unlock()
	id := strings.TrimSuffix(filepath.Base(session), ".jsonl")
	if !strings.HasPrefix(session, second) || !fileExists(session) {
		t.Errorf("transcript not carried into the next account: %s", session)
	}
	if b, _ := os.ReadFile(filepath.Join(second, "CLAUDE.md")); string(b) != "shared brief" {
		t.Errorf("secondary account did not get the shared CLAUDE.md")
	}

	want := fmt.Sprintf("session=%s prompt=continue", id)
	until := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		s := out.String()
		mu.Unlock()
		if strings.Contains(s, "acct-02") && strings.Contains(s, want) {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("resumed process never said %q; output:\n%s", want, s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPTYRunsAndExits(t *testing.T) {
	p, err := StartPTY([]string{"cmd.exe", "/c", "echo ait-ok"}, "", os.Environ(), 80, 24, true)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string)
	go func() {
		var all []byte
		buf := make([]byte, 4096)
		for {
			n, err := p.Read(buf)
			all = append(all, buf[:n]...)
			if err != nil {
				got <- string(all)
				return
			}
		}
	}()
	code, err := p.Wait()
	p.Close()
	if err != nil || code != 0 {
		t.Fatalf("exit %d, %v", code, err)
	}
	select {
	case s := <-got:
		if !strings.Contains(s, "ait-ok") {
			t.Fatalf("output %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader never finished after Close")
	}
}

func TestResetTime(t *testing.T) {
	london, _ := time.LoadLocation("Europe/London")
	now := time.Date(2026, 10, 1, 18, 0, 0, 0, london)
	cases := []struct {
		text string
		want time.Time
	}{
		{"You've hit your session limit · resets 11pm (Europe/London)", time.Date(2026, 10, 1, 23, 1, 0, 0, london)},
		{"You've hit your session limit · resets 12:30am (Europe/London)", time.Date(2026, 10, 2, 0, 31, 0, 0, london)},
		{"You've hit your session limit · resets 11:10am (Europe/London)", time.Date(2026, 10, 2, 11, 11, 0, 0, london)},
		{"You've hit your weekly limit · resets Oct 3, 5pm (Europe/London)", time.Date(2026, 10, 3, 17, 1, 0, 0, london)},
		{"You've hit your weekly limit · resets Oct 3 at 5pm (Europe/London)", time.Date(2026, 10, 3, 17, 1, 0, 0, london)},
	}
	for _, c := range cases {
		if got := resetTime(c.text, now); !got.Equal(c.want) {
			t.Errorf("%q\n got %v\nwant %v", c.text, got.In(london), c.want)
		}
	}
	if got := resetTime("something odd", now); got.Sub(now) != 30*time.Minute {
		t.Errorf("unparseable text should cool down 30m, got %v", got.Sub(now))
	}
}

// An error already in a resumed transcript must not trigger a second handoff.
func TestScanIgnoresOldErrors(t *testing.T) {
	providers(t.TempDir())
	scanTranscript := func(p string, off int64, since time.Time) (*limitHit, int64) {
		return provider("claude").Scan(p, off, since)
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	old := `{"timestamp":"2026-09-01T10:00:00Z","isApiErrorMessage":true,"error":"rate_limit","message":{"content":[{"type":"text","text":"You've hit your session limit · resets 11pm"}]}}` + "\n"
	os.WriteFile(p, []byte(old), 0o644)
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	hit, off := scanTranscript(p, 0, since)
	if hit != nil || off != int64(len(old)) {
		t.Fatalf("old error triggered: %+v off=%d", hit, off)
	}
	fresh := strings.Replace(old, "2026-09-01", "2026-10-01", 1)
	fresh = strings.Replace(fresh, "10:00:00Z", "12:00:00Z", 1)
	signed := `{"timestamp":"2026-10-01T12:00:00Z","isApiErrorMessage":true,"error":"authentication_failed","message":{"content":[{"type":"text","text":"Not logged in · Please run /login"}]}}` + "\n"
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(fresh + `{"partial":`) // a half-written line must wait
	f.Close()
	hit, off2 := scanTranscript(p, off, since)
	if hit == nil || hit.SignedOut || off2 != off+int64(len(fresh)) {
		t.Fatalf("fresh limit missed: %+v off=%d", hit, off2)
	}
	os.WriteFile(p, []byte(signed), 0o644)
	if hit, _ := scanTranscript(p, 0, since); hit == nil || !hit.SignedOut {
		t.Fatalf("signed-out not detected: %+v", hit)
	}
}

func TestPickWrapsAndSkips(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	var accts []Account
	for _, id := range []string{"a", "b", "c"} {
		d := filepath.Join(root, id)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, ".credentials.json"), nil, 0o644)
		accts = append(accts, Account{ID: id, Label: id, Provider: "claude", Dir: d})
	}
	b, _ := json.Marshal(Config{Accounts: accts})
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	s, _ := newStoreAt(root, home)
	now := time.Now()
	s.MarkLimited("c", now.Add(time.Hour), "")
	if a, _ := s.Pick("claude", "b", now); a.ID != "a" {
		t.Errorf("after b with c limited: got %s, want a (wrap)", a.ID)
	}
	s.MarkLimited("a", now.Add(time.Hour), "")
	if a, _ := s.Pick("claude", "b", now); a.ID != "b" {
		t.Errorf("only b free: got %s", a.ID)
	}
	s.MarkSignedOut("b")
	if _, ok := s.Pick("claude", "b", now); ok {
		t.Error("nothing should be available")
	}
	if a, at, _ := s.EarliestReset("claude"); a.ID != "c" && a.ID != "a" || at.IsZero() {
		t.Errorf("earliest reset: %s %v", a.ID, at)
	}
}

func TestProjectKey(t *testing.T) {
	if got := projectKey(`C:\Users\someone\project`); got != "C--Users-someone-project" {
		t.Errorf("got %s", got)
	}
}

// Codex reports quota as numbers; a window at 100% is a hit with an exact reset.
func TestCodexScanReadsQuota(t *testing.T) {
	providers(t.TempDir())
	p := filepath.Join(t.TempDir(), "rollout-2026-10-01T12-00-00-01a0c9af-0893-79d1-a6d6-8a5a872b27ad.jsonl")
	ok := `{"timestamp":"2026-10-01T12:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":67.0,"window_minutes":300,"resets_at":1790093876},"secondary":{"used_percent":26.0,"window_minutes":10080,"resets_at":1790527721},"rate_limit_reached_type":null}}}` + "\n"
	full := strings.Replace(ok, `"used_percent":67.0`, `"used_percent":100.0`, 1)
	os.WriteFile(p, []byte(ok), 0o644)
	cx := provider("codex")
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if hit, _ := cx.Scan(p, 0, since); hit != nil {
		t.Fatalf("67%% is not a limit: %+v", hit)
	}
	os.WriteFile(p, []byte(full), 0o644)
	hit, _ := cx.Scan(p, 0, since)
	if hit == nil || hit.Until.Unix() != 1790093876+60 {
		t.Fatalf("100%% window not detected with its reset: %+v", hit)
	}
	if id := cx.SessionID(p); id != "01a0c9af-0893-79d1-a6d6-8a5a872b27ad" {
		t.Errorf("session id %q", id)
	}
	if got := cx.Args(Launch{ResumeID: "X", Prompt: "continue"}); strings.Join(got, " ") != "resume X continue" {
		t.Errorf("codex resume args: %v", got)
	}
}

// Moving a Codex conversation keeps its dated folder, which is where codex looks.
func TestCodexCarryKeepsDateFolders(t *testing.T) {
	providers(t.TempDir())
	from, to := t.TempDir(), t.TempDir()
	src := filepath.Join(from, "sessions", "2026", "10", "01", "rollout-x-01a0c9af-0893-79d1-a6d6-8a5a872b27ad.jsonl")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, []byte("{}\n"), 0o644)
	dst, err := provider("codex").Carry(src, to, "")
	want := filepath.Join(to, "sessions", "2026", "10", "01", filepath.Base(src))
	if err != nil || dst != want || !fileExists(dst) {
		t.Fatalf("carried to %s (%v), want %s", dst, err, want)
	}
}

// The trust prompt is answered one key at a time from the redrawn screen —
// Down and Enter sent 100ms apart were measured to lose the Enter.
func TestClaudeTrustAnswer(t *testing.T) {
	c := &claude{}
	prompt := "Accessing workspace Is this a project you trust ❯ No, exit Yes, I trust this folder Enter to confirm"
	if k, done := c.TrustAnswer(prompt, prompt); k != "\x1b[B" || done {
		t.Errorf("No selected: got %q %v, want Down", k, done)
	}
	if k, done := c.TrustAnswer(prompt, "No, exit ❯ Yes, I trust this folder"); k != "\r" || !done {
		t.Errorf("Yes selected: got %q %v, want Enter", k, done)
	}
	if k, _ := c.TrustAnswer(prompt, ""); k != "" {
		t.Errorf("no redraw yet: got %q, want nothing", k)
	}
	if k, _ := c.TrustAnswer("just a normal screen ❯ No", "❯ No"); k != "" {
		t.Errorf("no prompt: got %q", k)
	}
}

// A prewarmed agent is handed to the first matching tab with what it drew.
func TestStandbyIsAdopted(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644)
	b, _ := json.Marshal(Config{Accounts: []Account{{ID: "main", Label: "Main"}}, StartingDir: cwd})
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	t.Setenv("AIT_FAKE_CLI", "1")
	t.Setenv("AIT_FAKE_HOME", home)
	store, _ := newStoreAt(root, home)
	registry["claude"] = fakeCLI{registry["claude"]}
	app := NewApp(store)
	app.emit = func(string, ...any) {}

	app.prepareStandby()
	if app.standby == nil {
		t.Fatal("no standby started")
	}
	// Let it draw: wait for output rather than a fixed time (on a busy machine
	// a fixed 500ms was flaky).
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		st := app.standby
		st.backMu.Lock()
		n := len(st.backlog)
		st.backMu.Unlock()
		if n > 0 && strings.Contains(string(st.backlog), "FAKE dir=") {
			break
		}
	}
	info, err := app.Open(OpenRequest{ID: 7, Profile: "claude", Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close(7)
	if app.standby != nil || app.tab(7) == nil {
		t.Fatal("standby not adopted into tab 7")
	}
	if !strings.Contains(string(info.Backlog), "FAKE dir=") {
		t.Fatalf("backlog missing what the agent drew: %q", info.Backlog)
	}
}

// Data moves from the working name's folder once, with account paths rewritten.
func TestMigrateFromBaton(t *testing.T) {
	base := t.TempDir()
	old, root := filepath.Join(base, "Baton"), filepath.Join(base, "AIT")
	os.MkdirAll(filepath.Join(old, "profiles", "claude-02"), 0o755)
	b, _ := json.Marshal(Config{Accounts: []Account{{ID: "claude-02", Dir: filepath.Join(old, "profiles", "claude-02")}}})
	os.WriteFile(filepath.Join(old, "config.json"), b, 0o644)
	migrate(old, root)
	if fileExists(old) || !fileExists(filepath.Join(root, "profiles", "claude-02")) {
		t.Fatal("folder not moved")
	}
	var c Config
	b, _ = os.ReadFile(filepath.Join(root, "config.json"))
	json.Unmarshal(b, &c)
	if want := filepath.Join(root, "profiles", "claude-02"); c.Accounts[0].Dir != want {
		t.Fatalf("dir %q, want %q", c.Accounts[0].Dir, want)
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{{"1.0.1", "1.0.0", true}, {"1.2.10", "1.2.9", true}, {"1.0.0", "1.0.0", false}, {"0.9", "1.0.0", false}, {"2", "1.9.9", true}}
	for _, c := range cases {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// Shared memory: a project with no memory folder is linked to the shared
// one; a project that already has memories is never touched.
func TestShareMemoryLinksOnlyNewFolders(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	providers(home)
	s, _ := newStoreAt(root, home)
	shared := s.ensureMemory()
	os.WriteFile(filepath.Join(shared, "fact.md"), []byte("shared fact"), 0o644)
	cl := provider("claude")
	cl.ShareMemory(cl.DefaultHome(), `C:\work\new`, shared)
	got, err := os.ReadFile(filepath.Join(cl.DefaultHome(), "projects", projectKey(`C:\work\new`), "memory", "fact.md"))
	if err != nil || string(got) != "shared fact" {
		t.Fatalf("new project not linked to shared memory: %v %q", err, got)
	}
	own := filepath.Join(cl.DefaultHome(), "projects", projectKey(`C:\work\old`), "memory")
	os.MkdirAll(own, 0o755)
	os.WriteFile(filepath.Join(own, "mine.md"), []byte("its own"), 0o644)
	cl.ShareMemory(cl.DefaultHome(), `C:\work\old`, shared)
	if b, _ := os.ReadFile(filepath.Join(own, "mine.md")); string(b) != "its own" || fileExists(filepath.Join(own, "fact.md")) {
		t.Fatal("an existing memory folder was replaced")
	}
	if text := memoryRules(shared); !strings.Contains(text, shared) || !strings.Contains(text, "MEMORY.md") {
		t.Fatal("instructions do not point at the shared memory")
	}
}

// The updater against a fake GitHub: a newer release is offered, a good
// download is accepted, and a tampered or foreign download is refused.
func TestUpdaterVerifiesDownloads(t *testing.T) {
	installer := []byte("pretend installer")
	sum := sha256.Sum256(installer)
	good := hex.EncodeToString(sum[:])
	var checksum, assetHost, srv0 string
	repoName := UpdateRepo
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","body":"notes","html_url":"%[3]s/%[2]s/releases/tag/v9.9.9","assets":[
				{"name":"AIT-setup.exe","browser_download_url":"%[1]s/%[2]s/releases/download/v9.9.9/AIT-setup.exe","size":17},
				{"name":"AIT-setup.exe.sha256","browser_download_url":"%[1]s/%[2]s/releases/download/v9.9.9/AIT-setup.exe.sha256"}]}`, assetHost, repoName, srv0)
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			fmt.Fprint(w, checksum)
		case strings.HasSuffix(r.URL.Path, ".exe"):
			w.Write(installer)
		}
	}))
	defer srv.Close()
	oldAPI, oldDL := apiBase, downloadBase
	defer func() { apiBase, downloadBase = oldAPI, oldDL }()
	apiBase, downloadBase, assetHost, srv0 = srv.URL, srv.URL, srv.URL, srv.URL

	s, _ := newStoreAt(t.TempDir(), t.TempDir())
	a := NewApp(s)
	u, err := a.CheckUpdate()
	if err != nil || !u.Available || u.Latest != "9.9.9" {
		t.Fatalf("newer release not offered: %+v %v", u, err)
	}
	checksum = good
	path, err := a.prepareUpdate()
	if err != nil {
		t.Fatalf("good download refused: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(installer) {
		t.Fatal("wrong file written")
	}

	a.prepared = ""
	repoName = "renamed-owner/AIT" // the repository was renamed: still its own releases
	if _, err := a.prepareUpdate(); err != nil {
		t.Fatalf("download refused after a rename: %v", err)
	}
	repoName = UpdateRepo

	a.prepared = ""
	checksum = strings.Repeat("0", 64)
	if _, err := a.prepareUpdate(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered download accepted: %v", err)
	}

	a.prepared = ""
	checksum = good
	downloadBase = "https://github.com" // assets now come from somewhere else
	if _, err := a.prepareUpdate(); err == nil || !strings.Contains(err.Error(), "not from AIT's own releases") {
		t.Fatalf("foreign download accepted: %v", err)
	}
}

// The Claude catalog scan finds versioned entries, newest first.
func TestClaudeCatalogScan(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "claude.exe")
	blob := strings.Repeat("x", 9<<20) + // pushes entries across the first chunk boundary
		`{id:"claude-opus-5",family:"opus",display_name:"Opus 5",knowledge_cutoff:"May 2026"}` +
		`{id:"claude-opus-5-5",family:"opus",display_name:"Opus 5.5",knowledge_cutoff:"June 2026"}` +
		`{id:"claude-3-5-haiku",family:"haiku",display_name:"Haiku 3.5"}` +
		`{id:"claude-mythos-5",family:"mythos",display_name:"Mythos 5",knowledge_cutoff:"Jan 2026"}`
	os.WriteFile(exe, []byte(blob), 0o644)
	c := &claude{userHome: dir}
	got := scanFile(c, exe, dir)
	if len(got) != 2 || got[0].Name != "Opus 5.5" || got[1].Name != "Opus 5" || got[0].Family != "Opus" || !got[0].Long {
		t.Fatalf("catalog: %+v", got)
	}
}

// fakeOther is a second chat AI for the cross-AI test: Claude's protocol,
// another name.
type fakeOther struct{ fakeChat }

func (f fakeOther) ID() string   { return "codex" }
func (f fakeOther) Name() string { return "ChatGPT" }

// When every account of an AI is out, the conversation moves to the next AI
// with a handover file it is told to read.
func TestCrossAIHandover(t *testing.T) {
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o644)
	b, _ := json.Marshal(Config{Accounts: []Account{{ID: "main", Label: "Claude 1"}, {ID: "codex-main", Label: "ChatGPT 1", Provider: "codex"}},
		StartingDir: cwd, AIOrder: []string{"claude", "codex"}})
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "config.json"), b, 0o644)
	t.Setenv("AIT_FAKE_CLI", "1")
	store, _ := newStoreAt(root, home)
	cl := registry["claude"].(*claude)
	registry["claude"] = fakeChat{cl}
	registry["codex"] = fakeOther{fakeChat{cl}}
	app := NewApp(store)
	evs := make(chan Ev, 512)
	switched := make(chan string, 2)
	app.emit = func(name string, d ...any) {
		switch name {
		case "chat:ev":
			for _, e := range d[1].([]Ev) {
				evs <- e
			}
		case "tab:provider":
			switched <- d[1].(string)
		}
	}
	if _, err := app.Open(OpenRequest{ID: 1, Profile: "claude", Cols: 80, Rows: 24}); err != nil {
		t.Fatal(err)
	}
	defer app.Close(1)
	// A conversation worth handing over.
	tab := app.tab(1)
	transcript := filepath.Join(home, ".claude", "projects", projectKey(cwd), "conv.jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o755)
	os.WriteFile(transcript, []byte(`{"type":"user","message":{"content":"add a login page"}}`+"\n"+
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"Started on login.tsx"},{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"login.tsx"}}]}}`+"\n"), 0o644)
	tab.mu.Lock()
	tab.session = transcript
	gen := tab.gen.Load()
	tab.mu.Unlock()

	// The account menu lists every AI's accounts, in the order they take over.
	if list := app.Accounts(1); len(list) != 2 || list[0].ID != "main" || !list[0].Current || list[1].Provider != "codex" {
		t.Fatalf("account list %+v", list)
	}

	app.handoff(tab, gen, &limitHit{Text: "You've hit your session limit · resets 11pm", Until: time.Now().Add(time.Hour)})

	select {
	case to := <-switched:
		if to != "codex" {
			t.Fatalf("moved to %q", to)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no cross-AI handover")
	}
	files, _ := filepath.Glob(filepath.Join(root, "handovers", "*.md"))
	if len(files) != 1 {
		t.Fatalf("handover files: %v", files)
	}
	md, _ := os.ReadFile(files[0])
	if !strings.Contains(string(md), "add a login page") || !strings.Contains(string(md), "Created login.tsx") {
		t.Fatalf("handover file missing the conversation:\n%s", md)
	}
	reading, ready := false, false
	for end := time.After(5 * time.Second); ; {
		select {
		case e := <-evs:
			if e["k"] == "handover" {
				if e["state"] == "reading" {
					reading = true
				}
				if e["state"] == "complete" {
					ready = reading
				}
			}
			text, _ := e["text"].(string)
			if strings.Contains(text, "preparation turn only") {
				t.Fatal("preparation output leaked into the chat")
			}
			if e["k"] != "delta" || !strings.Contains(text, handoverContinue) {
				continue
			}
			if !ready {
				t.Fatal("continued before reading completed")
			}
			return
		case <-end:
			t.Fatal("the next AI never continued after reading the handover")
		}
	}
}

// Coming back to the window re-checks for updates only when the last check is old.
func TestUpdateNudge(t *testing.T) {
	s, _ := newStoreAt(t.TempDir(), t.TempDir())
	a := NewApp(s)
	a.UpdateNudge()
	if len(a.checkNow) != 0 {
		t.Fatal("nudged before the first check")
	}
	a.lastCheck.Store(time.Now().Unix())
	a.UpdateNudge()
	if len(a.checkNow) != 0 {
		t.Fatal("nudged right after a check")
	}
	a.lastCheck.Store(time.Now().Add(-nudgeAfter - time.Minute).Unix())
	a.UpdateNudge()
	a.UpdateNudge() // a second focus doesn't queue a second check
	if len(a.checkNow) != 1 {
		t.Fatalf("queued %d checks", len(a.checkNow))
	}
}
