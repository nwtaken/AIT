// AIT's browser gate. It sits between an AI and Playwright's MCP server and
// passes everything through, except:
//  - the browser itself (a window of AIT's) starts on the first call, and is
//    brought back from the taskbar before a screenshot and closed (for the
//    next call to reopen) when the AI calls browser_close;
//  - the first visit to a website asks the user (through AIT) before the
//    page loads, whether the AI typed the address or a click led there;
//  - tools that could read local files or run arbitrary code are not offered;
//  - one tool is added: browser_ask_user, with which the AI hands a step it
//    cannot do (a CAPTCHA, a login) to the user and waits until they are done.
// Usage: node gate.cjs --ait <url> --key <key> --cli <playwright cli.js> --config <config.json>
"use strict";
const { spawn } = require("child_process");
const http = require("http");
const readline = require("readline");

const args = process.argv.slice(2);
const opt = (n) => { const i = args.indexOf(n); return i < 0 ? "" : args[i + 1]; };
const BASE = opt("--ait"), KEY = opt("--key"), CLI = opt("--cli"), CONFIG = opt("--config");

const HIDDEN = new Set(["browser_run_code_unsafe", "browser_file_upload", "browser_drop"]);
const LOCAL = /^(localhost|127(\.\d+){3}|\[::1\]|::1)$|\.localhost$/;
const HELP_TOOL = {
  name: "browser_ask_user",
  description: "Ask the user to do something in the browser window that you cannot do yourself: a CAPTCHA, a sign-in or 2FA, a payment, anything only a human should do. " +
    "The user sees your message in the browser's side bar and in the chat, does the step in the window, and presses Done. This waits for them (up to 15 minutes). " +
    "Never try to get past a CAPTCHA or enter credentials yourself. After it returns, take a fresh snapshot.",
  inputSchema: { type: "object", properties: { message: { type: "string", description: "What the user should do, in one or two plain sentences." } }, required: ["message"] },
};
const ASKED = new Map(); // host -> true (allowed) | expiry time (denied, remembered briefly)

const child = spawn(process.execPath, [CLI, "--config", CONFIG], { stdio: ["pipe", "pipe", "inherit"], windowsHide: true });
child.on("exit", (code) => process.exit(code || 0));
const toChild = (m) => child.stdin.write((typeof m === "string" ? m : JSON.stringify(m)) + "\n");
const toAI = (m) => process.stdout.write((typeof m === "string" ? m : JSON.stringify(m)) + "\n");
const refuse = (id, text) => toAI({ jsonrpc: "2.0", id, result: { isError: true, content: [{ type: "text", text }] } });

// What a page address needs: null = nothing, a host = the user must allow it,
// "" with blocked = never.
function judge(raw) {
  let u;
  try { u = new URL(raw); } catch { try { u = new URL("https://" + raw); } catch { return { blocked: "That is not a web address." }; } }
  if (u.protocol === "about:") return {};
  if (u.protocol !== "http:" && u.protocol !== "https:") return { blocked: `${u.protocol} addresses are not allowed in AIT's browser.` };
  if (LOCAL.test(u.hostname)) return {};
  return { host: u.hostname.toLowerCase().replace(/^www\./, "") };
}

// Asks AIT something that a person answers; resolves true for "allow".
function ask(path, query) {
  return new Promise((resolve) => {
    const req = http.get(`${BASE}/${path}?key=${KEY}&${query}`, (res) => {
      let b = "";
      res.on("data", (d) => (b += d));
      res.on("end", () => resolve(b.trim() === "allow"));
    });
    req.setTimeout(0);
    req.on("error", () => resolve(false));
  });
}

async function allowed(host) {
  const known = ASKED.get(host);
  if (known === true) return true;
  if (known && known > Date.now()) return false;
  const ok = await ask("ask", "host=" + encodeURIComponent(host));
  ASKED.set(host, ok ? true : Date.now() + 120000);
  return ok;
}

