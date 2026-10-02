//go:build manual

package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The effort controls against the real Claude: the model list carries effort
// levels, and a change of effort or model is reported back.
func TestRealClaudeEffort(t *testing.T) {
	home, _ := os.UserHomeDir()
	c := &claude{userHome: home}
	cmd := exec.Command(c.Command()[0], c.ChatArgs(Launch{}, "ask")...)
	var in bytes.Buffer
	for _, l := range c.ChatStart(nil, Launch{}, "ask", "") {
		in.Write(l)
	}
	in.Write(c.ChatControl(nil, "effort:low"))
	in.Write(c.ChatControl(nil, "model:claude-opus-4-6"))
	in.Write(c.ChatControl(nil, "effort:xhigh"))
	in.Write(c.ChatControl(nil, "model:claude-haiku-4-5"))
	cmd.Stdin = &in
	out, _ := cmd.StdoutPipe()
	cmd.Start()
	go func() { time.Sleep(60 * time.Second); cmd.Process.Kill() }()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		for _, e := range c.ChatDecode(sc.Bytes(), nil) {
			switch e["k"] {
			case "efforts":
				m := e["models"].(map[string][]string)
				t.Logf("efforts: default=%v opus-4-6=%v Opus 5.5=%v haiku=%v", m[""], m["claude-opus-4-6"], m["Opus 5.5"], m["Haiku 4.5"])
			case "effort":
				t.Logf("effort: %q", e["effort"])
			}
		}
	}
	cmd.Wait()
}
