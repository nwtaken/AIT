package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Shared memory: one folder every agent reads and writes, so what one AI
// learns the others know.
//
// Each agent keeps its own home (~/.claude, ~/.codex, ~/.gemini) exactly as
// it is — its chats live there, and moving or merging a home makes the agent
// lose its conversations. Sharing happens on top:
//   - agents with a file-based memory folder of their own (Claude) get that
//     folder linked to the shared one wherever no memory exists yet
//     (Provider.ShareMemory); existing memory folders are never touched;
//   - every agent is told in its instructions where the shared memory is and
//     how to use it (RulesFor), which is all an agent without such a folder
//     (ChatGPT, Gemini) needs.

const memoryIndex = "MEMORY.md"

// MemoryDir is the shared memory folder: the user's choice, else Claude's
// existing memory for the home folder (so someone already using Claude keeps
// everything it remembers), else AIT's own folder.
func (s *Store) MemoryDir() string {
	if d := s.Config().MemoryDir; d != "" {
		return d
	}
	if cl := provider("claude"); cl != nil {
		d := filepath.Join(cl.DefaultHome(), "projects", projectKey(s.home), "memory")
		if fileExists(filepath.Join(d, memoryIndex)) {
			return d
		}
	}
	return filepath.Join(s.root, "memory")
}

// ensureMemory creates the shared folder and its index when missing.
func (s *Store) ensureMemory() string {
	d := s.MemoryDir()
	os.MkdirAll(d, 0o755)
	idx := filepath.Join(d, memoryIndex)
	if !fileExists(idx) {
		os.WriteFile(idx, []byte("# Memory\n\nOne line per memory file: `- [Title](file.md) — short hook`.\n"), 0o644)
	}
	return d
}

// memoryRules is the part of every agent's instructions that points it at
// the shared memory. Short: it is re-sent with every request.
func memoryRules(dir string) string {
	return "\n\n## Shared memory\n" +
		"- All AIs in AIT share one memory folder: " + dir + "\n" +
		"- Read " + memoryIndex + " there before starting work; open the files it links only when relevant.\n" +
		"- Save lasting facts (who the user is, their preferences, project decisions, corrections) there: one fact per .md file with a short kebab-case name, plus one line in " + memoryIndex + " (`- [Title](file.md) — hook`). Update an existing file rather than adding a near-duplicate.\n" +
		"- Never save secrets, passwords or tokens. Don't save what the code or git history already records.\n" +
		"- Keep using your own chat history as usual; only memories go here."
}

// OpenMemory shows the shared memory folder in Explorer.
func (a *App) OpenMemory() {
	exec.Command("explorer.exe", a.store.ensureMemory()).Start()
}

// PickMemoryFolder lets the user choose a different shared memory folder.
func (a *App) PickMemoryFolder() (string, error) {
	dir, err := pickFolder(a.ctx, a.store.MemoryDir())
	if err != nil || dir == "" {
		return "", err
	}
	c := a.store.Config()
	c.MemoryDir = dir
	a.store.saveConfig(c)
	a.killStandby() // it was started with the old instructions
	return maskUser(dir), nil
}

func (a *App) MemoryFolder() string { return maskUser(a.store.MemoryDir()) }

// linkMemory makes link point at the shared folder when link does not exist
// yet. An existing folder is left alone: it holds memories and its agent
// expects to find them there.
func linkMemory(link, shared string) {
	if fileExists(link) || strings.EqualFold(filepath.Clean(link), filepath.Clean(shared)) {
		return
	}
	os.MkdirAll(filepath.Dir(link), 0o755)
	junction(shared, link)
}
