//go:build manual

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The real surface: AIT creates its WebView2, and an AI's tools (through the
// gate and Playwright, over the debugging port) open a page in it. Creates a
// window for a moment. Run with:
//
//	go test -tags manual -run TestRealBrowserSurface -v
func TestRealBrowserSurface(t *testing.T) {
	appData, _ := os.UserConfigDir()
	src := filepath.Join(appData, "AIT", "browser")
	if !fileExists(filepath.Join(src, "node_modules", "@playwright", "mcp", "cli.js")) {
		t.Skip("the browser is not installed (start AIT once)")
	}
	root := t.TempDir()
	if out, err := exec.Command("robocopy", src, filepath.Join(root, "browser"), "/E", "/NFL", "/NDL", "/NJH", "/NJS", "/XD", "profile", "output", "tmp").CombinedOutput(); err != nil && !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("copy: %v %s", err, out)
	}
	store, _ := newStoreAt(root, t.TempDir())
	app := NewApp(store)
	var mu sync.Mutex
	var events []string
	app.emit = func(name string, d ...any) {
		mu.Lock()
		defer mu.Unlock()
		switch name {
		case "browser:open", "browser:gone":
			events = append(events, name)
		case "browser:page":
			events = append(events, fmt.Sprintf("page %v | %v", d[1], d[2]))
		case "chat:ev":
			for _, e := range d[1].([]Ev) {
				if e["k"] == "ask" {
					go app.ChatAnswer(1, e["req"].(string), "allow")
					events = append(events, fmt.Sprint("ask ", e["desc"]))
				}
			}
		}
	}
	tab := &Tab{id: 1, agent: registry["claude"], profile: "claude"}
	tab.adopted.Store(true)
	port, _ := freePort()
	l := &browserLog{key: "k", tab: tab, ai: "Claude", port: port, allowed: map[string]bool{}}
	app.browser.logs = map[string]*browserLog{"k": l}
	app.tabs[1] = tab
	tab.browserKey.Store("k")
	base, err := app.browserBase()
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "browser", "config.json")
	b, _ := json.Marshal(map[string]any{"browser": map[string]any{"browserName": "chromium", "cdpEndpoint": fmt.Sprintf("http://127.0.0.1:%d", port)}, "outputDir": filepath.Join(root, "out")})
	os.WriteFile(cfg, b, 0o644)
	gate, _ := browserAssets.ReadFile("browserassets/gate.cjs")
	gatePath := filepath.Join(root, "browser", "gate.cjs")
	os.WriteFile(gatePath, gate, 0o644)

	cmd := exec.Command(nodeBinary(), gatePath, "--ait", base, "--key", "k", "--cli", store.browserCLI(), "--config", cfg)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); app.closeBrowsers(); time.Sleep(4 * time.Second) }()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	id := 0
	rpc := func(method string, params any) map[string]any {
		id++
		line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		fmt.Fprintln(stdin, string(line))
		done := make(chan map[string]any, 1)
		go func() {
			for sc.Scan() {
				var m map[string]any
				if json.Unmarshal(sc.Bytes(), &m) == nil && m["id"] == float64(id) {
					done <- m
					return
				}
			}
		}()
		select {
		case m := <-done:
			return m
		case <-time.After(90 * time.Second):
			t.Fatalf("%s: no answer", method)
		}
		return nil
	}
	call := func(name string, args map[string]any) (bool, string) {
		r := rpc("tools/call", map[string]any{"name": name, "arguments": args})["result"].(map[string]any)
		var text string
		for _, c := range r["content"].([]any) {
			text += c.(map[string]any)["text"].(string)
		}
		isErr, _ := r["isError"].(bool)
		return isErr, text
	}
	rpc("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}})
	fmt.Fprintln(stdin, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	isErr, text := call("browser_navigate", map[string]any{"url": "https://example.com"})
	if isErr || !strings.Contains(text, "Example Domain") {
		t.Fatalf("navigate: %v %s", isErr, text)
	}
	isErr, text = call("browser_evaluate", map[string]any{"function": "() => document.title + ' | ' + location.href"})
	if isErr || !strings.Contains(text, "Example Domain | https://example.com/") {
		t.Fatalf("evaluate: %v %s", isErr, text)
	}
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	t.Logf("events: %v", events)
	joined := strings.Join(events, "\n")
	for _, want := range []string{"browser:open", "ask open example.com", "page https://example.com/ | Example Domain"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no %q in events", want)
		}
	}
	if st := app.BrowserState(1); st == nil || st["open"] != true || st["title"] != "Example Domain" {
		t.Errorf("state %v", st)
	}
}
