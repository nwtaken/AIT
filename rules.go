package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Every agent AIT launches gets rules.md on top of its own instructions —
// through each CLI's supported flag (Provider.RulesArgs), never by editing
// the agent's own files. Emptying the file turns the rules off.
//
// The default is written to be short on purpose: rules are re-sent with every
// request, so each line has to earn its tokens. It holds nothing about any
// particular user; what a user tells AIT about themselves (their name) is
// added at launch by RulesFor, so rules.md stays theirs to edit.
const defaultRules = `# AIT rules

You run inside AIT. Usage is shared across several accounts; spend it on work, not words.

## Output
- Lead with the answer or the result. No preamble, no restating the request, no recap of what was just shown.
- Default to 1-4 sentences. Go longer only when the content needs it: code, steps, data.
- Don't narrate tool use. One short line before a long step is enough.
- No filler, apologies, emoji, or stacked hedges. One caveat, only if it matters.
- Show code once; don't repeat it in prose. Cite files as path:line.
- Final report: what changed, how it was verified, what is still open. Nothing else.

## Work
- Read the relevant code before changing it. One targeted search beats broad exploration.
- Finish the task. Don't stop at a plan when asked to build; don't hand back work you can do.
- Smallest correct change. Match the existing style. No speculative features, abstractions or options.
- Verify by building, testing or reproducing. Never claim success you haven't checked; if you couldn't verify, say so.
- When something fails, find the cause before retrying. Never repeat a failing step unchanged.
- Ask only when a wrong guess would be costly or irreversible; otherwise state the assumption in one line and proceed.
- Confirm before destructive or outward-facing actions: deleting data, force-pushing, publishing, sending.

## Token economy
- Run independent tool calls in parallel.
- Read only what you need: search, then read line ranges. Don't re-read files already in context.
- Keep command output small: filter it, use quiet flags.
- Don't paste large files or logs back; quote the lines that matter.
- Match reasoning depth to difficulty.

## Continuity
- A conversation can move to another account mid-task. If the newest message is just "continue", pick up exactly where the work stopped: no restart, no re-plan, no recap.
`

func (s *Store) rulesPath() string { return filepath.Join(s.root, "rules.md") }

// Rules returns the rules file's path and text, creating it on first use.
func (s *Store) Rules() (string, string) {
	p := s.rulesPath()
	if !fileExists(p) {
		os.WriteFile(p, []byte(defaultRules), 0o644)
	}
	b, _ := os.ReadFile(p)
	return p, strings.TrimSpace(string(b))
}

// RulesFor is what an agent actually gets: rules.md plus what the user told
// AIT during setup. Agents that take a file get a generated copy, so the
// user's rules.md is never rewritten.
func (s *Store) RulesFor() (string, string) {
	p, text := s.Rules()
	text += memoryRules(s.ensureMemory())
	if name := strings.TrimSpace(s.Config().UserName); name != "" {
		text += "\n\n## The user\n- Call the user \"" + name + "\". Use that name, nothing else, when you address or mention them."
	}
	gp := filepath.Join(s.root, "rules.generated.md")
	if os.WriteFile(gp, []byte(text), 0o644) != nil {
		return p, text
	}
	return gp, text
}

func (a *App) OpenRules() {
	p, _ := a.store.Rules()
	exec.Command("notepad.exe", p).Start()
}
