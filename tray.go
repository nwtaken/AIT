package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/energye/systray"
	"github.com/wailsapp/go-webview2/pkg/edge"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

// The tray icon and its status panel. The panel is a window of its own (a
// small WebView2 next to the tray), so the main window never changes when it
// opens. Its page is drawn with AIT's own stylesheets, and the main page
// sends it the live status and theme while it is open (TrayState).
// Everything here runs on the tray's thread, which owns the panel.

//go:embed build/windows/icon.ico
var trayIcon []byte

const windowClass = "AITMainWindow"

const panelW = 320 // panel width in DIPs; the height follows its content

const wmPanelUpdate = 0x8000 + 21 // WM_APP+21: new status for the panel

var (
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procShowWindow          = user32.NewProc("ShowWindow")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetDpiForWindow     = user32.NewProc("GetDpiForWindow")
	procSystemParametersW   = user32.NewProc("SystemParametersInfoW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procCoInitializeEx      = windows.NewLazySystemDLL("ole32.dll").NewProc("CoInitializeEx")
)

type winRect struct{ Left, Top, Right, Bottom int32 }

type trayPanel struct {
	hwnd     uintptr
	web      *edge.Chromium
	height   int // content height in DIPs, as the page last measured it
	hiddenAt time.Time

	mu    sync.Mutex
	state string // latest status from the main page, JSON
}

// trayGone closes once the icon has been removed from the tray.
var trayGone = make(chan struct{})

// stopTray removes the icon before AIT exits; an exit that beats it leaves a
// dead icon in the tray until the mouse passes over it.
func stopTray() {
	systray.Quit()
	select {
	case <-trayGone:
	case <-time.After(2 * time.Second):
	}
}

func (a *App) startTray() {
	go func() {
		goruntime.LockOSThread()
		const coinitApartmentThreaded = 2
		procCoInitializeEx.Call(0, coinitApartmentThreaded) // WebView2 needs an STA thread
		systray.Run(func() {
			systray.SetIcon(trayIcon)
			systray.SetTooltip("AIT")
			// Either button toggles the panel, as tray widgets do.
			systray.SetOnClick(func(systray.IMenu) { a.togglePanel() })
			systray.SetOnRClick(func(systray.IMenu) { a.togglePanel() })
		}, func() { close(trayGone) })
	}()
}

// togglePanel runs on the tray thread (from the icon's click).
func (a *App) togglePanel() {
	p := &a.panel
	if p.hwnd == 0 && !a.createPanel() {
		return
	}
	if vis, _, _ := procIsWindowVisible.Call(p.hwnd); vis != 0 {
		a.hidePanel()
		return
	}
	// Clicking the icon takes focus from an open panel, which hides it just
	// before this click arrives; that click means "close", not "reopen".
	if time.Since(p.hiddenAt) < 400*time.Millisecond {
		return
	}
	a.emit("tray:open", true)
	a.placePanel()
	const swShow = 5
	procShowWindow.Call(p.hwnd, swShow)
	p.web.Hide()
	p.web.Show()
	procSetForegroundWindow.Call(p.hwnd)
	p.web.Focus()
	a.updatePanel()
}

func (a *App) hidePanel() {
	p := &a.panel
	if p.hwnd == 0 {
		return
	}
	if vis, _, _ := procIsWindowVisible.Call(p.hwnd); vis == 0 {
		return
	}
	const swHide = 0
	procShowWindow.Call(p.hwnd, swHide)
	p.hiddenAt = time.Now()
	a.emit("tray:open", false)
}

// placePanel puts the panel above the tray, sized to its content.
func (a *App) placePanel() {
	p := &a.panel
	var work winRect
	const spiGetWorkArea = 0x30
	procSystemParametersW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&work)), 0)
	dpi, _, _ := procGetDpiForWindow.Call(p.hwnd)
	if dpi == 0 {
		dpi = 96
	}
	h := max(p.height, 120)
	w, ht := int32(panelW*int(dpi)/96), int32(h*int(dpi)/96)
	gap := int32(12 * int(dpi) / 96)
	ht = min(ht, work.Bottom-work.Top-2*gap)
	hwndTopmost := ^uintptr(0) // HWND_TOPMOST (-1)
	const swpNoActivate = 0x0010
	procSetWindowPos.Call(p.hwnd, hwndTopmost, uintptr(work.Right-w-gap), uintptr(work.Bottom-ht-gap), uintptr(w), uintptr(ht), swpNoActivate)
	p.web.Resize()
}

// TrayState is the main page's latest status for the panel.
func (a *App) TrayState(state string) {
	p := &a.panel
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
	if p.hwnd != 0 {
		procPostMessageW.Call(p.hwnd, wmPanelUpdate, 0, 0)
	}
}

func (a *App) updatePanel() {
	p := &a.panel
	p.mu.Lock()
	s := p.state
	p.mu.Unlock()
	if s != "" && p.web != nil {
		p.web.Eval("update(" + s + ")")
	}
}

