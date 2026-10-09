package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// The AIs' browser is a window of its own, like a live stream: the page the AI
// is on fills the left, and a column on the right is the "live chat", with
// the steps the AI plans and takes and the questions it asks. It is an
// ordinary top-level window (its own taskbar button, any size, any monitor).
// Two WebView2s share it, side by side: the page, which the AI drives through
// its remote-debugging port with Playwright, and AIT's own column. Everything
// that touches a window runs on one UI thread of its own.

const (
	wmUIRun   = 0x8000 + 22 // WM_APP+22: run what is queued for the UI thread
	columnDIP = 340         // width of the right column in DIPs
)

var (
	procIsIconic        = user32.NewProc("IsIconic")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
	procGetMessageW     = user32.NewProc("GetMessageW")
	procTranslateMsg    = user32.NewProc("TranslateMessage")
	procDispatchMsgW    = user32.NewProc("DispatchMessageW")
	procGetClientRect   = user32.NewProc("GetClientRect")
	procSetWindowTextW  = user32.NewProc("SetWindowTextW")
	procMonitorFromWnd  = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW = user32.NewProc("GetMonitorInfoW")
	procExtractIconW    = shell32.NewProc("ExtractIconW")
)

type uiThread struct {
	once sync.Once
	hwnd uintptr
	fns  chan func()
}

var ui = uiThread{fns: make(chan func(), 64)}

// uiStart starts the UI thread: a message loop with a hidden window that runs
// the functions uiWait and uiPost queue.
func uiStart() {
	ui.once.Do(func() {
		ready := make(chan struct{})
		go func() {
			goruntime.LockOSThread()
			const coinitApartmentThreaded = 2
			procCoInitializeEx.Call(0, coinitApartmentThreaded) // WebView2 needs an STA thread
			inst := windows.Handle(0)
			windows.GetModuleHandleEx(0, nil, &inst)
			cls, _ := windows.UTF16PtrFromString("AITUIThread")
			proc := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
				if msg == wmUIRun {
					for {
						select {
						case f := <-ui.fns:
							f()
						default:
							return 0
						}
					}
				}
				r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
				return r
			})
			wc := wndClassExW{WndProc: proc, Instance: uintptr(inst), ClassName: cls}
			wc.Size = uint32(unsafe.Sizeof(wc))
			procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
			hwndMessage := ^uintptr(2) // HWND_MESSAGE (-3): a window that only receives messages
			ui.hwnd, _, _ = procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, uintptr(inst), 0)
			close(ready)
			var msg [48]byte // MSG
			for {
				r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
				if int32(r) <= 0 {
					return
				}
				procTranslateMsg.Call(uintptr(unsafe.Pointer(&msg[0])))
				procDispatchMsgW.Call(uintptr(unsafe.Pointer(&msg[0])))
			}
		}()
		<-ready
	})
}

// uiPost queues f for the UI thread and returns at once.
func uiPost(f func()) {
	uiStart()
	select {
	case ui.fns <- f:
	default:
		go func() { ui.fns <- f }() // a burst: never block a caller
	}
	procPostMessageW.Call(ui.hwnd, wmUIRun, 0, 0)
}

// uiWait runs f on the UI thread and waits for it.
func uiWait(f func()) {
	done := make(chan struct{})
	uiPost(func() { defer close(done); f() })
	<-done
}

type wndClassExW struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	MenuName, ClassName                *uint16
	IconSm                             uintptr
}

type browserView struct {
	key   string
	hwnd  uintptr
	page  *edge.Chromium // what the AI drives
	col   *edge.Chromium // AIT's column on the right
	port  int
	dir   string // the page's user data folder
	temp  bool   // removed when the view closes
	ready chan struct{}

	colLoaded bool // UI thread only
	mu        sync.Mutex
	gone      bool
	url       string
	title     string
}

var (
	viewClass   sync.Once
	viewsByHwnd sync.Map // hwnd -> *browserView
)

