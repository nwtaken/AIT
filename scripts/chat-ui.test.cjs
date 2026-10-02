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
    for (const file of ["vendor/marked.js", "app.js", "chat.js", "setup.js"]) {
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
    assert.deepEqual(errors, []);
    console.log("Chat UI: focus, selection, controls, handover progress, Escape, and model display passed.");
  } finally {
    await browser.close();
  }
})().catch(err => { console.error(err); process.exitCode = 1; });
