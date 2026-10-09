package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// The AIs' browser surface. A WebView2 of its own (private: a throwaway
// profile, or the kept one) sits in a borderless window owned by AIT's main
// window, which AIT keeps exactly over the browser pane in the page: the pane
// (toolbar, steps, questions) is AIT's own HTML, the surface is native. The AI
// drives the surface through its remote-debugging port with Playwright.
// Everything that touches a window runs on one UI thread of its own.

const wmUIRun = 0x8000 + 22 // WM_APP+22: run what is queued for the UI thread

var (
	procClientToScreen = user32.NewProc("ClientToScreen")
	procIsIconic       = user32.NewProc("IsIconic")
	procDestroyWindow  = user32.NewProc("DestroyWindow")
	procGetMessageW    = user32.NewProc("GetMessageW")
	procTranslateMsg   = user32.NewProc("TranslateMessage")
	procDispatchMsgW   = user32.NewProc("DispatchMessageW")
)

type uiThread struct {
	once sync.Once
	hwnd uintptr
	fns  chan func()
}

var ui = uiThread{fns: make(chan func(), 64)}

// uiStart starts the UI thread: a message loop with a hidden window that runs
// the functions uiDo queues.
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

// uiWait runs f on the UI thread and waits for it.
func uiWait(f func()) {
	uiStart()
	done := make(chan struct{})
	ui.fns <- func() { defer close(done); f() }
	procPostMessageW.Call(ui.hwnd, wmUIRun, 0, 0)
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

// paneRect is where the page placeholder is in the main window: CSS pixels
// from the top-left of its client area.
type paneRect struct{ X, Y, W, H float64 }

type browserView struct {
	key   string
	hwnd  uintptr
	web   *edge.Chromium
	port  int
	dir   string // the user data folder
	temp  bool   // removed when the view closes
	ready chan struct{}

	mu         sync.Mutex
	gone       bool
	url, title string
	rect       paneRect
	want       bool // the pane is showing it
	at         [4]int32
}

var (
	viewClass   sync.Once
	viewsByHwnd sync.Map // hwnd -> *browserView
)

const parkedAt = -32000 // off-screen but still "visible", so the page keeps rendering

// Links that would open a window open here instead. (The page's address and
// title are read from the browser itself, never from the page: a page could
// claim to be any site.)
const viewScript = `(() => {
  if (window.top !== window) return;
  window.open = (u) => { if (u) location.href = String(u); return null; };
  document.addEventListener("click", (e) => {
    const a = e.target && e.target.closest && e.target.closest("a[target]");
    if (a && a.href && a.target !== "_self") { e.preventDefault(); location.href = a.href; }
  }, true);
})();`

// createBrowserView makes the window and its WebView2 on the UI thread.
func (a *App) createBrowserView(l *browserLog, dir string, temp bool) (*browserView, error) {
	v := &browserView{key: l.key, port: l.port, dir: dir, temp: temp, ready: make(chan struct{})}
	var err error
	uiWait(func() {
		inst := windows.Handle(0)
		windows.GetModuleHandleEx(0, nil, &inst)
		cls, _ := windows.UTF16PtrFromString("AITBrowserView")
		viewClass.Do(func() {
			proc := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
				const wmSize, wmClose = 0x0005, 0x0010
				switch msg {
				case wmSize:
					if x, ok := viewsByHwnd.Load(hwnd); ok && x.(*browserView).web != nil {
						x.(*browserView).web.Resize()
					}
				case wmClose:
					return 0 // the pane owns it
				}
				r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
				return r
			})
			wc := wndClassExW{WndProc: proc, Instance: uintptr(inst), ClassName: cls}
			wc.Size = uint32(unsafe.Sizeof(wc))
			procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		})
		const wsPopup, wsClipChildren, wsExToolWindow = 0x80000000, 0x02000000, 0x00000080
		title, _ := windows.UTF16PtrFromString("AIT browser")
		owner := ownWindow(windowClass) // the main window; 0 in tests
		h, _, _ := procCreateWindowExW.Call(wsExToolWindow, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
			wsPopup|wsClipChildren, uintptr(parkedAt&0xffffffff), uintptr(parkedAt&0xffffffff), 900, 700, owner, 0, uintptr(inst), 0)
		if h == 0 {
			err = fmt.Errorf("could not create the browser window")
			return
		}
		v.hwnd = h
		viewsByHwnd.Store(h, v)
		const swShowNoActivate = 4
		procShowWindow.Call(h, swShowNoActivate)

		web := edge.NewChromium()
		web.SetErrorCallback(func(e error) { log.Println("browser view:", e) }) // never take AIT down
		web.DataPath = dir
		web.AdditionalBrowserArgs = []string{
			fmt.Sprintf("--remote-debugging-port=%d", v.port),
			// A window kept off-screen or behind AIT must go on rendering for the AI.
			"--disable-features=CalculateNativeWinOcclusion",
			"--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding", "--disable-background-timer-throttling",
		}
		var once sync.Once
		web.NavigationCompletedCallback = func(*edge.ICoreWebView2, *edge.ICoreWebView2NavigationCompletedEventArgs) {
			once.Do(func() { close(v.ready) })
		}
		if !web.Embed(h) {
			err = fmt.Errorf("WebView2 did not start")
			return
		}
		web.Resize()
		web.SetBackgroundColour(12, 12, 12, 255)
		if s, e := web.GetSettings(); e == nil {
			s.PutAreDevToolsEnabled(false)
			s.PutIsZoomControlEnabled(false)
			s.PutIsStatusBarEnabled(false)
		}
		web.PutIsPasswordAutosaveEnabled(false)
		web.PutIsGeneralAutofillEnabled(false)
		web.Init(viewScript)
		v.web = web
		web.Navigate("about:blank")
	})
	if err != nil {
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
		if v.web != nil {
			if c := v.web.GetController(); c != nil {
				closeController(c)
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

// watchPage reads the surface's address and title from the browser itself,
// through its debugging port, for as long as the view lives.
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
			if changed && l.tab != nil {
				a.emit("browser:page", l.tab.id, t.URL, t.Title)
			}
			break
		}
	}
}