// Links that would open a window open in the page instead.
const viewScript = `(() => {
  if (window.top !== window) return;
  window.open = (u) => { if (u) location.href = String(u); return null; };
  document.addEventListener("click", (e) => {
    const a = e.target && e.target.closest && e.target.closest("a[target]");
    if (a && a.href && a.target !== "_self") { e.preventDefault(); location.href = a.href; }
  }, true);
})();`

// show makes both pages visible again. UI thread.
func (v *browserView) show() {
	for _, w := range []*edge.Chromium{v.page, v.col} {
		if w != nil {
			w.Show()
		}
	}
}

// layout puts the page on the left and the column on the right. UI thread.
func (v *browserView) layout() {
	if v.hwnd == 0 || v.page == nil || v.col == nil {
		return
	}
	var r winRect
	procGetClientRect.Call(v.hwnd, uintptr(unsafe.Pointer(&r)))
	dpi, _, _ := procGetDpiForWindow.Call(v.hwnd)
	if dpi == 0 {
		dpi = 96
	}
	w, h := r.Right-r.Left, r.Bottom-r.Top
	cw := int32(columnDIP * int(dpi) / 96)
	if cw > w/2 {
		cw = w / 2
	}
	page := edge.Rect{Left: 0, Top: 0, Right: w - cw, Bottom: h}
	col := edge.Rect{Left: w - cw, Top: 0, Right: w, Bottom: h}
	v.page.ResizeWithBounds(&page)
	v.col.ResizeWithBounds(&col)
}

// workArea is the usable area of the monitor the main window is on.
func workArea(main uintptr) winRect {
	var work winRect
	const spiGetWorkArea, monitorDefaultToNearest = 0x30, 2
	procSystemParametersW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&work)), 0)
	if main != 0 {
		if mon, _, _ := procMonitorFromWnd.Call(main, monitorDefaultToNearest); mon != 0 {
			var mi struct {
				Size          uint32
				Monitor, Work winRect
				Flags         uint32
			}
			mi.Size = uint32(unsafe.Sizeof(mi))
			if ok, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok != 0 {
				work = mi.Work
			}
		}
	}
	return work
}