const denied = (host) => `The user did not allow ${host}. Do not try that site again or work around it; tell the user instead.`;

// The browser surface starts when the AI first uses it; AIT answers once it is up.
let opening = null;
const open = () => opening || (opening = new Promise((resolve) => {
  http.get(`${BASE}/open?key=${KEY}`, (res) => { res.resume(); res.on("end", () => resolve(res.statusCode === 200)); }).on("error", () => resolve(false));
}).then((ok) => { if (!ok) opening = null; return ok; }));

const front = () => new Promise((resolve) => {
  http.get(`${BASE}/front?key=${KEY}`, (res) => { res.resume(); res.on("end", resolve); }).on("error", resolve);
});

// Tells AIT the AI closed its browser; the next call opens a new window.
const closeWin = () => new Promise((resolve) => {
  http.get(`${BASE}/close?key=${KEY}`, (res) => { res.resume(); res.on("end", resolve); }).on("error", resolve);
}).then(() => { opening = null; });

const closes = new Set(); // ids of browser_close calls in flight
const calls = new Set(); // ids of tools/call requests in flight
const lists = new Set(); // ids of tools/list requests in flight
let own = 0;

readline.createInterface({ input: process.stdin }).on("line", async (line) => {
  let m;
  try { m = JSON.parse(line); } catch { return toChild(line); }
  if (m.method === "tools/list") lists.add(m.id);
  if (m.method === "tools/call") {
    const name = m.params && m.params.name, a = (m.params && m.params.arguments) || {};
    if (HIDDEN.has(name)) return refuse(m.id, "That tool is not available in AIT's browser.");
    if (!(await open())) return refuse(m.id, "AIT could not start its browser. Tell the user.");
    if (name === "browser_take_screenshot") await front(); // a minimised window does not draw
    if (name === "browser_ask_user") {
      const text = String(a.message || "Help with this step").slice(0, 300);
      if (await ask("help", "message=" + encodeURIComponent(text)))
        return toAI({ jsonrpc: "2.0", id: m.id, result: { content: [{ type: "text", text: "The user finished that step in the browser. Take a fresh snapshot before you continue." }] } });
      return refuse(m.id, "The user skipped that step. Stop and tell them what is blocked.");
    }
    if ((name === "browser_navigate" || name === "browser_tabs") && typeof a.url === "string") {
      const j = judge(a.url);
      if (j.blocked) return refuse(m.id, j.blocked);
      if (j.host && !(await allowed(j.host))) return refuse(m.id, denied(j.host));
    }
    if (name === "browser_close") closes.add(m.id);
    calls.add(m.id);
  }
  toChild(line);
});
process.stdin.on("end", () => child.stdin.end());

readline.createInterface({ input: child.stdout }).on("line", async (line) => {
  let m;
  try { m = JSON.parse(line); } catch { return toAI(line); }
  if (typeof m.id === "string" && m.id.startsWith("ait-gate-")) return; // our own call
  if (lists.delete(m.id) && m.result && Array.isArray(m.result.tools)) {
    m.result.tools = m.result.tools.filter((t) => !HIDDEN.has(t.name)).concat(HELP_TOOL);
    return toAI(m);
  }
  if (closes.delete(m.id)) await closeWin(); // Playwright has let go: AIT closes the window
  if (calls.delete(m.id) && m.result && Array.isArray(m.result.content)) {
    // A click or script can lead somewhere new: the AI must not see a page
    // the user has not allowed.
    for (const c of m.result.content) {
      const hit = c && typeof c.text === "string" && /Page URL:\s*(\S+)/.exec(c.text);
      const j = hit && judge(hit[1]);
      if (j && j.host && !(await allowed(j.host))) {
        toChild({ jsonrpc: "2.0", id: "ait-gate-" + ++own, method: "tools/call", params: { name: "browser_navigate", arguments: { url: "about:blank" } } });
        return refuse(m.id, denied(j.host));
      }
    }
  }
  toAI(m);
});
