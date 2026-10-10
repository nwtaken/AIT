package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A chat that began as a handover has the whole earlier conversation in its
// first message. When that line was longer than the part AIT read, the chat
// had no title: History hid it and reopening it failed ("no longer on disk").
func TestLongHandoverChatStaysListed(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "projects", projectKey(`C:\work`))
	os.MkdirAll(dir, 0o755)
	big := strings.Repeat("the user and the AI built the game, step by step. ", 3000) // ~150 KB
	handover := "You are taking over a conversation from ChatGPT, which was switched out by the user. The conversation so far is in this file: x\n\n" +
		"<conversation>\n# Conversation handed over from ChatGPT\n\n## User\n\nRecreate territorial.io in Roblox\n\n## ChatGPT\n\n" + big + "\n</conversation>"
	line, _ := json.Marshal(map[string]any{"type": "user", "cwd": `C:\work`, "message": map[string]any{"role": "user", "content": handover}})
	p := filepath.Join(dir, "11111111-2222-3333-4444-555555555555.jsonl")
	body := `{"type":"queue-operation"}` + "\n" + string(line) + "\n" + strings.Repeat(`{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}`+"\n", 5000)
	os.WriteFile(p, []byte(body), 0o644)
	if len(line) < 120<<10 {
		t.Fatalf("the first line should be longer than what was read before: %d", len(line))
	}
	cl := &claude{userHome: home}
	ch, ok := cl.readChat(p)
	if !ok || ch.Cwd != `C:\work` || !strings.HasPrefix(ch.Title, "You are taking over a conversation from ChatGPT") {
		t.Fatalf("not listed: ok=%v %+v", ok, ch)
	}
	if got := handoverTitle(ch.Title); got != "Recreate territorial.io in Roblox (continued from ChatGPT)" {
		t.Errorf("History title %q", got)
	}
	found := false
	for _, c := range cl.Chats(home) {
		found = found || c.Path == p
	}
	if !found {
		t.Error("the chat is missing from the list History and reopening use")
	}

	// ChatGPT's transcripts, the same way.
	cx := filepath.Join(t.TempDir(), "rollout-2026-10-09T17-25-48-01a12145-3c9f-7de3-a13f-87f5661ba1cf.jsonl")
	huge := strings.Repeat("x", 300<<10)
	meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "01a12145-3c9f-7de3-a13f-87f5661ba1cf", "cwd": `C:\work`}})
	um, _ := json.Marshal(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": map[string]any{"type": "UserMessage",
		"content": []map[string]any{{"text": "You are taking over a conversation from Claude, which ran out of usage on every account. " + huge}}}}})
	os.WriteFile(cx, []byte(string(meta)+"\n"+string(um)+"\n"), 0o644)
	cc, ok := (&codex{}).readChat(cx)
	if !ok || !strings.HasPrefix(cc.Title, "You are taking over a conversation from Claude") {
		t.Errorf("ChatGPT chat with a long handover not listed: ok=%v title=%.60q", ok, cc.Title)
	}
}

// The conversation that went missing on 2026-10-09 (read only; skipped where
// it does not exist).
func TestTerritorialChatIsListed(t *testing.T) {
	p := `C:\Users\Kaya\AppData\Roaming\AIT\profiles\claude-02\projects\C--Users-Kaya\e185bc53-016d-47eb-861b-4beab773780e.jsonl`
	if !fileExists(p) {
		t.Skip("not on this PC")
	}
	ch, ok := (&claude{}).readChat(p)
	t.Logf("ok=%v cwd=%s title=%q", ok, ch.Cwd, shorten(handoverTitle(ch.Title), 120))
	if !ok {
		t.Fatal("still hidden")
	}
}