// createBrowserView makes the window and its two WebView2s on the UI thread
// and shows it without taking the keyboard from what the user is typing.
func (a *App) createBrowserView(l *browserLog, dir string, temp bool) (*browserView, error) {
	v := &browserView{key: l.key, port: l.port, dir: dir, temp: temp, ready: make(chan struct{})}
	var err error
	uiWait(func() {
		inst := windows.Handle(0)
		windows.GetModuleHandleEx(0, nil, &inst)
		cls, _ := windows.UTF16PtrFromString("AITBrowserWindow")
		viewClass.Do(func() {
			exe, _ := os.Executable()
			path, _ := windows.UTF16PtrFromString(exe)
			icon, _, _ := procExtractIconW.Call(uintptr(inst), uintptr(unsafe.Pointer(path)), 0)
			proc := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
				const wmSize, wmClose = 0x0005, 0x0010
				switch msg {
				case wmSize:
					if x, ok := viewsByHwnd.Load(hwnd); ok {
						v := x.(*browserView)
						const sizeMinimized = 1
						if wp != sizeMinimized {
							v.layout()
							v.show() // minimising hides the pages: restoring must show them again
						}
					}
				case wmClose:
					// The AI's browser goes on behind a closed window: it is
					// minimised, and comes back from the chat's browser button.
					const swMinimize = 6
					procShowWindow.Call(hwnd, swMinimize)
					return 0
				}
				r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
				return r
			})
			wc := wndClassExW{WndProc: proc, Instance: uintptr(inst), ClassName: cls, Icon: icon, IconSm: icon}
			wc.Size = uint32(unsafe.Sizeof(wc))
			procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		})
		const wsOverlappedWindow, wsClipChildren = 0x00CF0000, 0x02000000
		const wsExAppWindow = 0x00040000 // its own taskbar button
		main := ownWindow(windowClass)   // 0 in tests
		work := workArea(main)
		ww, wh := min(1500, (work.Right-work.Left)*85/100), min(900, (work.Bottom-work.Top)*85/100)
		x, y := work.Left+(work.Right-work.Left-ww)/2, work.Top+(work.Bottom-work.Top-wh)/2
		title, _ := windows.UTF16PtrFromString(browserTitle(l.ai, ""))
		h, _, _ := procCreateWindowExW.Call(wsExAppWindow, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
			wsOverlappedWindow|wsClipChildren, uintptr(x), uintptr(y), uintptr(ww), uintptr(wh), 0, 0, uintptr(inst), 0)
		if h == 0 {
			err = fmt.Errorf("could not create the browser window")
			return
		}
		v.hwnd = h
		viewsByHwnd.Store(h, v)

		// The page: private, with a debugging port the AI's tools connect to.
		page := edge.NewChromium()
		page.SetErrorCallback(func(e error) { log.Println("browser page:", e) }) // never take AIT down
		page.DataPath = dir
		page.AdditionalBrowserArgs = []string{
			fmt.Sprintf("--remote-debugging-port=%d", v.port),
			// A window that is minimised or behind others must go on rendering for the AI.
			"--disable-features=CalculateNativeWinOcclusion",
			"--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding", "--disable-background-timer-throttling",
		}
		var once sync.Once
		page.NavigationCompletedCallback = func(*edge.ICoreWebView2, *edge.ICoreWebView2NavigationCompletedEventArgs) {
			once.Do(func() { close(v.ready) })
		}
		if !page.Embed(h) {
			err = fmt.Errorf("WebView2 did not start")
			return
		}
		page.SetBackgroundColour(12, 12, 12, 255)
		if s, e := page.GetSettings(); e == nil {
			s.PutAreDevToolsEnabled(false)
			s.PutIsZoomControlEnabled(false)
			s.PutIsStatusBarEnabled(false)
		}
		page.PutIsPasswordAutosaveEnabled(false)
		page.PutIsGeneralAutofillEnabled(false)
		page.Init(viewScript)
		v.page = page

		// AIT's column.
		col := edge.NewChromium()
		col.SetErrorCallback(func(e error) { log.Println("browser column:", e) })
		col.DataPath = filepath.Join(a.store.root, "browser-ui-webview")
		col.MessageCallback = func(msg string, _ *edge.ICoreWebView2, _ *edge.ICoreWebView2WebMessageReceivedEventArgs) {
			a.browserColumnMessage(l, v, msg)
		}
		col.NavigationCompletedCallback = func(*edge.ICoreWebView2, *edge.ICoreWebView2NavigationCompletedEventArgs) {
			col.Hide() // WebView2 can stay blank until hidden and shown once (see tray.go)
			col.Show()
			v.colLoaded = true
			a.browserPush(l)
		}
		if !col.Embed(h) {
			err = fmt.Errorf("WebView2 did not start")
			return
		}
		col.SetBackgroundColour(12, 12, 12, 255)
		if s, e := col.GetSettings(); e == nil {
			s.PutAreDefaultContextMenusEnabled(false)
			s.PutAreDevToolsEnabled(false)
			s.PutIsZoomControlEnabled(false)
			s.PutIsStatusBarEnabled(false)
		}
		v.col = col
		v.layout()
		col.NavigateToString(browserColumnPage())
		page.Navigate("about:blank")

		// A process's first ShowWindow takes its show state from how the process
		// was started (it can be minimised or hidden); the second call is honoured.
		const swShowNoActivate = 4
		procShowWindow.Call(h, swShowNoActivate)
		procShowWindow.Call(h, swShowNoActivate)
		v.layout()
	})
	if err != nil {
		if v.hwnd != 0 {
			a.destroyBrowserView(v)
		}
		return nil, err
	}
	select {
	case <-v.ready:
	case <-time.After(20 * time.Second):
		a.destroyBrowserView(v)
		return nil, fmt.Errorf("the browser did not start in time")
	}
	// The AI connects to the debugging port: wait until it answers.
	for i := 0; i < 50; i++ {
		if res, e := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", v.port)); e == nil {
			res.Body.Close()
			return v, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	a.destroyBrowserView(v)
	return nil, fmt.Errorf("the browser's debugging port did not open")
}

