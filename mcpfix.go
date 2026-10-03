package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Fixing an MCP server that does not work. An AI repairs it out of sight,
// on the tab's own AI and account; the MCP list shows only its progress
// (mcpfix:step), any sign-in page it needs (mcpfix:open, opened in the
// browser) and the outcome (mcpfix:done). Nothing of it shows in the chat.

const connectorsURL = "https://claude.ai/settings/connectors"

var (
	fixOpenLine   = regexp.MustCompile(`(?m)^\s*OPEN:\s*(https?://\S+)`)
	fixResultLine = regexp.MustCompile(`(?im)^\s*RESULT:\s*(fixed|not fixed)\s*$`)
)

// FixMcp starts fixing one of the tab's MCP servers. status, errText and
// where (its URL or command line) are as the MCP list shows them.
func (a *App) FixMcp(id int, name, status, errText, where string) error {
	t := a.tab(id)
	if t == nil {
		return errors.New("no chat")
	}
	t.mu.Lock()
	p, acctID, cwd, closed := t.agent, t.acct, t.cwd, t.closed
	t.mu.Unlock()
	acct, ok := a.store.Account(acctID)
	if closed || p == nil || !ok {
		return errors.New("the chat is not running")
	}
	if !t.fixing.CompareAndSwap(false, true) {
		return errors.New("a fix is already running in this chat")
	}
	// A claude.ai connector lives in the claude.ai account: there is nothing
	// on this PC to repair, only a sign-in on the website.
	if strings.HasPrefix(name, "claude.ai ") {
		t.fixing.Store(false)
		who := ""
		if email := p.Email(accountHome(p, acct)); email != "" {
			who = " as " + email
		}
		a.emit("mcpfix:open", id, name, connectorsURL)
		a.emit("mcpfix:done", id, name, false, fmt.Sprintf("%s is a claude.ai connector: connect it on claude.ai%s, then open this list again.", strings.TrimPrefix(name, "claude.ai "), who))
		return nil
	}
	go a.fixMcp(t, p, acct, cwd, name, mcpFixPrompt(p, cwd, name, status, errText, where))
	return nil
}

func (a *App) fixMcp(t *Tab, p Provider, acct Account, cwd, name, prompt string) {
	defer t.fixing.Store(false)
	var extra []string
	if p.ID() == "claude" {
		extra = []string{"--strict-mcp-config"} // the fixer itself starts no MCP servers
	}
	opened := map[string]bool{}
	result, err := a.runHelper(p, acct, cwd, "never", extra, 10*time.Minute, prompt,
		func(s string) { a.emit("mcpfix:step", t.id, name, s) },
		func(block string) {
			for _, m := range fixOpenLine.FindAllStringSubmatch(block, -1) {
				if !opened[m[1]] {
					opened[m[1]] = true
					a.emit("mcpfix:open", t.id, name, m[1])
				}
			}
		})
	if err != nil {
		a.emit("mcpfix:done", t.id, name, false, "The fix did not finish: "+err.Error())
		return
	}
	fixed := false
	if m := fixResultLine.FindStringSubmatch(result); m != nil {
		fixed = strings.EqualFold(m[1], "fixed")
	}
	summary := strings.TrimSpace(fixOpenLine.ReplaceAllString(fixResultLine.ReplaceAllString(result, ""), ""))
	if fixed {
		// The chat reads MCP settings when it starts: restart it on the same
		// conversation (carrying on if it was working).
		t.mu.Lock()
		if !t.closed && !t.reading && t.native {
			prompt := ""
			if t.working.Load() {
				prompt = "continue"
			}
			if err := a.relaunch(t, acct, prompt); err != nil {
				summary += "\n\nRestart the chat to use it: " + err.Error()
			}
		}
		t.mu.Unlock()
	}
	a.emit("mcpfix:done", t.id, name, fixed, summary)
}

// accountHome is the folder an account's AI keeps its settings in.
func accountHome(p Provider, acct Account) string {
	if acct.Dir != "" {
		return acct.Dir
	}
	return p.DefaultHome()
}

func mcpFixPrompt(p Provider, cwd, name, status, errText, where string) string {
	settings := ""
	switch p.ID() {
	case "claude":
		main := filepath.Join(filepath.Dir(p.DefaultHome()), ".claude.json")
		settings = "Claude Code keeps MCP servers in " + main + ": \"mcpServers\" (every folder) and \"projects\" → the folder → \"mcpServers\" (one folder), " +
			"and in .mcp.json files in project folders. AIT copies " + main + " to its other Claude accounts whenever a chat starts, so change it there " +
			"(edit the file, or run `claude mcp` with CLAUDE_CONFIG_DIR unset), never in an account copy. `claude mcp get " + name + "` checks the server."
	case "codex":
		main := filepath.Join(p.DefaultHome(), "config.toml")
		settings = "Codex keeps MCP servers in " + main + " as [mcp_servers.NAME] tables. AIT copies that file to its other ChatGPT accounts whenever a chat starts, " +
			"so change it there, never in an account copy."
	}
	if errText == "" {
		errText = "(none reported)"
	}
	if where == "" {
		where = "(see the settings)"
	}
	return "You are fixing one MCP server for the user of AIT, out of sight: the user sees only a small progress screen and your final message.\n\n" +
		"Server: " + name + "\nStatus: " + status + "\nError: " + errText + "\nRuns: " + where + "\nChat folder: " + cwd + "\n\n" + settings + "\n\n" +
		"Find out why it does not work and fix it: a missing or moved program, a package to install or build, a wrong path, argument or URL, " +
		"a dead local service. Keep its name; change nothing unrelated, never remove or switch off a server, never touch other servers. " +
		"Check that it starts before you say it is fixed.\n" +
		"If the user must sign in or approve something in a browser, write the address on its own line as `OPEN: <url>` as soon as you have it " +
		"(AIT opens it for them), then wait for it to work if you can.\n" +
		"If only the user can fix it (for example an app on this PC that must be running, like Roblox Studio), say exactly what to do.\n" +
		"End with a short message for the user: at most two plain sentences on what was wrong and what you did. " +
		"Its last line is exactly `RESULT: fixed` or `RESULT: not fixed`."
}
