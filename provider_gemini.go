package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// gemini drives Google's Gemini CLI (npm: @google/gemini-cli) in a terminal
// tab, with the model switcher.
//
// Basic support: it starts the CLI on the chosen model. It does not yet do
// automatic account handoff or history — Gemini was not installed where AIT
// was built, so its transcript format and limit messages could not be
// measured, and a guessed format is worse than none. Implementing Scan,
// Chats, Carry and SessionID here is all that is missing.
type gemini struct{ userHome string }

func (g *gemini) ID() string          { return "gemini" }
func (g *gemini) Name() string        { return "Gemini" }
func (g *gemini) HomeEnv() string     { return "GEMINI_CLI_HOME" }
func (g *gemini) DefaultHome() string { return filepath.Join(g.userHome, ".gemini") }
func (g *gemini) PresetsID() bool     { return false }

func (g *gemini) Command() []string {
	if d, err := os.UserConfigDir(); err == nil {
		js := filepath.Join(d, "npm", "node_modules", "@google", "gemini-cli", "dist", "index.js")
		if node := lookBinary("node"); node != "" && fileExists(js) {
			return []string{node, js}
		}
	}
	if p := lookBinary("gemini"); p != "" && filepath.Ext(p) == ".exe" {
		return []string{p}
	}
	return nil
}

func (g *gemini) SignedIn(home string) bool {
	return fileExists(filepath.Join(home, "oauth_creds.json")) || os.Getenv("GEMINI_API_KEY") != ""
}

func (g *gemini) Email(home string) string {
	var a struct {
		Active string `json:"active"`
	}
	if b, err := os.ReadFile(filepath.Join(home, "google_accounts.json")); err == nil {
		json.Unmarshal(b, &a)
	}
	return a.Active
}

func (g *gemini) Provision(home, cwd string, trusted bool) {}

// Models: the CLI's own default first; the rest is what the user set in
// Gemini's settings.json, so AIT never offers a model name it made up. Any
// other model can be typed in the switcher's "Other model" field.
func (g *gemini) Models() []Model {
	out := []Model{{ID: "", Name: "Default", Desc: "Your Gemini settings"}}
	var s struct {
		Model struct {
			Name string `json:"name"`
		} `json:"model"`
	}
	if b, err := os.ReadFile(filepath.Join(g.DefaultHome(), "settings.json")); err == nil && json.Unmarshal(b, &s) == nil && s.Model.Name != "" {
		out = append(out, Model{ID: s.Model.Name, Name: s.Model.Name, Desc: "From your Gemini settings"})
	}
	return out
}

func (g *gemini) ModelArgs(id string) []string         { return []string{"-m", id} }
func (g *gemini) ShareMemory(home, cwd, shared string) {}
func (g *gemini) AccessArgs(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	return []string{"--include-directories", strings.Join(dirs, ",")}
}
func (g *gemini) RulesArgs(file, text string) []string      { return nil } // no instruction flag measured yet
func (g *gemini) NestingEnv() []string                      { return []string{"GEMINI_CLI"} }
func (g *gemini) TrustAnswer(string, string) (string, bool) { return "", false }

func (g *gemini) Args(l Launch) []string {
	args := append([]string{}, l.Extra...)
	if l.Prompt != "" {
		args = append(args, "-i", l.Prompt)
	}
	return args
}

func (g *gemini) SessionFile(home, cwd, id string) string { return "" }
func (g *gemini) NewestSession(home, cwd string, after time.Time, skip map[string]bool) string {
	return ""
}
func (g *gemini) SessionID(path string) string { return "" }
func (g *gemini) Carry(path, toHome, cwd string) (string, error) {
	return "", errors.New("Gemini conversations cannot be moved between accounts yet")
}
func (g *gemini) Scan(path string, offset int64, since time.Time) (*limitHit, int64) {
	return nil, offset
}
func (g *gemini) Chats(home string) []Chat { return nil }