// browserTitle is the window's title (and its taskbar button's).
func browserTitle(ai, page string) string {
	t := "AIT Browser · " + ai
	if page = strings.TrimSpace(page); page != "" && page != "about:blank" {
		t = shorten(page, 60) + " — " + t
	}
	return t
}

// closeController closes a WebView2 controller (the library has no method
// for it): the browser behind it ends once nothing else uses it.
func closeController(c *edge.ICoreWebView2Controller) {
	const closeSlot = 24 // ICoreWebView2Controller's vtable: Close follows NotifyParentWindowPositionChanged
	vtbl := *(**[26]uintptr)(unsafe.Pointer(c))
	syscall.SyscallN(vtbl[closeSlot], uintptr(unsafe.Pointer(c)))
}

func (a *App) destroyBrowserView(v *browserView) {
	v.mu.Lock()
	v.gone = true
	v.mu.Unlock()
	uiWait(func() {
		viewsByHwnd.Delete(v.hwnd)
		for _, w := range []*edge.Chromium{v.page, v.col} {
			if w != nil {
				if c := w.GetController(); c != nil {
					closeController(c)
				}
			}
		}
		if v.hwnd != 0 {
			procDestroyWindow.Call(v.hwnd)
		}
	})
	if v.temp && v.dir != "" {
		go func() { // the browser process releases the folder a moment after the window
			for i := 0; i < 20; i++ {
				if os.RemoveAll(v.dir) == nil {
					return
				}
				time.Sleep(500 * time.Millisecond)
			}
		}()
	}
}

// watchPage reads the page's address and title from the browser itself,
// through its debugging port (never from the page: a page could claim to be
// any site), for as long as the view lives.
func (a *App) watchPage(l *browserLog, v *browserView) {
	for ; ; time.Sleep(600 * time.Millisecond) {
		v.mu.Lock()
		gone := v.gone
		v.mu.Unlock()
		if gone {
			return
		}
		res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", v.port))
		if err != nil {
			continue
		}
		var targets []struct{ Type, URL, Title string }
		json.NewDecoder(res.Body).Decode(&targets)
		res.Body.Close()
		for _, t := range targets {
			if t.Type != "page" {
				continue
			}
			v.mu.Lock()
			changed := v.url != t.URL || v.title != t.Title
			v.url, v.title = t.URL, t.Title
			v.mu.Unlock()
			if changed {
				title := browserTitle(l.ai, t.Title)
				uiPost(func() {
					if v.hwnd != 0 && !v.isGone() {
						p, _ := windows.UTF16PtrFromString(title)
						procSetWindowTextW.Call(v.hwnd, uintptr(unsafe.Pointer(p)))
					}
				})
				a.browserPush(l)
			}
			break
		}
	}
}

func (v *browserView) isGone() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.gone
}

// browserEnsureView starts the chat's browser window on first use.
func (a *App) browserEnsureView(l *browserLog) error {
	l.viewMu.Lock()
	defer l.viewMu.Unlock()
	if l.view != nil {
		return nil
	}
	dir, temp := filepath.Join(a.store.browserDir(), "profile"), false
	if !a.store.Config().BrowserKeep || a.profileBusy() {
		var err error
		if dir, err = os.MkdirTemp(a.browserTmp(), "view-"); err != nil {
			return err
		}
		temp = true
	}
	os.MkdirAll(dir, 0o755)
	v, err := a.createBrowserView(l, dir, temp)
	if err != nil {
		return err
	}
	l.view = v
	a.browserNote("window of %s opened (port %d, %s)", l.key[:min(8, len(l.key))], l.port, map[bool]string{true: "private", false: "kept profile"}[temp])
	a.browserUsed(l)
	go a.watchPage(l, v)
	if l.tab != nil {
		a.emit("browser:open", l.tab.id)
	}
	return nil
}

