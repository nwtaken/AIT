//go:build windows

package main

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AIT is a GUI binary; any console helper it runs would flash a window.
func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
}

func fileCreated(info os.FileInfo) time.Time {
	if d, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, d.CreationTime.Nanoseconds())
	}
	return info.ModTime()
}

var (
	user32                = windows.NewLazySystemDLL("user32.dll")
	shell32               = windows.NewLazySystemDLL("shell32.dll")
	procOpenClipboard     = user32.NewProc("OpenClipboard")
	procCloseClipboard    = user32.NewProc("CloseClipboard")
	procGetClipboardData  = user32.NewProc("GetClipboardData")
	procIsClipboardFormat = user32.NewProc("IsClipboardFormatAvailable")
	procDragQueryFileW    = shell32.NewProc("DragQueryFileW")
)

const cfHDROP = 15

// fixedDrives lists the PC's local disks (C:\, D:\ …), not removable,
// network or optical drives.
func fixedDrives() []string {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(p) == windows.DRIVE_FIXED {
			out = append(out, root)
		}
	}
	return out
}

// clipboardFiles returns the paths of files copied in Explorer. The web view
// only sees their contents, never where they are.
func clipboardFiles() []string {
	runtime.LockOSThread() // the clipboard is owned per thread
	defer runtime.UnlockOSThread()
	if r, _, _ := procIsClipboardFormat.Call(cfHDROP); r == 0 {
		return nil
	}
	if r, _, _ := procOpenClipboard.Call(0); r == 0 {
		return nil
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfHDROP)
	if h == 0 {
		return nil
	}
	n, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	var out []string
	for i := uintptr(0); i < n; i++ {
		l, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
		buf := make([]uint16, l+1)
		procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		out = append(out, windows.UTF16ToString(buf))
	}
	return out
}
