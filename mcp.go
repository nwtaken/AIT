package main

import (
	"fmt"
	"slices"
	"strings"
)

// MCP servers per AI. The page lists what the running chat reports
// (ChatControl "mcp-status" -> an "mcp" event). Switching a server off is
// remembered per AI in config.json and applied to every chat that AI starts,
// on any account and in any folder (Launch.McpOff).

// mcpToggler is implemented by AIs that can switch a server in a running
// chat; the others are restarted on the same conversation.
type mcpToggler interface {
	McpToggle(st *ChatState, name string, on bool) []byte
}

// mcpKnower keeps only the servers an account actually has: Codex treats
// "enabled=false" for an unknown server as a new, broken one and won't start.
type mcpKnower interface {
	McpKnown(home string, names []string) []string
}

func (s *Store) setMcpOff(provider, name string, off bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.Config()
	list := slices.DeleteFunc(slices.Clone(c.McpOff[provider]), func(n string) bool { return n == name })
	if off {
		list = append(list, name)
	}
	if c.McpOff == nil {
		c.McpOff = map[string][]string{}
	}
	c.McpOff[provider] = list
	return s.saveConfig(c)
}

// McpOff lists the servers switched off for an AI.
func (a *App) McpOff(provider string) []string {
	return append([]string{}, a.store.Config().McpOff[provider]...)
}

// McpToggle switches one of the tab's AI's MCP servers on or off.
func (a *App) McpToggle(tabID int, name string, on bool) error {
	t := a.tab(tabID)
	if t == nil {
		return fmt.Errorf("no tab")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.reading {
		return fmt.Errorf("wait for the conversation handover to finish")
	}
	proc, cp, st := t.chat, chatOf(t.agent), t.chatState
	if proc == nil || cp == nil {
		return fmt.Errorf("the agent is not running")
	}
	if err := a.store.setMcpOff(t.profile, name, !on); err != nil {
		return err
	}
	if tg, ok := cp.(mcpToggler); ok {
		return proc.send(tg.McpToggle(st, name, on))
	}
	// The AI reads its servers at start: restart it on the same conversation,
	// carrying on if it was in the middle of a turn.
	acct, ok := a.store.Account(t.acct)
	if !ok {
		return fmt.Errorf("no account %q", t.acct)
	}
	prompt := ""
	if t.working.Load() {
		prompt = "continue"
	}
	return a.relaunch(t, acct, prompt)
}

// MCP categories, in the order the MCP list shows them.
var mcpCategories = []struct {
	name string
	keys []string
}{
	{"Roblox", []string{"roblox", "rbx", "luau", "rojo", "opencloud"}},
	{"Minecraft", []string{"minecraft", "mineflayer", "bukkit", "spigot", "papermc", "fabricmc"}},
	{"Other AIs", []string{"elevenlabs", "higgsfield", "recraft", "openai", "chatgpt", "gpt", "gemini", "codex", "midjourney", "stabilityai",
		"huggingface", "runway", "suno", "ollama", "perplexity", "mistral", "groq", "deepseek", "ideogram", "heygen", "leonardo",
		"text_to_speech", "text_to_image", "generate_image", "generate_video"}},
}

// mcpCategory sorts a server by what it is for, from its name, where it runs
// (URL or command line) and its tool names. A server reached over the web
// that fits nothing else is a website; the rest is "Other".
func mcpCategory(name, where string, tools []string) string {
	hay := strings.ToLower(name + " " + where + " " + strings.Join(tools, " "))
	for _, c := range mcpCategories {
		for _, k := range c.keys {
			if strings.Contains(hay, k) {
				return c.name
			}
		}
	}
	if strings.HasPrefix(name, "claude.ai ") || strings.Contains(strings.ToLower(where), "http") {
		return "Websites"
	}
	return "Other"
}