func (a *App) browserTmp() string {
	d := filepath.Join(a.store.browserDir(), "tmp")
	os.MkdirAll(d, 0o755)
	return d
}

// profileBusy: the kept profile can serve one browser at a time.
func (a *App) profileBusy() bool {
	a.browser.mu.Lock()
	defer a.browser.mu.Unlock()
	for _, l := range a.browser.logs {
		l.viewMu.Lock()
		busy := l.view != nil && !l.view.temp
		l.viewMu.Unlock()
		if busy {
			return true
		}
	}
	return false
}

// freePort picks a port for a browser's debugging endpoint.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// closeBrowser removes a chat's browser (its window and, when private, its data).
func (a *App) closeBrowser(key string) {
	a.browserNote("browser %s removed (its chat was closed, restarted or AIT is quitting)", key[:min(8, len(key))])
	a.browser.mu.Lock()
	l := a.browser.logs[key]
	delete(a.browser.logs, key)
	a.browser.mu.Unlock()
	if l == nil {
		return
	}
	l.viewMu.Lock()
	v := l.view
	l.view = nil
	l.viewMu.Unlock()
	if v != nil {
		a.destroyBrowserView(v)
		if l.tab != nil {
			a.emit("browser:gone", l.tab.id)
		}
	}
}

// closeBrowsers is for when AIT quits.
func (a *App) closeBrowsers() {
	a.browser.mu.Lock()
	keys := make([]string, 0, len(a.browser.logs))
	for k := range a.browser.logs {
		keys = append(keys, k)
	}
	a.browser.mu.Unlock()
	for _, k := range keys {
		a.closeBrowser(k)
	}
}

// ---- the column --------------------------------------------------------------

// browserColumnPage is the column's HTML: AIT's own stylesheets plus the column.
func browserColumnPage() string {
	read := func(name string) string {
		b, _ := assets.ReadFile("frontend/" + name)
		return string(b)
	}
	return strings.Replace(read("browser.html"), "/*AIT_CSS*/", read("style.css")+"\n"+read("chat.css"), 1)
}

// browserStateJSON is what the column shows: the page, the steps and the
// questions waiting for the user.
func (a *App) browserStateJSON(l *browserLog, v *browserView) []byte {
	type ask struct {
		Req    string            `json:"req"`
		Help   bool              `json:"help"`
		Text   string            `json:"text"`
		Always bool              `json:"always"`
		Labels map[string]string `json:"labels,omitempty"`
	}
	a.browser.mu.Lock()
	steps := append([]BrowserStep{}, l.steps...)
	var asks []ask
	for _, req := range sortedAsks(a.browser.asks, l.key) {
		p := a.browser.asks[req]
		x := ask{Req: req, Help: p.help, Text: p.message, Always: !p.help}
		if p.help {
			x.Labels = map[string]string{"allow": "I'm done", "deny": "Can't do it"}
		} else {
			x.Text = p.host
		}
		asks = append(asks, x)
	}
	theme := a.browser.theme
	a.browser.mu.Unlock()
	v.mu.Lock()
	url, title := v.url, v.title
	v.mu.Unlock()
	busy := l.tab != nil && l.tab.working.Load()
	b, _ := json.Marshal(map[string]any{
		"ai": l.ai, "url": url, "title": title, "busy": busy, "steps": steps, "asks": asks,
		"keep": a.store.Config().BrowserKeep, "theme": json.RawMessage(orNull(theme)),
	})
	return b
}

func orNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}

// browserPush sends the column its latest state.
func (a *App) browserPush(l *browserLog) {
	l.viewMu.Lock()
	v := l.view
	l.viewMu.Unlock()
	if v == nil {
		// still starting: the column pushes itself once it has loaded
		return
	}
	a.browserPushView(l, v)
}

