package main

import (
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Telling the user the AI is done (or needs them) while AIT is in the
// background: a Windows notification from the tray icon, and the taskbar
// button flashes when the window is open behind others.

var (
	procShellNotifyIconW   = shell32.NewProc("Shell_NotifyIconW")
	procGetForegroundWnd   = user32.NewProc("GetForegroundWindow")
	procFlashWindowEx      = user32.NewProc("FlashWindowEx")
	procFindWindowExNotify = user32.NewProc("FindWindowExW")
	procWindowPID          = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowLongPtrW  = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW  = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW    = user32.NewProc("CallWindowProcW")
)

// notifyIconData is NOTIFYICONDATAW (976 bytes on 64-bit); the tray
// library's icon is ID 100 on its window.
type notifyIconData struct {
	Size                       uint32
	Wnd                        uintptr
	ID, Flags, CallbackMessage uint32
	Icon                       uintptr
	Tip                        [128]uint16
	State, StateMask           uint32
	Info                       [256]uint16
	TimeoutOrVersion           uint32 // a union in Windows: one field
	InfoTitle                  [64]uint16
	InfoFlags                  uint32
	GUID                       windows.GUID
	BalloonIcon                uintptr
}

// ownWindow finds this process's top-level window of a class.
func ownWindow(class string) uintptr {
	cls, _ := windows.UTF16PtrFromString(class)
	var h uintptr
	for {
		h, _, _ = procFindWindowExNotify.Call(0, h, uintptr(unsafe.Pointer(cls)), 0)
		if h == 0 {
			return 0
		}
		var pid uint32
		procWindowPID.Call(h, uintptr(unsafe.Pointer(&pid)))
		if int(pid) == os.Getpid() {
			return h
		}
	}
}

// inFront reports whether one of AIT's windows (the app or the tray panel)
// is the one the user is looking at.
func inFront() bool {
	fg, _, _ := procGetForegroundWnd.Call()
	var pid uint32
	procWindowPID.Call(fg, uintptr(unsafe.Pointer(&pid)))
	return int(pid) == os.Getpid()
}

// notifyBackground shows a notification unless AIT is in front or the user
// turned notifications off.
func (a *App) notifyBackground(title, text string) {
	if !a.store.Config().notify() || inFront() {
		return
	}
	if main := ownWindow(windowClass); main != 0 {
		type flashInfo struct {
			Size         uint32
			Wnd          uintptr
			Flags, Count uint32
			Timeout      uint32
		}
		const flashwTray, flashwTimerNoFG = 0x2, 0xC
		f := flashInfo{Wnd: main, Flags: flashwTray | flashwTimerNoFG}
		f.Size = uint32(unsafe.Sizeof(f))
		procFlashWindowEx.Call(uintptr(unsafe.Pointer(&f)))
	}
	showNotification(title, text)
}

// showNotification shows a Windows notification from AIT's tray icon.
func showNotification(title, text string) bool {
	tray := ownWindow("SystrayClass")
	if tray == 0 {
		return false
	}
	const nifInfo, niifRespectQuietTime, nimModify = 0x10, 0x80, 0x1
	n := notifyIconData{Wnd: tray, ID: 100, Flags: nifInfo, InfoFlags: niifRespectQuietTime}
	n.Size = uint32(unsafe.Sizeof(n))
	copyUTF16(n.InfoTitle[:], title)
	copyUTF16(n.Info[:], text)
	ok, _, _ := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
	return ok != 0
}

// copyUTF16 fills a fixed buffer, cut to fit with its terminating zero.
func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(strings.ToValidUTF8(s, ""))
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

// notifyTab tells the user about a chat tab's turn: "<AI> finished" with the
// start of the reply, or "<AI> stopped" with the error. Not for a standby
// tab or the preparation turn of a handover.
func (a *App) notifyTab(t *Tab, done, failed, text, errText string) {
	t.mu.Lock()
	skip := t.reading || t.closed || !t.adopted.Load()
	name := t.agent.Name()
	t.mu.Unlock()
	if skip {
		return
	}
	title := name + done
	if errText != "" && failed != "" {
		title, text = name+failed, errText
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 180 {
		text = cutText(text, 180) + "…"
	}
	if text == "" {
		text = "Open AIT to see the result."
	}
	a.notifiedTab.Store(int64(t.id))
	a.notifyBackground(title, text)
}

// openOnNotificationClick makes a click on AIT's notification open AIT. The
// tray library ignores that click, so its window's messages pass through
// here first.
func (a *App) openOnNotificationClick() {
	tray := ownWindow("SystrayClass")
	if tray == 0 {
		return
	}
	const gwlpWndProc = ^uintptr(3) // -4
	const wmTray, ninBalloonUserClick = 0x0400 + 1, 0x0400 + 5
	old, _, _ := procGetWindowLongPtrW.Call(tray, gwlpWndProc)
	proc := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		if msg == wmTray && lp&0xffff == ninBalloonUserClick {
			go func() {
				a.ShowApp()
				a.emit("tab:focus", int(a.notifiedTab.Load())) // the tab the notification was about
			}()
			return 0
		}
		r, _, _ := procCallWindowProcW.Call(old, hwnd, msg, wp, lp)
		return r
	})
	procSetWindowLongPtrW.Call(tray, gwlpWndProc, proc)
}
