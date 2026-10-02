package main

import (
	_ "embed"
	"os"
	goruntime "runtime"
	"sync"
	"unsafe"

	"github.com/energye/systray"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"
)

// The tray icon. Left-click opens a small status panel above the tray (the
// main window in its mini layout); right-click has Open, Hide and Quit.
// Hidden to the tray, AIT keeps running its agents with no window at all.

//go:embed build/windows/icon.ico
var trayIcon []byte

const windowClass = "AITMainWindow"

const miniW, miniH = 300, 400 // panel size in DIPs

var (
	procFindWindowExW       = user32.NewProc("FindWindowExW")
	procGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowPlacement  = user32.NewProc("GetWindowPlacement")
	procSetWindowPlacement  = user32.NewProc("SetWindowPlacement")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetDpiForWindow     = user32.NewProc("GetDpiForWindow")
	procSystemParametersW   = user32.NewProc("SystemParametersInfoW")
)

type winRect struct{ Left, Top, Right, Bottom int32 }

type windowPlacement struct {
	Length, Flags, ShowCmd uint32
	MinPos, MaxPos         [2]int32
	Normal                 winRect
}

type trayState struct {
	mu     sync.Mutex
	mini   bool
	saved  windowPlacement
	hidden bool // the window was hidden to the tray before the panel opened
}

// mainWindow finds AIT's own window.
func mainWindow() uintptr {
	cls, _ := windows.UTF16PtrFromString(windowClass)
	var h uintptr
	for {
		h, _, _ = procFindWindowExW.Call(0, h, uintptr(unsafe.Pointer(cls)), 0)
		if h == 0 {
			return 0
		}
		var pid uint32
		procGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
		if int(pid) == os.Getpid() {
			return h
		}
	}
}

func (a *App) startTray() {
	go func() {
		goruntime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(trayIcon)
			systray.SetTooltip("AIT")
			systray.SetOnClick(func(systray.IMenu) { a.TrayPanel() })
			systray.AddMenuItem("Open AIT", "").Click(a.ShowApp)
			systray.AddMenuItem("Hide to tray", "").Click(a.HideToTray)
			systray.AddSeparator()
			systray.AddMenuItem("Quit AIT", "").Click(func() {
				a.ShowApp()
				a.emit("app:close-requested") // the usual confirmation
			})
		}, nil)
	}()
}

// HideToTray removes the window entirely; agents keep working.
func (a *App) HideToTray() {
	a.endMini(true)
	runtime.WindowHide(a.ctx)
	a.tray.mu.Lock()
	a.tray.hidden = true
	a.tray.mu.Unlock()
}

// ShowApp brings the full window back.
func (a *App) ShowApp() {
	a.endMini(false)
	a.tray.mu.Lock()
	a.tray.hidden = false
	a.tray.mu.Unlock()
	runtime.WindowShow(a.ctx)
}

// TrayPanel opens the small status panel above the tray, or closes it.
func (a *App) TrayPanel() {
	h := mainWindow()
	if h == 0 {
		return
	}
	a.tray.mu.Lock()
	if a.tray.mini {
		a.tray.mu.Unlock()
		a.TrayDismiss()
		return
	}
	a.tray.mini = true
	a.tray.saved = windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{}))}
	procGetWindowPlacement.Call(h, uintptr(unsafe.Pointer(&a.tray.saved)))
	vis, _, _ := procIsWindowVisible.Call(h)
	if vis == 0 {
		a.tray.hidden = true
	}
	a.tray.mu.Unlock()

	a.emit("tray:mini", true)
	runtime.WindowSetMinSize(a.ctx, 1, 1)
	const swShowNormal = 1
	procShowWindow.Call(h, swShowNormal)
	a.placePanel(h, miniH)
	procSetForegroundWindow.Call(h)
}

// TrayFit sizes the panel to its content (height in DIPs), keeping it
// anchored above the tray.
func (a *App) TrayFit(height int) {
	a.tray.mu.Lock()
	mini := a.tray.mini
	a.tray.mu.Unlock()
	h := mainWindow()
	if !mini || h == 0 || height < 100 {
		return
	}
	a.placePanel(h, height)
}

func (a *App) placePanel(h uintptr, height int) {
	var work winRect
	const spiGetWorkArea = 0x30
	procSystemParametersW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&work)), 0)
	dpi, _, _ := procGetDpiForWindow.Call(h)
	if dpi == 0 {
		dpi = 96
	}
	w, ht := int32(miniW*int(dpi)/96), int32(height*int(dpi)/96)
	gap := int32(12 * int(dpi) / 96)
	ht = min(ht, work.Bottom-work.Top-2*gap)
	const swpShow = 0x0040
	hwndTopmost := ^uintptr(0) // HWND_TOPMOST (-1)
	procSetWindowPos.Call(h, hwndTopmost, uintptr(work.Right-w-gap), uintptr(work.Bottom-ht-gap), uintptr(w), uintptr(ht), swpShow)
}

// TrayDismiss closes the panel, putting the window back as it was.
func (a *App) TrayDismiss() { a.endMini(true) }

// endMini leaves the panel layout; restore puts the window back in the
// state it had before (hidden, minimised or open where it was).
func (a *App) endMini(restore bool) {
	a.tray.mu.Lock()
	if !a.tray.mini {
		a.tray.mu.Unlock()
		return
	}
	a.tray.mini = false
	saved, hidden := a.tray.saved, a.tray.hidden
	a.tray.mu.Unlock()

	h := mainWindow()
	a.emit("tray:mini", false)
	runtime.WindowSetMinSize(a.ctx, 420, 260)
	runtime.WindowSetAlwaysOnTop(a.ctx, a.store.Config().AlwaysOnTop)
	if h != 0 {
		if hidden {
			saved.ShowCmd = 0 // SW_HIDE: put the bounds back without showing
			if !restore {
				saved.ShowCmd = 1 // SW_SHOWNORMAL
			}
		} else if !restore && saved.ShowCmd == 2 { // was minimised, now opened
			saved.ShowCmd = 1
		}
		if saved.ShowCmd == 1 { // open: put size and position back directly
			r := saved.Normal
			const swpShow, swpNoZOrder = 0x0040, 0x0004
			procSetWindowPos.Call(h, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpShow|swpNoZOrder)
			return
		}
		procSetWindowPlacement.Call(h, uintptr(unsafe.Pointer(&saved)))
	}
}
