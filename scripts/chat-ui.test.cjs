// Run with Playwright available to Node: node scripts/chat-ui.test.cjs
const { chromium } = require("playwright");
const fs = require("node:fs");
const path = require("node:path");
const assert = require("node:assert/strict");

(async () => {
  const browser = await chromium.launch({ channel: "chrome", headless: true });
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", e => errors.push(e.message));
    const frontend = path.join(__dirname, "../frontend");
    await page.setContent(fs.readFileSync(path.join(frontend, "index.html"), "utf8")
      .replace(/<script[\s\S]*?<\/script>/g, "").replace(/<link[^>]*>/g, ""));
    for (const file of ["style.css", "chat.css", "term.css"]) {
      await page.addStyleTag({ path: path.join(frontend, file) });
    }
    for (const file of ["vendor/marked.js", "app.js", "chat.js", "settings.js", "setup.js"]) {
      await page.addScriptTag({ path: path.join(frontend, file) });
    }
    await page.evaluate(() => {
      window.calls = [];
      window.go = { main: { App: {
        History: async () => [],
        ChatControl: async (...args) => calls.push(args),
        ChatSend: async (...args) => calls.push(["send", ...args]),
      } } };
      ui.profiles = [{ id: "claude", name: "Claude", models: [] }];
      ui.settings = {};
      const pane = document.createElement("div");
      pane.style.cssText = "position:fixed;inset:50px 0 0;display:flex";
      document.body.append(pane);
      const tab = { id: 1, native: true, profile: "claude", pane };
      tabs.set(1, tab); active = 1;
      createChat(tab);
      hideWelcome(tab.chat, true);
      tab.chat.thread.innerHTML = '<p id="message">Select this conversation text</p><button id="control">Control</button><input id="other" aria-label="Other input">';
      setupChatFocus();
      document.body.classList.add("ready");
    });
    const prompt = page.locator(".chat textarea");
    await page.locator("#message").click();
    await page.keyboard.type("hello");
    assert.equal(await prompt.inputValue(), "hello", "click then type");
    await page.locator("#message").dblclick();
    assert.ok(await page.evaluate(() => getSelection().toString()), "double-click keeps selection");
    await page.keyboard.press("Control+c");
    assert.ok(await page.evaluate(() => getSelection().toString()), "copy keeps selection");
    await page.keyboard.type("!");
    assert.equal(await prompt.inputValue(), "hello!", "first character after selection reaches prompt");
    await page.locator("#other").fill("other");
    assert.equal(await prompt.inputValue(), "hello!", "other input keeps focus");
    await page.locator("#control").focus();
    await page.keyboard.press("Space");
    assert.equal(await page.evaluate(() => document.activeElement.id), "control", "keyboard controls keep focus");
    await page.evaluate(() => { document.querySelector("#historyPanel").hidden = false; document.querySelector("#hq").focus(); });
    await page.keyboard.type("search");
    assert.equal(await page.locator("#hq").inputValue(), "search", "history search keeps input");
    const hist = await page.evaluate(() => {
      const now = Date.now() / 1000;
      hItems = [
        { provider: "claude", providerName: "Claude", title: "small new", folder: "~", updated: now, size: 300 * 1024 },
        { provider: "claude", providerName: "Claude", title: "big old", folder: "~", updated: now - 86400 * 9, size: 120 * 1024 * 1024 },
      ];
      $("#hq").value = "";
      renderHistory();
      return { sections: [...document.querySelectorAll(".hsection")].map((e) => e.textContent), rows: [...document.querySelectorAll(".hrow")].map((r) => r.querySelector("b").textContent + " " + r.querySelector(".size").textContent) };
    });
    assert.deepEqual(hist.sections, ["Important chats", "Past chats"], "history has important and past chats");
    assert.deepEqual(hist.rows, ["big old 120 MB", "small new 300 KB"], "big chats come first, each with its size");
    await page.evaluate(() => { document.querySelector("#hq").value = "search"; });
    await page.evaluate(() => { document.querySelector("#historyPanel").hidden = true; handoverProgress(tabs.get(1), "reading"); });
    await prompt.focus();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Enter");
    assert.deepEqual(await page.evaluate(() => calls), [], "reading cannot be interrupted or receive new work");
    assert.equal(await page.locator("progress").getAttribute("value"), null, "reading progress is indeterminate");
    await page.evaluate(() => handoverProgress(tabs.get(1), "complete"));
    assert.equal(await page.locator("progress").getAttribute("value"), "1", "completion fills progress bar");
    await page.keyboard.press("Escape");
    assert.deepEqual(await page.evaluate(() => calls), [[1, "interrupt"]], "normal work can be interrupted");
    await page.evaluate(() => restoreModel(tabs.get(1), "claude-opus-5-5"));
    assert.match(await page.locator(".cm-name").textContent(), /Opus 5.5/);
    await page.evaluate(() => handoverProgress(tabs.get(1), "failed"));
    assert.equal(await page.locator(".c-send").isEnabled(), true, "failure unlocks composer");
    await page.evaluate(() => { order = [1, 2, 3]; window.activated = []; activate = (id) => activated.push(id); });
    await page.locator(".field textarea").fill("");
    for (const key of ["Control+2", "Control+9", "Control+1", "Control+5"]) await page.keyboard.press(key);
    assert.deepEqual(await page.evaluate(() => activated), [2, 3, 1], "Ctrl+digit jumps to tabs");
    assert.equal(await page.locator(".field textarea").inputValue(), "", "Ctrl+digit types nothing");
    const snap = await page.evaluate(() => { tabs.get(1).chat.quota = { five: 0.32, week: 0.1 }; return traySnapshot(); });
    assert.equal(snap.ai, "Claude", "tray status names the AI");
    assert.equal(snap.rows.length, 2, "tray status carries usage");
    const panel = await browser.newPage({ viewport: { width: 300, height: 400 } });
    panel.on("pageerror", e => errors.push(e.message));
    const css = ["style.css", "chat.css"].map((f) => fs.readFileSync(path.join(frontend, f), "utf8")).join("\n");
    const page2 = fs.readFileSync(path.join(frontend, "panel.html"), "utf8").replace("/*AIT_CSS*/", css)
      .replace("<script>", '<script>window.posted = []; window.chrome = { webview: { postMessage: (m) => posted.push(m) } };</script><script>');
    await panel.setContent(page2);
    await panel.evaluate((s) => update(s), snap);
    assert.equal(await panel.locator(".mn-ai").textContent(), "Claude", "tray panel names the AI");
    assert.equal(await panel.locator(".mn-m").count(), 2, "tray panel shows usage");
    await panel.evaluate((s) => update({ ...s, tab: 1, canSend: true, reply: "Fixed the bug in app.go.", ask: { req: "r9", always: false, title: "Claude wants to run a command", detail: "go test" } }), snap);
    assert.equal(await panel.locator(".mn-reply").textContent(), "Fixed the bug in app.go.", "tray panel shows the latest reply");
    await panel.fill(".mn-input textarea", "run the tests");
    await panel.press(".mn-input textarea", "Enter");
    await panel.click('.mn-ask [data-d="allow"]');
    await panel.click('[data-a="hide"]');
    const posted = await panel.evaluate(() => posted);
    assert.ok(posted.some((m) => m.startsWith("h:")) && posted.includes("hide"), "tray panel reports its height and actions");
    assert.ok(posted.includes('send:{"tab":1,"text":"run the tests"}'), "a prompt typed in the panel is sent");
    assert.ok(posted.includes('ask:{"tab":1,"req":"r9","d":"allow"}'), "a permission can be answered from the panel");
    await page.evaluate(() => Object.assign(go.main.App, { McpOff: async () => ["elevenlabs"], McpToggle: async (...a) => calls.push(["mcp", ...a]) }));
    await page.click(".c-mcp");
    await page.evaluate(() => chatEvents(tabs.get(1), [{ k: "mcp", servers: [{ name: "elevenlabs", status: "connected", tools: 27 }, { name: "docs", status: "connected", tools: 8 }] }], true));
    await page.waitForSelector('.mcp-row .switch[data-name="docs"]');
    assert.equal(await page.locator(".mcp-row").count(), 2, "MCP list shows the AI's servers");
    assert.equal(await page.locator('.switch[data-name="elevenlabs"]').getAttribute("aria-checked"), "false", "a server switched off in AIT shows as off");
    await page.click('.switch[data-name="docs"]');
    assert.ok((await page.evaluate(() => calls)).some((c) => c[0] === "mcp" && c[2] === "docs" && c[3] === false), "a server can be switched off");
    await page.evaluate(() => closeModelPop());
    const locked = await page.evaluate(() => {
      const tab = tabs.get(1), c = tab.chat;
      calls.length = 0;
      chatLoading(tab, true, 987 * 1024 * 1024);
      c.ta.disabled = false; c.ta.value = "too early"; submit(tab); // even if the box were usable
      const r = { overlay: !!c.root.querySelector(".chat-loading"), text: c.root.querySelector(".cl-card span").textContent, sendOff: c.root.querySelector(".c-send").disabled, sent: calls.length, panelCanSend: traySnapshot().canSend };
      chatLoading(tab, false);
      c.ta.value = "";
      return { ...r, after: !c.root.querySelector(".chat-loading") && !c.ta.disabled && !c.root.querySelector(".c-send").disabled };
    });
    assert.deepEqual(locked, { overlay: true, text: "987 MB · this one is big, it can take a moment", sendOff: true, sent: 0, panelCanSend: false, after: true }, "a loading chat shows a loading screen and takes no input");
    await page.evaluate(() => {
      go.main.App.History = async () => [{ provider: "claude", providerName: "Claude", title: "huge", ref: "r", updated: Date.now() / 1000, size: 987 * 1024 * 1024 }];
      const pane = document.createElement("div"); document.body.append(pane);
      const t2 = { id: 7, native: true, profile: "claude", pane }; tabs.set(7, t2); createChat(t2);
    });
    await page.waitForSelector(".w-tip .w-rt");
    await page.evaluate(() => [...document.querySelectorAll(".w-tip")].find((b) => b.textContent.includes("huge")).click());
    assert.equal(await page.locator("#confirm").isVisible(), true, "a big recent chat warns before opening");
    assert.match(await page.locator("#ctitle").textContent(), /987 MB/);
    await page.evaluate(() => { settle(false); tabs.get(7).pane.remove(); tabs.delete(7); });
    const failed = await page.evaluate(async () => {
      Object.assign(go.main.App, { ChatHistory: async () => { throw "transcript unreadable"; }, CanReview: async () => false });
      const pane = document.createElement("div"); document.body.append(pane);
      const t3 = { id: 9, native: true, profile: "claude", pane }; tabs.set(9, t3); createChat(t3);
      chatLoading(t3, true, 5000);
      chatOpened(t3, { folder: "~" }, true, false);
      await new Promise((r) => setTimeout(r, 50));
      const r = { loading: !!t3.chat.root.querySelector(".chat-loading"), error: t3.chat.thread.textContent.includes("Could not load this conversation: transcript unreadable") };
      pane.remove(); tabs.delete(9);
      return r;
    });
    assert.deepEqual(failed, { loading: false, error: true }, "a failed history load says so instead of looking ready");
    const replayed = await page.evaluate(async () => {
      const pane = document.createElement("div"); document.body.append(pane);
      const t4 = { id: 11, native: true, profile: "claude", pane }; tabs.set(11, t4); createChat(t4);
      const evs = [];
      for (let i = 0; i < 1000; i++) evs.push({ k: "user", text: "q" + i }, { k: "msg", id: "m" + i }, { k: "text", text: "a" + i }, { k: "done" });
      chatLoading(t4, true, 1);
      await replay(t4, evs);
      const r = { turns: t4.chat.thread.querySelectorAll(".turn").length, bar: t4.chat.loadingEl.querySelector("progress").value, shown: t4.chat.scroll.style.display === "" };
      chatLoading(t4, false); pane.remove(); tabs.delete(11);
      return r;
    });
    assert.deepEqual(replayed, { turns: 2000, bar: 1, shown: true }, "a long history is drawn in slices, all of it, with the bar full at the end");
    await page.evaluate(() => {
      const pane = document.createElement("div"); pane.style.cssText = "position:fixed;inset:0;display:flex"; document.body.append(pane);
      const t5 = { id: 12, native: true, profile: "claude", pane }; tabs.set(12, t5); createChat(t5); hideWelcome(t5.chat, true);
      chatEvents(t5, [{ k: "user", text: "make the Tray icon" }, { k: "msg", id: "x" }, { k: "text", text: "The tray icon is done. Tray tooltip too." }, { k: "done" }], false);
      openFind(t5);
    });
    await page.keyboard.type("tray");
    const found = await page.evaluate(() => tabs.get(12).chat.find.bar.querySelector(".fb-n").textContent);
    await page.keyboard.press("Enter");
    const stepped = await page.evaluate(() => [tabs.get(12).chat.find.bar.querySelector(".fb-n").textContent, CSS.highlights.get("find-now").size]);
    await page.keyboard.press("Escape");
    const closed = await page.evaluate(() => { const r = [tabs.get(12).chat.find.bar.hidden, CSS.highlights.has("find")]; tabs.get(12).pane.remove(); tabs.delete(12); return r; });
    assert.equal(found, "3 of 3", "find counts every match, case-insensitive, starting at the latest");
    assert.deepEqual(stepped, ["1 of 3", 1], "Enter steps to the next match and highlights it");
    assert.deepEqual(closed, [true, false], "Esc closes find and clears the highlights");
    const times = await page.evaluate(() => {
      const old = new Date(2026, 0, 5, 14, 30).getTime() / 1000;
      return [msgTime(old), msgTime(0) === new Date().toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })];
    });
    assert.match(times[0], /Jan/, "a reopened message from another day shows its date");
    assert.equal(times[1], true, "a new message shows just the time");
    const reopened = await page.evaluate(async () => {
      const keep = { openTab, activate, profiles: ui.profiles };
      const opened = [], fronts = [];
      ui.profiles = [{ id: "claude", name: "Claude", installed: true, models: [] }, { id: "codex", name: "ChatGPT", installed: false, models: [] }];
      go.main.App.LastTabs = async () => [
        { provider: "claude", ref: "a", size: 2000 },
        { provider: "codex", ref: "gone-ai", size: 10 },
        { provider: "claude", ref: "big", size: 300 * 1024 * 1024, active: true },
      ];
      openTab = async (p, extra) => { opened.push([p, extra.chat, extra.summary]); return { id: 100 + opened.length }; };
      activate = (id) => fronts.push(id);
      const any = await reopenTabs();
      go.main.App.LastTabs = async () => [];
      const none = await reopenTabs();
      ({ openTab, activate } = keep); ui.profiles = keep.profiles;
      return { opened, fronts, any, none };
    });
    assert.deepEqual(reopened, { opened: [["claude", "a", false], ["claude", "big", true]], fronts: [102], any: true, none: false },
      "last time's tabs reopen in order, the active one in front, big ones with their summary, skipping AIs that are gone");
    const draft = await page.evaluate(async () => {
      const keep = { openTab, profiles: ui.profiles };
      ui.profiles = [{ id: "claude", name: "Claude", installed: true, models: [] }];
      const saved = [];
      go.main.App.SetDraft = async (id, text) => saved.push([id, text]);
      go.main.App.LastTabs = async () => [{ provider: "claude", ref: "a", size: 1, draft: "half a prompt" }];
      const pane = document.createElement("div"); document.body.append(pane);
      const t6 = { id: 13, native: true, profile: "claude", pane }; tabs.set(13, t6); createChat(t6);
      openTab = async () => t6;
      await reopenTabs();
      go.main.App.ExportChat = async () => "x.md";
      t6.chat.ta.value = "/export"; submit(t6); // a command clears the box, and so the saved draft
      await new Promise((r) => setTimeout(r, 500));
      const afterCommand = saved.pop();
      t6.chat.ta.value = "half a prompt more"; t6.chat.ta.dispatchEvent(new Event("input"));
      await new Promise((r) => setTimeout(r, 500));
      if (afterCommand?.[1] !== "") throw new Error("draft not cleared after a command: " + JSON.stringify(afterCommand));
      ({ openTab } = keep); ui.profiles = keep.profiles;
      const r = [t6.chat.ta.value, saved.pop()];
      pane.remove(); tabs.delete(13);
      return r;
    });
    assert.deepEqual(draft, ["half a prompt more", [13, "half a prompt more"]], "a reopened tab gets its unsent text back, and edits keep being saved");
    const copied = await page.evaluate(() => {
      const pane = document.createElement("div"); document.body.append(pane);
      const t7 = { id: 14, native: true, profile: "claude", pane }; tabs.set(14, t7); createChat(t7); hideWelcome(t7.chat, true);
      chatEvents(t7, [{ k: "user", text: "q" }, { k: "msg", id: "m" }, { k: "start", i: 0, type: "text" }, { k: "delta", i: 0, text: "**Done:** the tray" }, { k: "stop", i: 0 }, { k: "done" }], false);
      const clip = [];
      const keep = window.runtime;
      window.runtime = { ...(keep || {}), ClipboardSetText: (s) => clip.push(s) };
      const btn = t7.chat.thread.querySelector(".turn-actions button");
      btn?.click();
      window.runtime = keep;
      const r = [clip[0], btn?.lastElementChild.textContent];
      pane.remove(); tabs.delete(14);
      return r;
    });
    assert.deepEqual(copied, ["**Done:** the tray", "Copied"], "a finished reply can be copied as its Markdown");
    const review = await page.evaluate(() => {
      const pane = document.createElement("div"); document.body.append(pane);
      const t8 = { id: 15, native: true, profile: "claude", pane }; tabs.set(15, t8); createChat(t8); hideWelcome(t8.chat, true);
      chatEvents(t8, [{ k: "msg", id: "m" }, { k: "start", i: 1, type: "text" }, { k: "delta", i: 1, text: "[[AIT_SUPEREVIEW]]" }, { k: "stop", i: 1 }, { k: "done" }], false);
      const keepActive = active; active = 15;
      reviewState(t8, "start", "ChatGPT");
      reviewStep(t8, "Read app.go");
      const during = { marker: t8.chat.thread.textContent.includes("AIT_SUPEREVIEW"), bar: !!t8.chat.thread.querySelector(".review-card progress:not([value])"), busy: t8.chat.busy, tray: traySnapshot().state, step: t8.chat.reviewEl.querySelector(".review-step").textContent };
      reviewState(t8, "done", "ChatGPT", "Fix the edge case.");
      const md = (reviewState(t8, "done", "ChatGPT", "**Bold** [file](https://example.com/x)"), t8.chat.reviewEl.querySelector(".review-body"));
      const opened = []; const keepRT = window.runtime; window.runtime = { ...(keepRT || {}), BrowserOpenURL: (u) => opened.push(u) };
      md.querySelector("a").click(); window.runtime = keepRT;
      const rendered = { bold: !!md.querySelector("strong"), opened };
      reviewState(t8, "done", "ChatGPT", "Fix the edge case.");
      const after = { title: t8.chat.reviewEl.querySelector("b").textContent, bar: t8.chat.reviewEl.querySelector("progress").value, feedback: t8.chat.reviewEl.querySelector(".review-body").textContent.trim() };
      active = keepActive; pane.remove(); tabs.delete(15);
      return { during, after, rendered };
    });
    assert.deepEqual(review.during, { marker: false, bar: true, busy: true, tray: "Another AI is reviewing the work", step: "Read app.go" }, "a review shows progress, removes the marker reply and counts as work");
    assert.deepEqual(review.rendered, { bold: true, opened: ["https://example.com/x"] }, "review feedback renders markdown and opens links in the browser");
    assert.deepEqual(review.after, { title: "ChatGPT reviewed the work. The AI is acting on it.", bar: 1, feedback: "Fix the edge case." }, "a finished review shows its feedback");
    const keysheet = await page.evaluate(() => {
      openShortcuts();
      const r = { open: !$("#keysSheet").hidden, groups: [...document.querySelectorAll("#keysSheet h4")].map((h) => h.textContent), find: [...document.querySelectorAll("#keysSheet .ks-row")].some((x) => x.textContent.includes("Ctrl+F")) };
      closeShortcuts();
      return { ...r, closed: $("#keysSheet").hidden };
    });
    assert.deepEqual(keysheet, { open: true, groups: ["Tabs", "Chat", "Find and history", "Window"], find: true, closed: true }, "the shortcuts sheet lists every group");
    const layouts = await page.evaluate(() => {
      const ta = tabs.get(1).chat.ta, out = [];
      for (const init of [{ key: "/", code: "Slash", ctrlKey: true }, { key: "/", code: "Digit7", ctrlKey: true, shiftKey: true }]) {
        ta.dispatchEvent(new KeyboardEvent("keydown", { ...init, bubbles: true, cancelable: true }));
        out.push(!$("#keysSheet").hidden);
        closeShortcuts();
      }
      return out;
    });
    assert.deepEqual(layouts, [true, true], "Ctrl+/ opens the shortcuts on US and German keyboards");
    const sup = await page.evaluate(async () => {
      go.main.App.ToggleReview = async () => true;
      runLocal(tabs.get(1), "/supereview");
      await new Promise((r) => setTimeout(r, 50));
      return [reviewOn, [...tabs.get(1).chat.thread.querySelectorAll(".sysline")].pop().textContent];
    });
    assert.equal(sup[0], true, "/supereview switches review on");
    assert.match(sup[1], /Supereview on/, "/supereview says it is on, and does nothing else");
    const mark = await page.evaluate(() => { const t = tabs.get(1); t.chat.reviewAvailable = true; renderStatus(t); const m = t.chat.root.querySelector(".sl-review").textContent; reviewOn = false; renderStatus(t); return [m, t.chat.root.querySelector(".sl-review").textContent]; });
    assert.deepEqual(mark, ["reviewon", ""], "the status line shows when supereview is on");
    // The page's own start-up (not run here) wires #calt to settle("alt").
    const choice = await page.evaluate(() => {
      const b = document.querySelector("#calt");
      b.addEventListener("click", () => settle("alt"));
      const p = ask("This chat is big (987 MB)", "…", "Open with summary", "Show everything");
      const label = !b.hidden && b.textContent;
      b.click();
      return p.then((v) => [v, label]);
    });
    assert.deepEqual(choice, ["alt", "Show everything"], "the big-chat warning offers a third choice");
    await page.evaluate(() => chatEvents(tabs.get(1), [{ k: "summary", text: "This session is being continued from a previous conversation.\n\nSummary:\n1. Built the **map**" }], false));
    assert.match(await page.locator(".summary-card .md").innerHTML(), /<strong>map<\/strong>/, "the AI's summary is shown as a card");
    assert.doesNotMatch(await page.locator(".summary-card .md").textContent(), /being continued/, "the card drops the continuation boilerplate");
    const snap2 = await page.evaluate(() => traySnapshot());
    assert.equal(snap2.canSend, true, "a native chat can take messages from the panel");
    await panel.close();
    await page.evaluate(() => {
      window.savedRules = [];
      Object.assign(go.main.App, { GetRules: async () => ({ text: "old", presets: [{ name: "A", desc: "", text: "rule a" }, { name: "B", desc: "", text: "" }] }), SaveRules: async (t) => savedRules.push(t) });
      return openRules();
    });
    assert.equal(await page.locator(".rl-text").inputValue(), "old", "rules editor shows the saved rules");
    await page.click(".rl-pre >> nth=0");
    await page.click(".rl-save");
    assert.deepEqual(await page.evaluate(() => savedRules), ["rule a"], "a preset can be picked and saved");
    await page.evaluate(() => closeRules());
    await page.evaluate(() => {
      window.installed = null; const handlers = {};
      window.runtime = { ...(window.runtime || {}), EventsOn: (n, cb) => { handlers[n] = cb; return () => {}; } };
      Object.assign(go.main.App, {
        Agents: async () => [{ id: "claude", name: "Claude", installed: true, signedIn: true }, { id: "codex", name: "ChatGPT", installed: false }, { id: "gemini", name: "Gemini", installed: false }],
        InstallAgents: async (ids) => { installed = ids; for (const id of ids) handlers["install:progress"](id, id === "codex" ? "done" : "failed", "no network"); },
        SaveSettings: async () => {},
      });
      window.setupDone = runSetup();
    });
    for (let i = 0; i < 4; i++) await page.click(".su-next");
    await page.waitForSelector(".su-agent .switch");
    assert.match(await page.locator(".su-next").textContent(), /Install and start/, "missing tools are offered for install");
    await page.click(".su-agent:nth-child(3) .switch"); // skip Gemini
    await page.click(".su-next");
    await page.waitForFunction(() => installed !== null);
    assert.deepEqual(await page.evaluate(() => installed), ["codex"], "only the chosen tools install");
    assert.match(await page.locator(".su-agent:nth-child(2) .su-state").textContent(), /Installed/);
    assert.match(await page.locator(".su-next").textContent(), /Start using AIT/);
    await page.click(".su-next");
    await page.evaluate(() => setupDone);
    assert.deepEqual(errors, []);
    console.log("Chat UI: focus, selection, controls, handover progress, Escape, model display, tab keys, tray panel, MCP list, rules editor, and tool installs passed.");
  } finally {
    await browser.close();
  }
})().catch(err => { console.error(err); process.exitCode = 1; });
