// End-to-end test of the AIs' browser gate: gate.cjs + Playwright's MCP server in
// a real headless Chrome/Edge, against a fake AIT server. (AIT's own browser
// surface is tested in browserview_manual_test.go.)
// Needs Node, Chrome or Edge, and @playwright/mcp in AIT's browser folder
// (AIT installs it itself); skipped when any is missing.
const assert = require("assert");
const fs = require("fs");
const http = require("http");
const os = require("os");
const path = require("path");
const { spawn } = require("child_process");

const dir = path.join(process.env.APPDATA || "", "AIT", "browser");
const cli = path.join(dir, "node_modules", "@playwright", "mcp", "cli.js");
const exe = [
  [process.env.ProgramFiles, "Google/Chrome/Application/chrome.exe"], [process.env["ProgramFiles(x86)"], "Google/Chrome/Application/chrome.exe"],
  [process.env.LocalAppData, "Google/Chrome/Application/chrome.exe"], [process.env["ProgramFiles(x86)"], "Microsoft/Edge/Application/msedge.exe"],
  [process.env.ProgramFiles, "Microsoft/Edge/Application/msedge.exe"],
].map(([d, f]) => d && path.join(d, f)).find((p) => p && fs.existsSync(p));
if (!fs.existsSync(cli) || !exe) { console.log("Browser test skipped: Playwright MCP or Chrome/Edge not found."); process.exit(0); }

const KEY = "testkey";
const asked = [], helped = [];
let opened = 0;
const answers = { "example.com": "allow", "example.net": "deny", "example.org": "deny" };
let base = "";
const fake = http.createServer((req, res) => {
  const u = new URL(req.url, "http://x");
  if (u.searchParams.get("key") !== KEY) { res.writeHead(404); return res.end(); }
  if (u.pathname === "/open") { opened++; return res.end("ok"); }
  if (u.pathname === "/help") { const m = u.searchParams.get("message"); helped.push(m); return setTimeout(() => res.end(m.includes("skip") ? "deny" : "allow"), 150); }
  if (u.pathname === "/ask") { asked.push(u.searchParams.get("host")); return res.end(answers[u.searchParams.get("host")] || "deny"); }
  res.writeHead(404); res.end();
});
// A "dev server" on localhost, with a link to a site that is not allowed.
const dev = http.createServer((req, res) => { res.setHeader("content-type", "text/html"); res.end('<title>dev</title><a id="out" href="https://example.net/">leave</a>'); });

(async () => {
  await new Promise((r) => fake.listen(0, "127.0.0.1", r));
  await new Promise((r) => dev.listen(0, "127.0.0.1", r));
  base = "http://127.0.0.1:" + fake.address().port;
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "aitbrowser-"));
  fs.writeFileSync(path.join(tmp, "config.json"), JSON.stringify({
    browser: { browserName: "chromium", isolated: true, launchOptions: { executablePath: exe, headless: true }, contextOptions: { viewport: null } },
    outputDir: path.join(tmp, "out"),
  }));
  const gate = spawn(process.execPath, [path.join(__dirname, "..", "browserassets", "gate.cjs"), "--ait", base, "--key", KEY, "--cli", cli, "--config", path.join(tmp, "config.json")], { stdio: ["pipe", "pipe", "inherit"] });
  let buf = "", id = 0;
  const waits = {};
  gate.stdout.on("data", (d) => { buf += d; let i; while ((i = buf.indexOf("\n")) >= 0) { const l = buf.slice(0, i); buf = buf.slice(i + 1); try { const m = JSON.parse(l); if (m.id && waits[m.id]) waits[m.id](m); } catch {} } });
  const rpc = (method, params) => new Promise((r) => { const n = ++id; waits[n] = r; gate.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: n, method, params }) + "\n"); });
  const tool = async (name, args) => { const r = await rpc("tools/call", { name, arguments: args }); return { err: !!r.result.isError, text: (r.result.content || []).map((c) => c.text || "").join("\n") }; };
  const finish = (code) => { try { gate.kill(); fake.close(); dev.close(); } catch {} process.exit(code); };
  setTimeout(() => { console.log("browser test timed out"); finish(1); }, 150000);

  try {
    await rpc("initialize", { protocolVersion: "2024-11-05", capabilities: {}, clientInfo: { name: "test", version: "1" } });
    gate.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n");

    const names = (await rpc("tools/list", {})).result.tools.map((t) => t.name);
    assert.equal(opened, 0, "AIT's browser is not started by listing the tools");
    assert.ok(names.includes("browser_navigate") && names.includes("browser_click"), "the browser tools are offered");
    for (const hidden of ["browser_run_code_unsafe", "browser_file_upload", "browser_drop"]) assert.ok(!names.includes(hidden), hidden + " is hidden");
    assert.ok((await tool("browser_run_code_unsafe", { code: "1" })).err, "a hidden tool cannot be called");
    assert.equal(opened, 0, "and a refused call does not start it either");

    assert.ok(names.includes("browser_ask_user"), "the AI can ask the user for help");
    const done = await tool("browser_ask_user", { message: "Solve the CAPTCHA" });
    assert.ok(!done.err && /finished/.test(done.text) && helped[0] === "Solve the CAPTCHA", "asking the user waits for them and reports it: " + done.text);
    assert.equal(opened, 1, "the browser starts on the first call");
    const skipped = await tool("browser_ask_user", { message: "please skip this" });
    assert.ok(skipped.err && /skipped/.test(skipped.text), "a step the user skips is reported as such: " + skipped.text);

    const a = await tool("browser_navigate", { url: "https://example.com" });
    assert.ok(!a.err && /Page URL: https:\/\/example\.com/.test(a.text), "an allowed site opens: " + a.text.slice(0, 120));
    assert.deepEqual(asked, ["example.com"], "the first visit asks");
    await tool("browser_navigate", { url: "https://www.example.com/" });
    assert.deepEqual(asked, ["example.com"], "an allowed site is not asked again, with or without www");
    assert.equal(opened, 1, "the browser is started once");

    const b = await tool("browser_navigate", { url: "https://example.org" });
    assert.ok(b.err && /did not allow example\.org/.test(b.text), "a refused site is blocked: " + b.text);
    assert.ok(asked.includes("example.org"));

    const c = await tool("browser_navigate", { url: "file:///C:/Windows/win.ini" });
    assert.ok(c.err && /not allowed/.test(c.text), "local files are blocked: " + c.text);

    const askedBefore = asked.length;
    const d = await tool("browser_navigate", { url: `http://localhost:${dev.address().port}/` });
    assert.ok(!d.err && asked.length === askedBefore, "a local dev server opens without asking: " + d.text.slice(0, 100));
    const e = await tool("browser_evaluate", { function: "() => { document.getElementById('out').click(); return 'clicked'; }" });
    assert.ok(e.err && /did not allow example\.net/.test(e.text), "a click that leads to a refused site is caught: " + e.text);
    const where = await tool("browser_evaluate", { function: "() => location.href" });
    assert.ok(/about:blank/.test(where.text), "and the page is sent back to a blank page: " + where.text);

    console.log("Browser gate: hidden tools, site questions, local files, dev servers, redirects by click, asking you for help and starting on first use passed.");
    finish(0);
  } catch (err) {
    console.log("FAILED:", err.message);
    finish(1);
  }
})();
