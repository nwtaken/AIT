//go:build manual

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

var procSendMessageW = user32.NewProc("SendMessageW")

// The real surface: AIT creates its WebView2, and an AI's tools (through the
// gate and Playwright, over the debugging port) open a page in it. Creates a
// window for a moment. Run with:
//
//	go test -tags manual -run TestRealBrowserSurface -v
func TestRealBrowserSurface(t *testing.T) {
	appData, _ := os.UserConfigDir()
	src := filepath.Join(appData, "AIT", "browser")
	if !fileExists(filepath.Join(src, "node_modules", "@playwright", "mcp", "cli.js")) {
		t.Skip("the browser is not installed (start AIT once)")
	}
	root := t.TempDir()
	if out, err := exec.Command("robocopy", src, filepath.Join(root, "browser"), "/E", "/NFL", "/NDL", "/NJH", "/NJS", "/XD", "profile", "output", "tmp").CombinedOutput(); err != nil && !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("copy: %v %s", err, out)
	}
	store, _ := newStoreAt(root, t.TempDir())
	app := NewApp(store)
	var mu sync.Mutex
	var events []string
	app.emit = func(name string, d ...any) {
		mu.Lock()
		defer mu.Unlock()
		switch name {
		case "browser:open", "browser:gone":
			events = append(events, name)
		case "chat:ev":
			for _, e := range d[1].([]Ev) {
				if e["k"] == "ask" {
					go app.ChatAnswer(1, e["req"].(string), "allow")
					events = append(events, fmt.Sprint("ask ", e["desc"]))
				}
			}
		}
	}
	tab := &Tab{id: 1, agent: registry["claude"], profile: "claude"}
	tab.adopted.Store(true)
	port, _ := freePort()
	l := &browserLog{key: "k", tab: tab, ai: "Claude", port: port, allowed: map[string]bool{}}
	app.browser.logs = map[string]*browserLog{"k": l}
	app.tabs[1] = tab
	tab.browserKey.Store("k")
	base, err := app.browserBase()
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "browser", "config.json")
	b, _ := json.Marshal(map[string]any{"browser": map[string]any{"browserName": "chromium", "cdpEndpoint": fmt.Sprintf("http://127.0.0.1:%d", port)}, "outputDir": filepath.Join(root, "out")})
	os.WriteFile(cfg, b, 0o644)
	gate, _ := browserAssets.ReadFile("browserassets/gate.cjs")
	gatePath := filepath.Join(root, "browser", "gate.cjs")
	os.WriteFile(gatePath, gate, 0o644)

	cmd := exec.Command(nodeBinary(), gatePath, "--ait", base, "--key", "k", "--cli", store.browserCLI(), "--config", cfg)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); app.closeBrowsers(); time.Sleep(4 * time.Second) }()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	id := 0
	rpc := func(method string, params any) map[string]any {
		id++
		line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		fmt.Fprintln(stdin, string(line))
		done := make(chan map[string]any, 1)
		go func() {
			for sc.Scan() {
				var m map[string]any
				if json.Unmarshal(sc.Bytes(), &m) == nil && m["id"] == float64(id) {
					done <- m
					return
				}
			}
		}()
		select {
		case m := <-done:
			return m
		case <-time.After(40 * time.Second):
			t.Fatalf("%s: no answer", method)
		}
		return nil
	}
	call := func(name string, args map[string]any) (bool, string) {
		t.Logf("-> %s %v", name, args)
		r := rpc("tools/call", map[string]any{"name": name, "arguments": args})["result"].(map[string]any)
		var text string
		for _, c := range r["content"].([]any) {
			if tx, ok := c.(map[string]any)["text"].(string); ok {
				text += tx
			} else {
				text += "[" + fmt.Sprint(c.(map[string]any)["type"]) + "]"
			}
		}
		isErr, _ := r["isError"].(bool)
		return isErr, text
	}
	rpc("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}})
	fmt.Fprintln(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	isErr, text := call("browser_navigate", map[string]any{"url": "https://example.com"})
	if isErr || !strings.Contains(text, "Example Domain") {
		t.Fatalf("navigate: %v %s", isErr, text)
	}
	isErr, text = call("browser_evaluate", map[string]any{"function": "() => document.title + ' | ' + location.href"})
	if isErr || !strings.Contains(text, "Example Domain | https://example.com/") {
		t.Fatalf("evaluate: %v %s", isErr, text)
	}
	time.Sleep(900 * time.Millisecond) // the title follows the page
	l.viewMu.Lock()
	v := l.view
	l.viewMu.Unlock()

	// A window of its own: a taskbar button of its own, no owner, visible, titled by the page.
	const wsExAppWindow, wsExToolWindow, gwOwner = 0x00040000, 0x00000080, 4
	gwlExStyle := int32(-20)
	procGetWindowLong := user32.NewProc("GetWindowLongPtrW")
	procGetWindow := user32.NewProc("GetWindow")
	exStyle, _, _ := procGetWindowLong.Call(v.hwnd, uintptr(int64(gwlExStyle)))
	if exStyle&wsExAppWindow == 0 || exStyle&wsExToolWindow != 0 {
		t.Errorf("not a window with its own taskbar button: ex style %#x", exStyle)
	}
	if owner, _, _ := procGetWindow.Call(v.hwnd, gwOwner); owner != 0 {
		t.Errorf("the window has an owner (%#x): it would hide behind AIT", owner)
	}
	if vis, _, _ := procIsWindowVisible.Call(v.hwnd); vis == 0 {
		t.Error("the window is not visible")
	}
	titleBuf := make([]uint16, 200)
	user32.NewProc("GetWindowTextW").Call(v.hwnd, uintptr(unsafe.Pointer(&titleBuf[0])), 200)
	if got := windows.UTF16ToString(titleBuf); got != "Example Domain — AIT Browser · Claude" {
		t.Errorf("window title %q", got)
	}
	// Page on the left, AIT's column on the right, together filling the window; resizing keeps that.
	bounds := func() (page, col edge.Rect, client winRect) {
		uiWait(func() {
			pb, _ := v.page.GetController().GetBounds()
			cb, _ := v.col.GetController().GetBounds()
			page, col = *pb, *cb
			procGetClientRect.Call(v.hwnd, uintptr(unsafe.Pointer(&client)))
		})
		return
	}
	check := func(when string) {
		page, col, c := bounds()
		w, h := c.Right-c.Left, c.Bottom-c.Top
		dpi, _, _ := procGetDpiForWindow.Call(v.hwnd)
		cw := int32(columnDIP * int(dpi) / 96)
		if page.Left != 0 || page.Top != 0 || page.Bottom != h || page.Right != w-cw || col.Left != w-cw || col.Right != w || col.Top != 0 || col.Bottom != h {
			t.Errorf("%s: page %+v column %+v in a %dx%d window (column should be %d wide)", when, page, col, w, h, cw)
		}
	}
	check("at first")
	const swpNoMove, swpNoZOrder = 0x0002, 0x0004
	procSetWindowPos.Call(v.hwnd, 0, 0, 0, 1000, 640, swpNoMove|swpNoZOrder)
	check("after resizing")
	procShowWindow.Call(v.hwnd, 3) // maximise
	time.Sleep(300 * time.Millisecond)
	check("maximised")
	procShowWindow.Call(v.hwnd, 9) // restore

	// The AI keeps working with the window minimised (what the close button does).
	for _, how := range []uintptr{6, 2, 11} { // SW_MINIMIZE, SW_SHOWMINIMIZED, SW_FORCEMINIMIZE
		r, _, _ := procShowWindow.Call(v.hwnd, how)
		time.Sleep(300 * time.Millisecond)
		ic, _, _ := procIsIconic.Call(v.hwnd)
		vis, _, _ := procIsWindowVisible.Call(v.hwnd)
		var wr winRect
		user32.NewProc("GetWindowRect").Call(v.hwnd, uintptr(unsafe.Pointer(&wr)))
		t.Logf("ShowWindow(%d) -> %d, iconic=%d visible=%d rect=%+v", how, r, ic, vis, wr)
		if ic != 0 {
			break
		}
	}
	if iconic, _, _ := procIsIconic.Call(v.hwnd); iconic == 0 {
		t.Fatal("not minimised")
	}
	isErr, text = call("browser_navigate", map[string]any{"url": "https://example.com/?minimised"})
	if isErr || !strings.Contains(text, "Example Domain") {
		t.Errorf("navigate while minimised: %v %s", isErr, text)
	}
	isErr, text = call("browser_snapshot", map[string]any{})
	if isErr || !strings.Contains(text, "Example Domain") {
		t.Errorf("read the page while minimised: %v %s", isErr, text)
	}
	isErr, text = call("browser_take_screenshot", map[string]any{})
	if isErr {
		t.Errorf("screenshot while minimised: %s", text)
	}
	if iconic, _, _ := procIsIconic.Call(v.hwnd); iconic != 0 {
		t.Error("a screenshot should have brought the minimised window back")
	}
	// The close button minimises rather than ending the AI's browser.
	procShowWindow.Call(v.hwnd, 9)
	procSendMessageW.Call(v.hwnd, 0x0010, 0, 0) // WM_CLOSE
	time.Sleep(300 * time.Millisecond)
	if iconic, _, _ := procIsIconic.Call(v.hwnd); iconic == 0 {
		t.Error("the close button did not minimise")
	}
	if isErr, text = call("browser_evaluate", map[string]any{"function": "() => document.title"}); isErr || !strings.Contains(text, "Example Domain") {
		t.Errorf("the page after the close button: %v %s", isErr, text)
	}
	app.BrowserShow(1)
	time.Sleep(300 * time.Millisecond)
	if iconic, _, _ := procIsIconic.Call(v.hwnd); iconic != 0 {
		t.Error("the chat's button did not bring the window back")
	}

	// The AI is done: browser_close closes the window; the next call opens a new one.
	hwnd := v.hwnd
	if isErr, text = call("browser_close", map[string]any{}); isErr {
		t.Errorf("browser_close: %s", text)
	}
	if isWin, _, _ := user32.NewProc("IsWindow").Call(hwnd); isWin != 0 {
		t.Error("the window is still there after browser_close")
	}
	if st := app.BrowserState(1); st == nil || st["open"] != false {
		t.Errorf("state after closing: %v", st)
	}
	isErr, text = call("browser_navigate", map[string]any{"url": "https://example.com/?again"})
	if isErr || !strings.Contains(text, "Example Domain") {
		t.Fatalf("navigate after closing: %v %s", isErr, text)
	}
	l.viewMu.Lock()
	v2 := l.view
	l.viewMu.Unlock()
	if v2 == nil || v2.hwnd == hwnd || v2.hwnd == 0 {
		t.Errorf("no new window after closing: %+v", v2)
	}
	// Left open and unused: the idle check closes it (the clock is moved, not waited for).
	tab.working.Store(false)
	if app.browserShouldReap(l, time.Now()) {
		t.Error("closed a browser that was just used")
	}
	if !app.browserShouldReap(l, time.Now().Add(browserIdleAfter+time.Minute)) {
		t.Error("an unused browser is not closed")
	}
	tab.working.Store(true)
	if app.browserShouldReap(l, time.Now().Add(browserIdleAfter+time.Minute)) {
		t.Error("closed a browser while the AI is working")
	}
	tab.working.Store(false)
	app.browserCloseView(l)
	if st := app.BrowserState(1); st["open"] != false {
		t.Errorf("not closed: %v", st)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("events: %v", events)
	joined := strings.Join(events, "\n")
	for _, want := range []string{"browser:open", "ask open example.com"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q in events", want)
		}
	}
}
