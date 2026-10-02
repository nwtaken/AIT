package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var unsafeName = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)

// ExportChat saves the tab's whole conversation as markdown in Downloads
// (/export) and shows it in Explorer. It returns the file's path.
func (a *App) ExportChat(id int) (string, error) {
	t := a.tab(id)
	if t == nil {
		return "", fmt.Errorf("no chat")
	}
	t.mu.Lock()
	agent, cwd, session := t.agent, t.cwd, t.session
	t.mu.Unlock()
	if session == "" || !fileExists(session) {
		return "", fmt.Errorf("nothing to export yet")
	}
	snap := &Tab{session: session, cwd: cwd}
	text := conversationMarkdown(snap, agent, fmt.Sprintf("# Chat with %s\n\nExported from AIT on %s. Working folder: %s\n\n",
		agent.Name(), time.Now().Format("2006-01-02 15:04"), maskUser(cwd)))
	title := "chat" // the file is named after the first request
	if _, rest, ok := strings.Cut(text, "## User\n\n"); ok {
		title, _, _ = strings.Cut(rest, "\n\n## ")
	}
	title = strings.Join(strings.Fields(unsafeName.ReplaceAllString(handoverTitle(title), " ")), " ")
	if len(title) > 60 {
		title = strings.TrimSpace(cutText(title, 60))
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Downloads")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, fmt.Sprintf("AIT - %s - %s.md", title, time.Now().Format("2006-01-02 15-04")))
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		return "", err
	}
	revealFile(p)
	return p, nil
}

// revealFile shows a file selected in Explorer.
var revealFile = func(p string) { exec.Command("explorer.exe", "/select,", p).Start() }
