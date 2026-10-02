//go:build manual

package main

import (
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/energye/systray"
)

// A real tray icon accepts AIT's notification (shows one on screen unless
// Focus Assist holds it).
func TestRealNotification(t *testing.T) {
	if n := unsafe.Sizeof(notifyIconData{}); n != 976 {
		t.Fatalf("NOTIFYICONDATAW is %d bytes, want 976", n)
	}
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		systray.Run(func() { systray.SetIcon(trayIcon); close(ready) }, nil)
	}()
	<-ready
	defer systray.Quit()
	time.Sleep(500 * time.Millisecond)
	if !showNotification("Claude finished", "AIT notification test — you can ignore this.") {
		t.Fatal("Windows rejected the notification")
	}
	time.Sleep(3 * time.Second)
}