// browserEnsureView starts the chat's browser surface on first use.
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
		if l.view != nil && !l.view.temp {
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

// ---- what the page (the pane) asks for ---------------------------------------

func (a *App) browserOf(tabID int) *browserView {
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
	defer l.viewMu.Unlock()
	return l.view
}

// BrowserBounds tells AIT where the pane's page area is (CSS pixels in the
// main window) and whether it is showing; the surface follows it.
func (a *App) BrowserBounds(tabID int, x, y, w, h float64, visible bool) {
	v := a.browserOf(tabID)
	if v == nil {
		return
	}
	v.mu.Lock()
	v.rect, v.want = paneRect{x, y, w, h}, visible
	v.mu.Unlock()
	a.placeBrowser(v)
}

// placeBrowser puts the window over the page area, or parks it off-screen.
func (a *App) placeBrowser(v *browserView) {
	v.mu.Lock()
	r, want := v.rect, v.want
	v.mu.Unlock()
	main := ownWindow(windowClass)
	x, y, w, h := int32(parkedAt), int32(parkedAt), int32(900), int32(700)
	if iconic, _, _ := procIsIconic.Call(main); want && main != 0 && iconic == 0 && r.W >= 1 && r.H >= 1 {
		var pt struct{ X, Y int32 }
		procClientToScreen.Call(main, uintptr(unsafe.Pointer(&pt)))
		dpi, _, _ := procGetDpiForWindow.Call(main)
		if dpi == 0 {
			dpi = 96
		}
		s := float64(dpi) / 96
		x, y = pt.X+int32(math.Round(r.X*s)), pt.Y+int32(math.Round(r.Y*s))
		w, h = int32(math.Round(r.W*s)), int32(math.Round(r.H*s))
	}
	v.mu.Lock()
	same := v.at == [4]int32{x, y, w, h}
	v.at = [4]int32{x, y, w, h}
	v.mu.Unlock()
	if same {
		return
	}
	const swpNoZOrder, swpNoActivate = 0x0004, 0x0010
	procSetWindowPos.Call(v.hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), swpNoZOrder|swpNoActivate)
}

// browserFollow keeps the surfaces over their panes while the main window is
// moved, minimised or restored.
func (a *App) browserFollow() {
	for range time.Tick(60 * time.Millisecond) {
		a.browser.mu.Lock()
		var views []*browserView
		for _, l := range a.browser.logs {
			l.viewMu.Lock()
			if l.view != nil {
				views = append(views, l.view)
			}
			l.viewMu.Unlock()
		}
		a.browser.mu.Unlock()
		for _, v := range views {
			v.mu.Lock()
			want := v.want
			v.mu.Unlock()
			if want {
				a.placeBrowser(v)
			}
		}
	}
}

// BrowserNav is the pane's back / forward / reload buttons.
func (a *App) BrowserNav(tabID int, action string) {
	v := a.browserOf(tabID)
	if v == nil || v.web == nil {
		return
	}
	js := map[string]string{"back": "history.back()", "forward": "history.forward()", "reload": "location.reload()", "stop": "window.stop()"}[action]
	if js == "" {
		return
	}
	uiWait(func() { v.web.Eval(js) })
}

// BrowserState is what the pane shows when it opens late: the steps so far
// and the page.
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
