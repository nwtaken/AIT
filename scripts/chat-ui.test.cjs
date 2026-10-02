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
    await panel.click('[data-a="hide"]');
    const posted = await panel.evaluate(() => posted);
    assert.ok(posted.some((m) => m.startsWith("h:")) && posted.includes("hide"), "tray panel reports its height and actions");
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
    console.log("Chat UI: focus, selection, controls, handover progress, Escape, model display, tab keys, tray panel, rules editor, and tool installs passed.");
  } finally {
    await browser.close();
  }
})().catch(err => { console.error(err); process.exitCode = 1; });