// createPanel makes the panel window and its WebView2 (once, on first use).
func (a *App) createPanel() bool {
	p := &a.panel
	inst := windows.Handle(0)
	windows.GetModuleHandleEx(0, nil, &inst)
	cls, _ := windows.UTF16PtrFromString("AITTrayPanel")
	proc := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		const wmActivate, wmSize, wmClose, waInactive = 0x0006, 0x0005, 0x0010, 0
		switch msg {
		case wmActivate:
			if wp&0xffff == waInactive {
				a.hidePanel()
			}
		case wmSize:
			if p.web != nil {
				p.web.Resize()
			}
		case wmClose:
			a.hidePanel()
			return 0
		case wmPanelUpdate:
			a.updatePanel()
			return 0
		}
		r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	})
	type wndClassEx struct {
		Size, Style                        uint32
		WndProc                            uintptr
		ClsExtra, WndExtra                 int32
		Instance, Icon, Cursor, Background uintptr
		MenuName, ClassName                *uint16
		IconSm                             uintptr
	}
	wc := wndClassEx{WndProc: proc, Instance: uintptr(inst), ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	const wsPopup, wsClipChildren = 0x80000000, 0x02000000
	const wsExToolWindow, wsExTopmost = 0x00000080, 0x00000008
	title, _ := windows.UTF16PtrFromString("AIT")
	h, _, _ := procCreateWindowExW.Call(wsExToolWindow|wsExTopmost, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		wsPopup|wsClipChildren, 0, 0, 300, 300, 0, 0, uintptr(inst), 0)
	if h == 0 {
		return false
	}
	p.hwnd = h

	web := edge.NewChromium()
	web.SetErrorCallback(func(err error) { log.Println("tray panel:", err) }) // never take AIT down
	web.DataPath = filepath.Join(a.store.root, "panel-webview")
	web.MessageCallback = func(msg string, _ *edge.ICoreWebView2, _ *edge.ICoreWebView2WebMessageReceivedEventArgs) {
		a.panelMessage(msg)
	}
	// WebView2 can stay blank until it is hidden and shown once (Wails does
	// the same: WebView2Feedback#1077).
	web.NavigationCompletedCallback = func(*edge.ICoreWebView2, *edge.ICoreWebView2NavigationCompletedEventArgs) {
		web.Hide()
		web.Show()
		a.updatePanel()
	}
	web.Embed(h)
	web.Resize()
	web.SetBackgroundColour(12, 12, 12, 255)
	if s, err := web.GetSettings(); err == nil {
		s.PutAreDefaultContextMenusEnabled(false)
		s.PutAreDevToolsEnabled(false)
		s.PutIsZoomControlEnabled(false)
		s.PutIsStatusBarEnabled(false)
	}
	p.web = web
	web.NavigateToString(panelPage())
	return true
}

// panelMessage handles what the panel page sends (tray thread).
func (a *App) panelMessage(msg string) {
	if h, ok := strings.CutPrefix(msg, "h:"); ok {
		var n int
		if json.Unmarshal([]byte(h), &n) == nil && n != a.panel.height {
			a.panel.height = n
			if vis, _, _ := procIsWindowVisible.Call(a.panel.hwnd); vis != 0 {
				a.placePanel()
			}
		}
		return
	}
	// A message or an answer typed in the panel goes to that tab's chat, as
	// if typed in AIT; the panel stays open to show the reply.
	if rest, ok := strings.CutPrefix(msg, "send:"); ok {
		var m struct {
			Tab  int    `json:"tab"`
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(rest), &m) == nil && strings.TrimSpace(m.Text) != "" {
			a.emit("tray:send", m.Tab, m.Text)
		}
		return
	}
	if rest, ok := strings.CutPrefix(msg, "ask:"); ok {
		var m struct {
			Tab      int    `json:"tab"`
			Req      string `json:"req"`
			Decision string `json:"d"`
		}
		if json.Unmarshal([]byte(rest), &m) == nil {
			a.emit("tray:answer", m.Tab, m.Req, m.Decision)
		}
		return
	}
	a.hidePanel()
	switch msg {
	case "open":
		a.ShowApp()
	case "hide":
		a.HideToTray()
	case "settings":
		a.ShowApp()
		a.emit("tray:settings")
	case "quit":
		a.ShowApp()
		a.emit("app:close-requested") // the usual confirmation
	}
}

// HideToTray removes the main window entirely; agents keep working.
func (a *App) HideToTray() {
	runtime.WindowHide(a.ctx)
}

// ShowApp brings the main window back, from the tray or minimised.
func (a *App) ShowApp() {
	runtime.WindowShow(a.ctx)
}

// panelPage is the panel's HTML: AIT's own stylesheets plus the panel.
func panelPage() string {
	read := func(name string) string {
		b, _ := assets.ReadFile("frontend/" + name)
		return string(b)
	}
	page := read("panel.html")
	page = strings.Replace(page, "/*AIT_CSS*/", read("style.css")+"\n"+read("chat.css"), 1)
	return page
}
