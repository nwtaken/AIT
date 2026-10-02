//go:build manual

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runMcp starts the real CLI in chat mode, sends extra control lines after
// start, and returns the server statuses from the first "mcp" event.
func runMcp(t *testing.T, p Provider, l Launch, after func(cp ChatProvider, st *ChatState) [][]byte) map[string]string {
	cp := chatOf(p)
	cwd := t.TempDir()
	cmd := exec.Command(p.Command()[0], append(p.Command()[1:], cp.ChatArgs(l, "ask")...)...)
	cmd.Dir = cwd
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	st := &ChatState{Asks: map[string]json.RawMessage{}, Data: map[string]any{}, Send: func(b []byte) { stdin.Write(b) }}
	for _, b := range cp.ChatStart(st, l, "ask", cwd) {
		stdin.Write(b)
	}
	got := make(chan map[string]string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			for _, e := range cp.ChatDecode(sc.Bytes(), st) {
				if e["k"] == "mcp" {
					m := map[string]string{}
					for _, s := range e["servers"].([]map[string]any) {
						m[s["name"].(string)] = s["status"].(string)
					}
					got <- m
					return
				}
			}
		}
		io.Copy(io.Discard, stdout)
	}()
	time.Sleep(14 * time.Second) // servers connect
	for _, b := range after(cp, st) {
		stdin.Write(b)
	}
	select {
	case m := <-got:
		return m
	case <-time.After(30 * time.Second):
		t.Fatal("no mcp status")
	}
	return nil
}

func TestRealClaudeMcp(t *testing.T) {
	home, _ := os.UserHomeDir()
	providers(home)
	cl := registry["claude"]
	m := runMcp(t, cl, Launch{McpOff: []string{"elevenlabs"}}, func(cp ChatProvider, st *ChatState) [][]byte {
		return [][]byte{cp.ChatControl(st, "mcp-status")}
	})
	t.Logf("start with elevenlabs off: %v", m)
	if m["elevenlabs"] != "disabled" {
		t.Fatal("elevenlabs not off at start")
	}
	m = runMcp(t, cl, Launch{McpOff: []string{"elevenlabs"}}, func(cp ChatProvider, st *ChatState) [][]byte {
		return [][]byte{cp.(mcpToggler).McpToggle(st, "elevenlabs", true)}
	})
	t.Logf("after live toggle on: %v", m)
	if m["elevenlabs"] == "disabled" {
		t.Fatal("toggle on did not apply")
	}
}

func TestRealCodexMcp(t *testing.T) {
	home, _ := os.UserHomeDir()
	providers(home)
	cx := registry["codex"]
	off := cx.(mcpKnower).McpKnown(home+"/.codex", []string{"robloxstudio", "not-a-server"})
	if strings.Join(off, ",") != "robloxstudio" {
		t.Fatalf("known filter: %v", off)
	}
	m := runMcp(t, cx, Launch{McpOff: off}, func(cp ChatProvider, st *ChatState) [][]byte {
		return [][]byte{cp.ChatControl(st, "mcp-status")}
	})
	t.Logf("codex with robloxstudio off: %v", m)
	if m["robloxstudio"] == "connected" || m["Roblox_Studio"] == "" {
		t.Fatal("codex did not apply the switch")
	}
}

func TestRealClaudeContextAtStart(t *testing.T) {
	home, _ := os.UserHomeDir()
	providers(home)
	p := registry["claude"]
	cp := chatOf(p)
	cmd := exec.Command(p.Command()[0], append(p.Command()[1:], cp.ChatArgs(Launch{}, "ask")...)...)
	cmd.Dir = t.TempDir()
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	st := &ChatState{Asks: map[string]json.RawMessage{}, Data: map[string]any{}, Send: func(b []byte) { stdin.Write(b) }}
	for _, b := range cp.ChatStart(st, Launch{}, "ask", cmd.Dir) {
		stdin.Write(b)
	}
	got := make(chan Ev, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			for _, e := range cp.ChatDecode(sc.Bytes(), st) {
				if e["k"] == "ctx" {
					got <- e
					return
				}
			}
		}
	}()
	select {
	case e := <-got:
		t.Logf("context at start: %v", e)
	case <-time.After(30 * time.Second):
		t.Fatal("no context readout at start")
	}
}
