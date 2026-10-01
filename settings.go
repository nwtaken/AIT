package main

import (
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Settings is what the in-app settings panel edits. Everything else in
// config.json (accounts, args) stays file-only.
type Settings struct {
	Style       string            `json:"style"`       // terminal | desktop
	Theme       string            `json:"theme"`       // campbell | powershell | custom
	Custom      map[string]string `json:"custom"`      // bg, fg, accent, panel
	FontSize    int               `json:"fontSize"`    //
	ChatView    string            `json:"chatView"`    // native | terminal
	Permissions string            `json:"permissions"` // ask | edits | never
	AlwaysOnTop bool              `json:"alwaysOnTop"` //
	Prewarm     bool              `json:"prewarm"`     //
	UserName    string            `json:"userName"`    // what agents call the user
	Models      map[string]string `json:"models"`      // default model per provider
	Onboarded   bool              `json:"onboarded"`   //
	Access      string            `json:"access"`      // everywhere | folder
	AutoUpdate  bool              `json:"autoUpdate"`  //
	AutoInstall bool              `json:"autoInstall"` //
	CrossAI     string            `json:"crossAI"`     // switch | ask | off
}

// AgentStatus is one row of the first-run screen: an agent and whether AIT
// found it installed and signed in.
type AgentStatus struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	SignedIn  bool   `json:"signedIn"`
	Email     string `json:"email"`
}

func (a *App) Agents() []AgentStatus {
	var out []AgentStatus
	for _, id := range order {
		p := registry[id]
		home := p.DefaultHome()
		out = append(out, AgentStatus{ID: id, Name: p.Name(), Installed: p.Command() != nil,
			SignedIn: p.SignedIn(home), Email: maskEmail(p.Email(home))})
	}
	return out
}

func (a *App) GetSettings() Settings {
	c := a.store.Config()
	return Settings{Style: c.Style, Theme: c.Theme, Custom: c.Custom, FontSize: c.FontSize, ChatView: c.ChatView,
		Permissions: c.Permissions, AlwaysOnTop: c.AlwaysOnTop, Prewarm: c.prewarm(),
		UserName: c.UserName, Models: c.Models, Onboarded: c.Onboarded, Access: c.Access, AutoUpdate: c.autoUpdate(), AutoInstall: c.AutoInstall, CrossAI: c.CrossAI}
}

// SaveSettings applies what can apply now (always-on-top) and stores the
// rest; agent options take effect for agents started from now on.
func (a *App) SaveSettings(s Settings) {
	c := a.store.Config()
	// The warm standby was started with the old answers; start a new one.
	restart := c.UserName != s.UserName || c.ChatView != s.ChatView || c.Permissions != s.Permissions || c.Access != s.Access ||
		c.Models[c.DefaultProfile] != s.Models[c.DefaultProfile]
	c.Style, c.Theme, c.Custom, c.FontSize = s.Style, s.Theme, s.Custom, s.FontSize
	c.ChatView, c.Permissions, c.AlwaysOnTop = s.ChatView, s.Permissions, s.AlwaysOnTop
	c.UserName, c.Models, c.Onboarded = s.UserName, s.Models, s.Onboarded
	if s.Access != "" {
		c.Access = s.Access
	}
	au := s.AutoUpdate
	c.AutoUpdate = &au
	c.AutoInstall = s.AutoInstall
	if s.CrossAI != "" {
		c.CrossAI = s.CrossAI
	}
	pw := s.Prewarm
	c.Prewarm = &pw
	a.store.saveConfig(c)
	runtime.WindowSetAlwaysOnTop(a.ctx, s.AlwaysOnTop)
	if !pw || restart {
		a.killStandby()
	}
	if pw && restart {
		time.AfterFunc(time.Second, a.prepareStandby)
	}
}
