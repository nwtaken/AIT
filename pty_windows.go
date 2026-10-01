//go:build windows

package main

import (
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PTY is one child process attached to a Windows pseudo console (ConPTY).
// Output arrives on Read as UTF-8 with VT sequences, exactly what a terminal
// emulator expects.
type PTY struct {
	hpc  windows.Handle
	in   *os.File // we write keystrokes here
	out  *os.File // we read the screen stream here
	proc windows.Handle
	job  windows.Handle // non-zero: closing it kills the whole process tree

	closeOnce sync.Once
}

// StartPTY launches argv in dir. killTree puts the process in a job object so
// Close takes its children with it — claude.exe spawns shells for its tools,
// and those must not outlive an account switch.
func StartPTY(argv []string, dir string, env []string, cols, rows int, killTree bool) (*PTY, error) {
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, err
	}

	var hpc windows.Handle
	err := windows.CreatePseudoConsole(coord(cols, rows), inR, outW, 0, &hpc)
	// The pseudo console holds its own duplicates of the far ends.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)
	if err != nil {
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, err
	}
	p := &PTY{
		hpc: hpc,
		in:  os.NewFile(uintptr(inW), "pty-in"),
		out: os.NewFile(uintptr(outR), "pty-out"),
	}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		p.Close()
		return nil, err
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, unsafe.Pointer(hpc), unsafe.Sizeof(hpc)); err != nil {
		p.Close()
		return nil, err
	}

	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))
	// Without this a child inherits AIT's own std handles when AIT has
	// any (started from a terminal, or under `go test`) and writes there
	// instead of to the pseudo console. Null handles make it use the console.
	si.Flags |= windows.STARTF_USESTDHANDLES

	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		p.Close()
		return nil, err
	}
	var wdir *uint16
	if dir != "" {
		if wdir, err = windows.UTF16PtrFromString(dir); err != nil {
			p.Close()
			return nil, err
		}
	}

	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if killTree {
		flags |= windows.CREATE_SUSPENDED // into the job before it can spawn anything
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(nil, cmdline, nil, nil, false, flags, envBlock(env), wdir, &si.StartupInfo, &pi)
	if err != nil {
		p.Close()
		return nil, err
	}
	p.proc = pi.Process

	if killTree {
		if job, err := newKillJob(); err == nil {
			if windows.AssignProcessToJobObject(job, pi.Process) == nil {
				p.job = job
			} else {
				windows.CloseHandle(job)
			}
		}
		windows.ResumeThread(pi.Thread)
	}
	windows.CloseHandle(pi.Thread)
	return p, nil
}

func (p *PTY) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *PTY) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *PTY) Resize(cols, rows int) error {
	return windows.ResizePseudoConsole(p.hpc, coord(cols, rows))
}

// Wait blocks until the process exits and returns its exit code.
func (p *PTY) Wait() (uint32, error) {
	if p.proc == 0 {
		return 0, errors.New("no process")
	}
	if _, err := windows.WaitForSingleObject(p.proc, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	err := windows.GetExitCodeProcess(p.proc, &code)
	return code, err
}

// Close kills the process (and its tree, when it has a job) and releases the
// console. Safe to call more than once and from any goroutine.
func (p *PTY) Close() {
	p.closeOnce.Do(func() {
		if p.job != 0 {
			windows.CloseHandle(p.job) // KILL_ON_JOB_CLOSE
		}
		if p.proc != 0 {
			windows.TerminateProcess(p.proc, 1)
		}
		// Closing the console while nothing drains its output can block on
		// older Windows 10 builds; the read loop is always running, so this
		// returns once the final frame is flushed.
		windows.ClosePseudoConsole(p.hpc)
		p.in.Close()
		p.out.Close()
	})
}

// release frees the process handle once the waiter is done with it.
func (p *PTY) release() {
	if p.proc != 0 {
		windows.CloseHandle(p.proc)
	}
}

func coord(cols, rows int) windows.Coord {
	if cols < 2 {
		cols = 2
	}
	if rows < 1 {
		rows = 1
	}
	return windows.Coord{X: int16(cols), Y: int16(rows)}
}

func newKillJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// envBlock builds the double-NUL-terminated UTF-16 block CreateProcess wants.
func envBlock(env []string) *uint16 {
	sorted := append([]string(nil), env...)
	sort.Slice(sorted, func(i, j int) bool { return strings.ToUpper(sorted[i]) < strings.ToUpper(sorted[j]) })
	var b []uint16
	for _, kv := range sorted {
		b = append(b, utf16.Encode([]rune(kv))...)
		b = append(b, 0)
	}
	b = append(b, 0)
	return &b[0]
}

// killTree puts an already-started process into a kill-on-close job and
// returns the function that ends it and everything it started since.
func killTree(pid int) func() {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return func() {}
	}
	job, err := newKillJob()
	if err != nil || windows.AssignProcessToJobObject(job, h) != nil {
		return func() { windows.TerminateProcess(h, 1); windows.CloseHandle(h) }
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			windows.CloseHandle(job)
			windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		})
	}
}
