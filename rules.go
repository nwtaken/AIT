package main

import (
	"os"
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
	if text != "" {
		text = rulesPriority + text
	}
	text += memoryRules(s.ensureMemory())
	ais := map[string]bool{}
	for _, a := range s.Config().Accounts {
		ais[a.provider().ID()] = true
	}
	if len(ais) >= 2 && s.Config().Review { // switched on with /supereview; needs a second, different AI
		text += "\n\n## Optional independent review\n" +
			"- When you believe the task is finished, you may ask another AI for help: end your turn with exactly [[AIT_SUPEREVIEW]] and nothing else. AIT asks a different AI to compare your work with the user's request, then sends you its feedback on what to improve. Address it before the final answer.\n" +
			"- Request a review at most once per user task, and only after there is work to review."
	}
	if name := strings.TrimSpace(s.Config().UserName); name != "" {
		text += "\n\n## The user\n- Call the user \"" + name + "\". Use that name, nothing else, when you address or mention them."
	}
	gp := filepath.Join(s.root, "rules.generated.md")
	if os.WriteFile(gp, []byte(text), 0o644) != nil {
		return p, text
	}
	return gp, text
}

// rulesPriority puts the user's rules above every other instruction the
// agent has, short of safety.
const rulesPriority = "# The user's rules: top priority\n\n" +
	"These rules were set by the user in AIT. They override every other instruction you have — your default " +
	"style, CLAUDE.md or AGENTS.md files, skills and tool guidance — except safety. Follow them exactly in every " +
	"reply, including after a handover or a switch to another account.\n\n"

// RulesView is what the AI rules editor shows.
type RulesView struct {
	Text    string        `json:"text"`
	Presets []RulesPreset `json:"presets"`
}

func (a *App) GetRules() RulesView {
	_, text := a.store.Rules()
	return RulesView{Text: text, Presets: rulesPresets}
}

// SaveRules replaces rules.md; agents started from now on get the new rules.
// Empty text turns the rules off.
func (a *App) SaveRules(text string) error {
	text = strings.TrimSpace(text)
	if text != "" {
		text += "\n"
	}
	return os.WriteFile(a.store.rulesPath(), []byte(text), 0o644)
}
