//go:build manual

package main

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Measures where the time goes when a Claude tab starts, and how long one
// keystroke takes to come back — the two things that "feel slow".
func TestPerfClaudeStartup(t *testing.T) {
	home, _ := os.UserHomeDir()
	providers(home)
	p := provider("claude")
	argv := append(p.Command(), "--session-id", newUUID())
	if x := os.Getenv("PERF_ARGS"); x != "" {
		argv = append(argv, strings.Fields(x)...)
	}
	env := baseEnv()

	start := time.Now()
	pty, err := StartPTY(argv, home, env, 120, 40, true)
	if err != nil {
		t.Fatal(err)
	}
	defer pty.Close()
	t.Logf("process started      %6.0fms", ms(start))

	var mu sync.Mutex
	var raw strings.Builder
	var firstByte, lastByte time.Time
	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := pty.Read(buf)
			mu.Lock()
			if n > 0 {
				if firstByte.IsZero() {
					firstByte = time.Now()
				}
				lastByte = time.Now()
				raw.Write(buf[:n])
			}
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	screen := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(strings.Fields(ansiRe.ReplaceAllString(raw.String(), " ")), " ")
	}
	wait := func(d time.Duration, f func(string) bool) bool {
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			if f(screen()) {
				return true
			}
			time.Sleep(5 * time.Millisecond)
		}
		return false
	}

	wait(30*time.Second, func(s string) bool { return s != "" })
	t.Logf("first output         %6.0fms", float64(firstByte.Sub(start).Milliseconds()))
	if wait(30*time.Second, func(s string) bool { return strings.Contains(s, "trust this folder") }) {
		t.Logf("trust prompt shown   %6.0fms", ms(start))
		mark := 0
		for i := 0; i < 20; i++ {
			s := screen()
			k, done := p.TrustAnswer(s, s[min(mark, len(s)):])
			if k != "" {
				time.Sleep(trustSettle)
				pty.Write([]byte(k))
				mark = len(s)
				if done {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !wait(30*time.Second, func(s string) bool { return strings.Contains(s, "for shortcuts") || strings.Contains(s, "mode on") }) {
		t.Fatalf("never ready: %q", screen()[max(0, len(screen())-600):])
	}
	t.Logf("prompt ready         %6.0fms", ms(start))
	time.Sleep(3 * time.Second) // let MCP etc. settle

	// Keystroke echo: write one char, time until the PTY next produces output.
	var samples []float64
	for i := 0; i < 8; i++ {
		mu.Lock()
		before := lastByte
		mu.Unlock()
		sent := time.Now()
		pty.Write([]byte("x"))
		for {
			mu.Lock()
			got := lastByte
			mu.Unlock()
			if got.After(before) && got.After(sent) {
				samples = append(samples, float64(got.Sub(sent).Microseconds())/1000)
				break
			}
			if time.Since(sent) > 3*time.Second {
				samples = append(samples, -1)
				break
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Logf("keystroke echo (ms)  %v", samples)
	if os.Getenv("PERF_DUMP") != "" {
		mu.Lock()
		n := raw.Len()
		mu.Unlock()
		pty.Write([]byte("q"))
		time.Sleep(600 * time.Millisecond)
		mu.Lock()
		t.Logf("raw bytes for one key: %q", raw.String()[n:])
		all := raw.String()
		t.Logf("last cursor show=%d hide=%d", strings.LastIndex(all, "[?25h"), strings.LastIndex(all, "[?25l"))
		mu.Unlock()
	}
	pty.Write([]byte("\x15")) // clear the line
}

func ms(t time.Time) float64 { return float64(time.Since(t).Milliseconds()) }
