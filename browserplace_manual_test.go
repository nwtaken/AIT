//go:build manual

package main

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The browser surface sits exactly over the pane's page area, parks off-screen
// when the pane is not showing, and follows the main window. Uses a stand-in
// main window (same class as AIT's) and a real WebView2. Run with:
//
//	go test -tags manual -run TestRealBrowserPlacement -v
var procGetWindowRect = user32.NewProc("GetWindowRect")

func TestRealBrowserPlacement(t *testing.T) {
	appData, _ := os.UserConfigDir()
	src := filepath.Join(appData, "AIT", "browser")
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "browser"), 0o755)
	_ = src
	exec.Command("robocopy", src, filepath.Join(root, "browser"), "/E", "/NFL", "/NDL", "/NJH", "/NJS", "/XD", "profile", "output", "tmp").Run()
	store, _ := newStoreAt(root, t.TempDir())
	app := NewApp(store)
	app.emit = func(string, ...any) {}
	tab := &Tab{id: 1, agent: registry["claude"], profile: "claude"}
	tab.adopted.Store(true)
	app.tabs[1] = tab
	port, _ := freePort()
	l := &browserLog{key: "k", tab: tab, port: port, allowed: map[string]bool{}}
	app.browser.logs = map[string]*browserLog{"k": l}
	tab.browserKey.Store("k")

	// The stand-in main window, on the UI thread.
	var main uintptr
	uiWait(func() {
		inst := windows.Handle(0)
		windows.GetModuleHandleEx(0, nil, &inst)
		cls, _ := windows.UTF16PtrFromString(windowClass)
		wc := wndClassExW{WndProc: syscall.NewCallback(func(h, m, w, p uintptr) uintptr { r, _, _ := procDefWindowProcW.Call(h, m, w, p); return r }), Instance: uintptr(inst), ClassName: cls}
		wc.Size = uint32(unsafe.Sizeof(wc))
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		title, _ := windows.UTF16PtrFromString("fake AIT")
		const wsOverlappedWindow, wsVisible = 0x00CF0000, 0x10000000
		main, _, _ = procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)), wsOverlappedWindow|wsVisible, 120, 90, 1000, 700, 0, 0, uintptr(inst), 0)
	})
	if main == 0 {
		t.Fatal("no stand-in window")
	}
	defer uiWait(func() { procDestroyWindow.Call(main) })
	defer app.closeBrowsers()
	if err := app.browserEnsureView(l); err != nil {
		t.Fatal(err)
	}

	rect := func(h uintptr) winRect {
		var r winRect
		procGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
		return r
	}
	v := l.view
	var origin struct{ X, Y int32 }
	origin.X, origin.Y = 0, 0
	procClientToScreen.Call(main, uintptr(unsafe.Pointer(&origin)))
	dpi, _, _ := procGetDpiForWindow.Call(main)
	s := float64(dpi) / 96

	// Not showing: parked off-screen.
	if r := rect(v.hwnd); r.Left > -10000 {
		t.Fatalf("starts parked, at %+v", r)
	}
	// Showing: exactly over the page area.
	app.BrowserBounds(1, 300, 50, 400, 300, true)
	r := rect(v.hwnd)
	want := winRect{origin.X + int32(math.Round(300*s)), origin.Y + int32(math.Round(50*s)), 0, 0}
	want.Right, want.Bottom = want.Left+int32(math.Round(400*s)), want.Top+int32(math.Round(300*s))
	if r != want {
		t.Fatalf("placed at %+v, want %+v (dpi %v)", r, want, dpi)
	}
	// The window moves: the surface follows (the follow loop, 60 ms).
	go app.browserFollow()
	const swpNoSize, swpNoZOrder = 0x0001, 0x0004
	procSetWindowPos.Call(main, 0, 400, 300, 0, 0, swpNoSize|swpNoZOrder)
	time.Sleep(400 * time.Millisecond)
	origin.X, origin.Y = 0, 0
	procClientToScreen.Call(main, uintptr(unsafe.Pointer(&origin)))
	if r := rect(v.hwnd); r.Left != origin.X+int32(math.Round(300*s)) || r.Top != origin.Y+int32(math.Round(50*s)) {
		t.Fatalf("did not follow the window: %+v, window client at %+v", r, origin)
	}
	// Minimised: parked. Restored: back.
	const swMinimize, swRestore = 6, 9
	procShowWindow.Call(main, swMinimize)
	time.Sleep(400 * time.Millisecond)
	if r := rect(v.hwnd); r.Left > -10000 {
		t.Fatalf("not parked while minimised: %+v", r)
	}
	procShowWindow.Call(main, swRestore)
	time.Sleep(400 * time.Millisecond)
	origin.X, origin.Y = 0, 0
	procClientToScreen.Call(main, uintptr(unsafe.Pointer(&origin)))
	if r := rect(v.hwnd); r.Left != origin.X+int32(math.Round(300*s)) {
		t.Fatalf("not back after restore: %+v (client %+v)", r, origin)
	}
	// Hidden by the pane: parked, but the page still answers the AI.
	app.BrowserBounds(1, 300, 50, 400, 300, false)
	if r := rect(v.hwnd); r.Left > -10000 {
		t.Fatalf("not parked when the pane hides: %+v", r)
	}
	v.mu.Lock()
	title := v.title
	v.mu.Unlock()
	t.Logf("surface ok, page title %q", title)
	if !strings.HasPrefix(strings.ToLower(v.dir), strings.ToLower(root)) {
		t.Errorf("private profile folder is %s", v.dir)
	}
}