func (a *App) browserPushView(l *browserLog, v *browserView) {
	js := "update(" + string(a.browserStateJSON(l, v)) + ")"
	uiPost(func() {
		if v.col != nil && v.colLoaded && !v.isGone() {
			v.col.Eval(js)
		}
	})
}

// browserColumnMessage is what the column sends (UI thread).
func (a *App) browserColumnMessage(l *browserLog, v *browserView, msg string) {
	if what, ok := strings.CutPrefix(msg, "nav:"); ok {
		js := map[string]string{"back": "history.back()", "forward": "history.forward()", "reload": "location.reload()"}[what]
		if js != "" && v.page != nil {
			v.page.Eval(js)
		}
		return
	}
	if rest, ok := strings.CutPrefix(msg, "answer:"); ok {
		if req, d, ok := strings.Cut(rest, "|"); ok {
			if p := a.browserPending(req); p != nil && p.key == l.key {
				go a.browserAnswer(req, d)
			}
		}
	}
}

// BrowserTheme is the main page telling AIT its colours, so the browser
// window looks like AIT.
func (a *App) BrowserTheme(theme string) {
	a.browser.mu.Lock()
	a.browser.theme = theme
	logs := make([]*browserLog, 0, len(a.browser.logs))
	for _, l := range a.browser.logs {
		logs = append(logs, l)
	}
	a.browser.mu.Unlock()
	for _, l := range logs {
		a.browserPush(l)
	}
}

// BrowserShow brings a chat's browser window forward (the chat's globe button).
func (a *App) BrowserShow(tabID int) {
	t := a.tab(tabID)
	if t == nil {
		return
	}
	key, _ := t.browserKey.Load().(string)
	l := a.browserLogFor(key)
	if l == nil {
		return
	}
	l.viewMu.Lock()
	v := l.view
	l.viewMu.Unlock()
	if v == nil {
		return
	}
	uiPost(func() {
		const swRestore, swShow = 9, 5
		if iconic, _, _ := procIsIconic.Call(v.hwnd); iconic != 0 {
			procShowWindow.Call(v.hwnd, swRestore)
		} else {
			procShowWindow.Call(v.hwnd, swShow)
		}
		procSetForegroundWindow.Call(v.hwnd)
	})
}

// browserAttention brings the window back if it was minimised, without
// taking the keyboard, and flashes its taskbar button: the AI is waiting for
// the user.
func (a *App) browserAttention(l *browserLog) {
	l.viewMu.Lock()
	v := l.view
	l.viewMu.Unlock()
	if v == nil {
		return
	}
	uiPost(func() {
		const swShowNoActivate = 4
		procShowWindow.Call(v.hwnd, swShowNoActivate)
		type flashInfo struct {
			Size         uint32
			Wnd          uintptr
			Flags, Count uint32
			Timeout      uint32
		}
		const flashwAll, flashwTimerNoFG = 0x3, 0xC
		f := flashInfo{Wnd: v.hwnd, Flags: flashwAll | flashwTimerNoFG}
		f.Size = uint32(unsafe.Sizeof(f))
		procFlashWindowEx.Call(uintptr(unsafe.Pointer(&f)))
	})
}

// BrowserState is a chat's browser as the column shows it (for tests).
func (a *App) BrowserState(tabID int) map[string]any {
	t := a.tab(tabID)
	if t == nil {
		return nil
	}
	key, _ := t.browserKey.Load().(string)
	l := a.browserLogFor(key)
	if l == nil {
		return nil
	}
	l.viewMu.Lock()
	v := l.view
	l.viewMu.Unlock()
	a.browser.mu.Lock()
	steps := append([]BrowserStep{}, l.steps...)
	a.browser.mu.Unlock()
	out := map[string]any{"steps": steps, "ai": l.ai, "open": v != nil}
	if v != nil {
		v.mu.Lock()
		out["url"], out["title"] = v.url, v.title
		v.mu.Unlock()
	}
	return out
}
