package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Reading a transcript for its title is the slow part of history, and most
// files never change again — so each is read once per (size, mtime).
var chatCache = struct {
	sync.Mutex
	m map[string]chatEntry
}{m: map[string]chatEntry{}}

type chatEntry struct {
	size int64
	mod  time.Time
	chat Chat
	ok   bool
}

func cachedChat(p string, read func(string) (Chat, bool)) (Chat, bool) {
	info, err := os.Stat(p)
	if err != nil {
		return Chat{}, false
	}
	chatCache.Lock()
	e, hit := chatCache.m[p]
	chatCache.Unlock()
	if hit && e.size == info.Size() && e.mod.Equal(info.ModTime()) {
		return e.chat, e.ok
	}
	ch, ok := read(p)
	ch.Updated = info.ModTime().Unix()
	chatCache.Lock()
	chatCache.m[p] = chatEntry{size: info.Size(), mod: info.ModTime(), chat: ch, ok: ok}
	chatCache.Unlock()
	return ch, ok
}

// ChatView is one row in the history panel.
type ChatView struct {
	Chat
	ProviderName string `json:"providerName"`
	Folder       string `json:"folder"`
	Ref          string `json:"ref"` // opaque handle passed back to Open
}

// History lists past conversations from every account of every agent,
// newest first. A conversation carried between accounts exists in several
// homes; only its newest copy is listed.
func (a *App) History() []ChatView {
	seen := map[string]int{}
	var all []Chat
	homes := map[string]bool{}
	for _, acct := range a.store.Config().Accounts {
		home := strings.ToLower(a.store.Home(acct))
		if homes[home] {
			continue
		}
		homes[home] = true
		for _, ch := range acct.provider().Chats(a.store.Home(acct)) {
			key := ch.Provider + "/" + ch.ID
			if i, ok := seen[key]; ok {
				if ch.Updated > all[i].Updated {
					all[i] = ch
				}
				continue
			}
			seen[key] = len(all)
			all = append(all, ch)
		}
	}
	sortChats(all)
	if len(all) > 400 {
		all = all[:400]
	}
	out := make([]ChatView, 0, len(all))
	for _, ch := range all {
		title := strings.Join(strings.Fields(ch.Title), " ")
		if len(title) > 140 {
			title = title[:140] + "…"
		}
		ch.Title = title
		out = append(out, ChatView{
			Chat:         ch,
			ProviderName: provider(ch.Provider).Name(),
			Folder:       maskUser(ch.Cwd),
			Ref:          ch.Path,
		})
	}
	return out
}

// ---- attachments ----------------------------------------------------------------

// SaveAttachment writes pasted image data to a temp file and returns its path.
// Agents attach an image when its path is pasted into the prompt, so this is
// how a screenshot on the clipboard becomes an attachment.
func (a *App) SaveAttachment(b64, ext string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(os.TempDir(), "AIT")
	os.MkdirAll(dir, 0o755)
	ext = strings.Trim(strings.ToLower(ext), ". ")
	if ext == "" || len(ext) > 5 {
		ext = "png"
	}
	p := filepath.Join(dir, fmt.Sprintf("paste-%s.%s", time.Now().Format("20060102-150405.000"), ext))
	return p, os.WriteFile(p, data, 0o644)
}

// ClipboardFiles returns the paths of files copied in Explorer, if any.
func (a *App) ClipboardFiles() []string {
	if f := clipboardFiles(); f != nil {
		return f
	}
	return []string{}
}

// PickFiles opens the system file picker for attachments.
func (a *App) PickFiles() ([]string, error) {
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Attach files",
		Filters: []runtime.FileFilter{
			{DisplayName: "All files", Pattern: "*.*"},
			{DisplayName: "Images", Pattern: "*.png;*.jpg;*.jpeg;*.gif;*.webp;*.bmp"},
			{DisplayName: "Videos", Pattern: "*.mp4;*.mov;*.webm;*.mkv;*.avi"},
		},
	})
}

// maskUser hides the Windows user name in a path shown on screen.
func maskUser(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || p == "" {
		return p
	}
	if strings.HasPrefix(strings.ToLower(p), strings.ToLower(home)) {
		return "~" + p[len(home):]
	}
	return p
}

func pickFolder(ctx context.Context, def string) (string, error) {
	return runtime.OpenDirectoryDialog(ctx, runtime.OpenDialogOptions{Title: "Choose a working folder", DefaultDirectory: def})
}
