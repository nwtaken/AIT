"use strict";

const API = () => window.go.main.App;
const RT = () => window.runtime;
const $ = (s) => document.querySelector(s);

// Icons per profile. An agent AIT has no icon for gets the generic spark,
// so a new provider shows up correctly with no front-end change.
const ICONS = {
  claude: '<svg viewBox="0 0 16 16"><g stroke="#d97757" stroke-width="1.9" stroke-linecap="round"><path d="M8 1.6v3.6M8 10.8v3.6M1.6 8h3.6M10.8 8h3.6M3.5 3.5l2.5 2.5M10 10l2.5 2.5M12.5 3.5 10 6M6 10l-2.5 2.5"/></g></svg>',
  codex: '<svg viewBox="0 0 16 16"><circle cx="8" cy="8" r="6.6" fill="none" stroke="#10a37f" stroke-width="1.6"/><path d="M5.2 6.2 7 8l-1.8 1.8M8.4 10.2h2.6" stroke="#10a37f" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>',
  gemini: '<svg viewBox="0 0 16 16"><path d="M8 1c.6 3.6 3.4 6.4 7 7-3.6.6-6.4 3.4-7 7-.6-3.6-3.4-6.4-7-7 3.6-.6 6.4-3.4 7-7z" fill="#4796e3"/></svg>',
  agent: '<svg viewBox="0 0 16 16"><path d="M8 1.5c.5 3 3.5 6 6.5 6.5-3 .5-6 3.5-6.5 6.5-.5-3-3.5-6-6.5-6.5 3-.5 6-3.5 6.5-6.5z" fill="#a78bfa"/></svg>',
  powershell: '<svg viewBox="0 0 16 16"><path d="M3.2 2.5h11.6l-2.4 11H.8z" fill="#2671be"/><path d="M4.6 5.4 7.5 8l-3.6 2.7" stroke="#fff" stroke-width="1.3" fill="none" stroke-linecap="round" stroke-linejoin="round"/><path d="M7.4 10.8h3" stroke="#fff" stroke-width="1.3" stroke-linecap="round"/></svg>',
  cmd: '<svg viewBox="0 0 16 16"><rect x="1.5" y="2.5" width="13" height="11" rx="1.5" fill="#1a1a1a" stroke="#8a8a8a"/><path d="m4 6 2.2 2L4 10" stroke="#e6e6e6" stroke-width="1.2" fill="none" stroke-linecap="round" stroke-linejoin="round"/><path d="M7.6 10.2h3.6" stroke="#e6e6e6" stroke-width="1.2" stroke-linecap="round"/></svg>',
};
const GLYPH = {
  plus: '<span class="mdl">&#xE710;</span>', gear: '<span class="mdl">&#xE713;</span>',
  history: '<span class="mdl">&#xE81C;</span>', attach: '<span class="mdl">&#xE723;</span>',
  rules: '<span class="mdl">&#xE70F;</span>',
};
const icon = (id) => ICONS[id] || ICONS.agent;
let APP_VERSION = "";

// Colour themes: the terminal palette, plus the page's own colours come from
// [data-theme] in style.css.
const ANSI = {
  black: "#0c0c0c", red: "#c50f1f", green: "#13a10e", yellow: "#c19c00",
  blue: "#0037da", magenta: "#881798", cyan: "#3a96dd", white: "#cccccc",
  brightBlack: "#767676", brightRed: "#e74856", brightGreen: "#16c60c", brightYellow: "#f9f1a5",
  brightBlue: "#3b78ff", brightMagenta: "#b4009e", brightCyan: "#61d6d6", brightWhite: "#f2f2f2",
};
const THEMES = {
  // Campbell — Windows Terminal's default scheme.
  campbell: { name: "Campbell", term: { ...ANSI, background: "#0c0c0c", foreground: "#cccccc", cursor: "#e8e8e8", cursorAccent: "#0c0c0c", selectionBackground: "#ffffff38",
    scrollbarSliderBackground: "#ffffff1c", scrollbarSliderHoverBackground: "#ffffff30", scrollbarSliderActiveBackground: "#ffffff44", overviewRulerBorder: "#0c0c0c" } },
  // The classic Windows PowerShell console blue.
  powershell: { name: "PowerShell blue", term: { ...ANSI, background: "#012456", foreground: "#eeedf0", cursor: "#fedba9", cursorAccent: "#012456", selectionBackground: "#fedba944",
    scrollbarSliderBackground: "#ffffff22", scrollbarSliderHoverBackground: "#ffffff38", scrollbarSliderActiveBackground: "#ffffff4c", overviewRulerBorder: "#012456" } },
};
THEMES.custom = { name: "Custom", term: { ...THEMES.campbell.term } };
const theme = () => THEMES[ui.theme] || THEMES.campbell;

let ui = { fontFamily: "Cascadia Mono", fontSize: 13, defaultProfile: "claude", profiles: [], theme: "campbell", renderer: "dom" };
const tabs = new Map();
let order = [];
let active = 0;
let nextId = 1;
const profile = (id) => ui.profiles.find((p) => p.id === id) || { id, name: id, agent: false };

// ---- tabs -------------------------------------------------------------------

async function openTab(profileId, extra = {}) {
  const prof = profile(profileId);
  if (prof.agent && prof.chat && ui.settings?.chatView !== "terminal") return openChatTab(prof, extra);
  const id = nextId++;
  const pane = document.createElement("div");
  pane.className = "pane";
  const host = document.createElement("div");
  host.className = "host";
  pane.append(host);
  $("#panes").append(pane);

  const term = new Terminal({
    fontFamily: `"${ui.fontFamily}", "Cascadia Mono", Consolas, "Segoe UI Symbol", monospace`,
    fontSize: ui.fontSize,
    fontWeight: ui.fontWeight || 500,
    fontWeightBold: Math.min(700, (ui.fontWeight || 500) + 200),
    lineHeight: 1.15,
    theme: theme().term,
    cursorBlink: true,
    cursorStyle: "bar",
    cursorWidth: 2,
    cursorInactiveStyle: "outline",
    scrollback: 9001,
    allowProposedApi: true,
    customGlyphs: true,
    rescaleOverlappingGlyphs: true,
    minimumContrastRatio: 1,
    windowsPty: { backend: "conpty" },
  });
  const fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  const uni = new Unicode11Addon.Unicode11Addon();
  term.loadAddon(uni);
  term.unicode.activeVersion = "11";
  term.loadAddon(new WebLinksAddon.WebLinksAddon((e, uri) => { if (e.ctrlKey) RT().BrowserOpenURL(uri); }));

  const tab = { id, profile: prof.id, agent: prof.agent, term, fit, pane, title: prof.name, account: "", seq: 0, exited: false, el: null };
  if (prof.agent) showVeil(tab, `Starting ${prof.name}`, "");
  tabs.set(id, tab);
  order.push(id);
  tab.el = makeTabEl(tab);
  tab.el.classList.add("enter");
  $("#tabs").append(tab.el);
  requestAnimationFrame(() => requestAnimationFrame(() => tab.el.classList.remove("enter")));

  term.open(host);
  // "dom" draws with the browser's own ClearType text — crisper at small
  // sizes than WebGL's greyscale glyph atlas. WebGL is faster for huge output.
  if (ui.renderer === "webgl") { try { term.loadAddon(new WebglAddon.WebglAddon()); } catch { /* DOM fallback */ } }
  tab.pred = [];
  activate(id);

  term.onData((d) => {
    if (tab.exited) { if (d === "\r") closeTab(id); return; }
    send(tab, d);
  });
  term.onTitleChange((s) => { tab.title = cleanTitle(s, prof); renderTab(tab); });
  term.onResize(({ cols, rows }) => { if (!tab.exited) API().Resize(id, cols, rows); });
  term.attachCustomKeyEventHandler((e) => keys(e, tab));
  host.addEventListener("contextmenu", (e) => { e.preventDefault(); rightClick(tab); });
  // Capture phase: files and images are ours, plain text goes on to xterm.
  host.addEventListener("paste", (e) => pasteEvent(e, tab), true);

  try {
    const info = await API().Open({ id, profile: prof.id, cols: term.cols, rows: term.rows, account: extra.account || "", chat: extra.chat || "", model: extra.model || "" });
    tab.account = info.account || "";
    if (info.backlog) {
      // A prewarmed agent: replay what it drew at its own size, then fit.
      term.resize(info.cols, info.rows);
      term.write(b64(info.backlog), () => { if (hasText(term)) liftVeil(tab); refit(tab); });
    }
    if (tab.veil) tab.veil.querySelector("small").textContent = (extra.chat ? "resuming · " : "") + "on " + tab.account;
  } catch (err) {
    liftVeil(tab);
    term.write(`\x1b[31m${err}\x1b[0m\r\n`);
    exited(tab, -1);
  }
  updateChrome();
  return tab;
}

// A native chat tab: no terminal, the conversation is drawn by chat.js.
async function openChatTab(prof, extra) {
  const id = nextId++;
  const pane = document.createElement("div");
  pane.className = "pane";
  $("#panes").append(pane);
  const tab = { id, profile: prof.id, agent: true, native: true, term: null, pane, title: prof.name, account: "", exited: false, el: null };
  tabs.set(id, tab);
  order.push(id);
  tab.el = makeTabEl(tab);
  tab.el.classList.add("enter");
  $("#tabs").append(tab.el);
  requestAnimationFrame(() => requestAnimationFrame(() => tab.el.classList.remove("enter")));
  createChat(tab);
  activate(id);
  try {
    const info = await API().Open({ id, profile: prof.id, cols: 120, rows: 40, account: extra.account || "", chat: extra.chat || "", model: extra.model || "" });
    if (extra.model) { const m = (prof.models || []).find((x) => x.id === extra.model); tab.chat.modelChoice = extra.model; tab.chat.modelLabel = m?.name || extra.model; }
    chatOpened(tab, info, !!extra.chat);
  } catch (err) {
    errorLine(tab.chat, String(err));
  }
  updateChrome();
  return tab;
}

// Keystrokes are fire-and-forget, numbered so Go can put them back in order
// (Wails delivers each call on its own goroutine). Nothing waits on a round trip.
function send(tab, data) {
  predict(tab, data);
  API().Input(tab.id, ++tab.seq, data).catch(() => {});
}

// ---- predictive echo ---------------------------------------------------------
// An agent's TUI takes 50-200ms to echo a keystroke on a small CPU (measured:
// that is the agent redrawing, not AIT). So a typed character is drawn at
// the cursor at once, as mosh does, and dropped as soon as the real screen
// shows it there — or after a moment if it never does.
const PRED_TTL = 600;

function predict(tab, data) {
  const t = tab.term;
  if (!tab.agent || tab.exited) return;
  if (!/^[^\x00-\x1f\x7f]{1,3}$/u.test(data) || data.length !== [...data].length) {
    if (tab.pred.length) { tab.pred = []; drawPred(tab); } // editing keys: let the real echo lead
    return;
  }
  const core = t._core;
  if (core?.coreService?.isCursorHidden) return;
  const buf = t.buffer.active;
  const last = tab.pred[tab.pred.length - 1];
  let x = last ? last.x + 1 : buf.cursorX;
  const y = last ? last.y : buf.baseY + buf.cursorY;
  for (const ch of data) {
    if (x >= t.cols - 1) break;
    tab.pred.push({ ch, x, y, at: performance.now() });
    x++;
  }
  drawPred(tab);
}

// After each write: drop predictions the screen now confirms, or that are stale.
function settlePred(tab) {
  if (!tab.pred.length) return;
  const buf = tab.term.buffer.active;
  const now = performance.now();
  tab.pred = tab.pred.filter((p) => {
    if (now - p.at > PRED_TTL) return false;
    const cell = buf.getLine(p.y)?.getCell(p.x);
    return !(cell && cell.getChars() === p.ch);
  });
  drawPred(tab);
}

function drawPred(tab) {
  let layer = tab.predLayer;
  if (!layer) {
    const screen = tab.term.element?.querySelector(".xterm-screen");
    if (!screen) return;
    layer = tab.predLayer = document.createElement("div");
    layer.className = "pred";
    screen.append(layer);
  }
  if (!tab.pred.length) { layer.textContent = ""; return; }
  const dim = tab.term._core?._renderService?.dimensions?.css?.cell;
  if (!dim) return;
  const top = tab.term.buffer.active.viewportY;
  layer.style.font = `${ui.fontWeight || 500} ${ui.fontSize}px "${ui.fontFamily}", "Cascadia Mono", Consolas, monospace`;
  layer.style.color = theme().term.foreground;
  layer.style.background = "transparent";
  layer.innerHTML = "";
  for (const p of tab.pred) {
    const s = document.createElement("span");
    s.textContent = p.ch;
    s.style.cssText = `left:${p.x * dim.width}px;top:${(p.y - top) * dim.height}px;width:${dim.width}px;height:${dim.height}px;line-height:${dim.height}px;background:${theme().term.background}`;
    layer.append(s);
  }
}

// A veil covers a pane while its agent starts or changes account, and lifts
// on the first output, so a slow start reads as progress, not a frozen tab.
function showVeil(tab, title, sub) {
  let v = tab.pane.querySelector(".veil");
  if (!v) {
    v = document.createElement("div");
    v.className = "veil";
    v.innerHTML = `<div class="card"><span class="icon">${icon(tab.profile)}</span><div><b></b><small></small></div><i class="bar"></i></div>`;
    tab.pane.append(v);
  }
  v.querySelector("b").textContent = title;
  v.querySelector("small").textContent = sub;
  v.classList.remove("out");
  tab.veil = v;
  clearTimeout(tab.veilTimer);
  tab.veilTimer = setTimeout(() => liftVeil(tab), 30000); // never trap the tab
}

// ---- native prompt cards ------------------------------------------------------
// Agents' own set-up prompts are plain text screens. AIT recognises them and
// shows a proper dialog instead, then answers the agent itself.

const SHIELD = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round"><path d="M12 2.8 4.5 5.6v5.6c0 4.6 3.1 8.6 7.5 10 4.4-1.4 7.5-5.4 7.5-10V5.6z"/><path d="m8.8 12 2.3 2.3 4.3-4.6" stroke-linecap="round"/></svg>';

function showTrustCard(tab, folder) {
  liftVeil(tab);
  tab.card?.remove();
  const c = document.createElement("div");
  c.className = "ask";
  const name = profile(tab.profile).name;
  c.innerHTML = `<div class="panel">
    <div class="glyph">${SHIELD}</div>
    <h3>Trust this folder?</h3>
    <div class="path"><svg viewBox="0 0 16 16" width="14" height="14"><path d="M1.5 4.2c0-.8.6-1.4 1.4-1.4h3.3l1.5 1.6h5.4c.8 0 1.4.6 1.4 1.4v6.4c0 .8-.6 1.4-1.4 1.4H2.9c-.8 0-1.4-.6-1.4-1.4z" fill="#e3b341"/></svg><span></span></div>
    <p>${esc(name)} will be able to read, edit and run files here. Only trust folders whose contents you know.</p>
    <div class="actions"><button class="btn quiet" data-a="no">Exit <kbd>Esc</kbd></button><button class="btn go" data-a="yes">Trust folder <kbd>Enter</kbd></button></div>
    <small>AIT remembers this, and won't ask again for this folder.</small>
  </div>`;
  c.querySelector(".path span:last-child").textContent = folder || "this folder";
  c.querySelector('[data-a="yes"]').addEventListener("click", () => answerCard(tab, true));
  c.querySelector('[data-a="no"]').addEventListener("click", () => answerCard(tab, false));
  tab.pane.append(c);
  tab.card = c;
  if (tab.id === active) c.querySelector('[data-a="yes"]').focus();
}

function answerCard(tab, yes) {
  const c = tab.card;
  if (!c) return;
  tab.card = null;
  c.classList.add("out");
  setTimeout(() => c.remove(), 200);
  if (!yes) { closeTab(tab.id); return; }
  API().AnswerTrust(tab.id, true);
  showVeil(tab, "Opening workspace", "");
  tab.veilNotBefore = performance.now() + 400; // the old prompt is still on screen until the agent redraws
  tab.term.focus();
}

// Start-up output begins with invisible set-up sequences; the veil stays
// until something is actually on screen.
function hasText(term) {
  const buf = term.buffer.active;
  for (let y = 0; y < term.rows; y++) {
    if (buf.getLine(buf.viewportY + y)?.translateToString(true).trim()) return true;
  }
  return false;
}

function liftVeil(tab) {
  const v = tab.veil;
  if (!v) return;
  tab.veil = null;
  clearTimeout(tab.veilTimer);
  v.classList.add("out");
  setTimeout(() => v.remove(), 220);
}

function makeTabEl(tab) {
  const el = document.createElement("div");
  el.className = "tab";
  el.innerHTML = `<span class="curve"></span><span class="icon">${icon(tab.profile)}</span><span class="title"></span><button class="x" title="Close tab (Ctrl+Shift+W)"><span class="mdl">&#xE8BB;</span></button>`;
  el.addEventListener("mousedown", (e) => {
    if (e.button === 1) { e.preventDefault(); closeTab(tab.id); }
    else if (e.button === 0 && !e.target.closest(".x")) activate(tab.id);
  });
  el.querySelector(".x").addEventListener("click", () => closeTab(tab.id));
  el.querySelector(".title").textContent = tab.title;
  el.title = tab.title;
  return el;
}

function renderTab(tab) {
  tab.el.querySelector(".title").textContent = tab.title;
  tab.el.title = tab.title;
  if (tab.id === active) document.title = tab.title;
}

// Agents prefix their title with a status glyph, and a bare process name
// ("claude", "codex.exe") says less than the profile's own name.
function cleanTitle(s, prof) {
  const t = (s || "").replace(/^[^\p{L}\p{N}]+/u, "").trim();
  if (!t || /^[\w.-]+(\.exe)?$/i.test(t) && /claude|codex|node|gemini/i.test(t)) return prof.name;
  if (/\\(cmd|powershell|pwsh)\.exe$/i.test(t)) return prof.name;
  return t;
}

function activate(id) {
  const tab = tabs.get(id);
  if (!tab) return;
  active = id;
  for (const t of tabs.values()) {
    t.pane.classList.toggle("active", t.id === id);
    t.el.classList.toggle("active", t.id === id);
  }
  document.title = tab.title;
  tab.el.classList.remove("unread");
  requestAnimationFrame(() => {
    if (tab.native) { chatFocus(tab); return; }
    refit(tab); (tab.card?.querySelector(".go") || tab.term).focus();
  });
  updateChrome();
}

// The last tab never closes silently.
async function closeTab(id, confirmed = false) {
  const tab = tabs.get(id);
  if (!tab) return;
  if (order.length === 1 && !confirmed) {
    const ok = await ask("Close AIT?", tab.agent && !tab.exited
      ? `This is the last tab, and ${profile(tab.profile).name} is still running in it. You can pick the conversation up again from History.`
      : "This is the last tab.");
    if (!ok) {
      if (tab.exited) { await openTab(ui.defaultProfile); closeTab(id, true); }
      return;
    }
    API().Quit();
    return;
  }
  API().Close(id);
  tab.term?.dispose();
  if (tab.chat) { cancelAnimationFrame(tab.chat.anim); clearInterval(tab.chat.busyTimer); }
  tab.pane.remove();
  tab.el.classList.add("leave");
  setTimeout(() => tab.el.remove(), 140);
  tabs.delete(id);
  const i = order.indexOf(id);
  order.splice(i, 1);
  if (active === id && order.length) activate(order[Math.min(i, order.length - 1)]);
}

function cycle(dir) {
  if (order.length < 2) return;
  const i = order.indexOf(active);
  activate(order[(i + dir + order.length) % order.length]);
}

function exited(tab, code) {
  tab.exited = true;
  tab.term.write(`\r\n\x1b[90m[process exited with code ${code}] Press Enter to close this tab.\x1b[0m\r\n`);
}

function refit(tab) {
  if (!tab.fit) return;
  try { tab.fit.fit(); } catch { /* hidden */ }
}

// ---- keyboard ---------------------------------------------------------------------

function keys(e, tab) {
  if (e.type !== "keydown") return true;
  if (tab.card) { // a prompt card is up: it owns the keyboard
    if (e.key === "Enter") answerCard(tab, true);
    else if (e.key === "Escape") answerCard(tab, false);
    e.preventDefault();
    return false;
  }
  const ctrl = e.ctrlKey && !e.altKey && !e.metaKey;
  const k = e.key.toLowerCase();

  if (ctrl && e.shiftKey) {
    if (k === "t") { openTab(ui.defaultProfile); return stop(e); }
    if (k === "w") { closeTab(tab.id); return stop(e); }
    const n = /^Digit([1-9])$/.exec(e.code);
    const usable = ui.profiles.filter((p) => p.installed);
    if (n && usable[n[1] - 1]) { openTab(usable[n[1] - 1].id); return stop(e); }
    if (k === "c" && tab.term) { copy(tab); return stop(e); }
    if (k === "v" && tab.term) return false; // let the browser raise a paste event
    if (k === "h") { toggleHistory(); return stop(e); }
    if (k === "o" && tab.agent) { attach(); return stop(e); }
  }
  if (ctrl && e.key === "Tab") { cycle(e.shiftKey ? -1 : 1); return stop(e); }
  if (ctrl && !e.shiftKey) {
    // Ctrl+1–8 go to that tab, Ctrl+9 to the last one, as in browsers.
    const d = /^Digit([1-9])$/.exec(e.code);
    if (d) { const id = d[1] === "9" ? order[order.length - 1] : order[d[1] - 1]; if (id) activate(id); return stop(e); }
    if (k === "c" && tab.term?.hasSelection()) { copy(tab); return stop(e); }
    if (k === "v") return tab.native ? true : false; // native paste -> pasteEvent / xterm
    if (k === "=" || k === "+") { zoom(1); return stop(e); }
    if (k === "-") { zoom(-1); return stop(e); }
    if (k === "0") { zoom(0); return stop(e); }
    if (k === ",") { openSettings(); return stop(e); }
  }
  return true;
}

function stop(e) { e.preventDefault(); return false; }

// Zoom: Ctrl+wheel, Ctrl+= / Ctrl+- / Ctrl+0, or the − / + in the menu.
// 13px is 100%; the size is remembered.
const ZOOM_BASE = 13, ZOOM_MIN = 9, ZOOM_MAX = 26;

function zoom(d) {
  setFontSize(d === 0 ? ZOOM_BASE : Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, ui.fontSize + d)));
  showZoom();
  clearTimeout(zoom.save);
  zoom.save = setTimeout(() => { if (ui.settings) { ui.settings.fontSize = ui.fontSize; API().SaveSettings(ui.settings); } }, 500);
}

function setFontSize(n) {
  ui.fontSize = n;
  for (const t of tabs.values()) if (t.term) t.term.options.fontSize = ui.fontSize;
  document.documentElement.style.setProperty("--fs", ui.fontSize + "px");
  const tab = tabs.get(active);
  if (tab) refit(tab);
}

const zoomPct = () => Math.round((ui.fontSize / ZOOM_BASE) * 100) + "%";

function showZoom() {
  let z = $("#zoomhud");
  if (!z) { z = document.createElement("div"); z.id = "zoomhud"; document.body.append(z); }
  z.textContent = "Zoom " + zoomPct();
  z.classList.add("on");
  clearTimeout(showZoom.t);
  showZoom.t = setTimeout(() => z.classList.remove("on"), 900);
}

// ---- clipboard, attachments, drag & drop -------------------------------------------

function copy(tab) {
  const s = tab.term.getSelection();
  if (s) RT().ClipboardSetText(s);
  tab.term.clearSelection();
}

// Windows Terminal's right click: copy when something is selected, else paste.
async function rightClick(tab) {
  if (!tab.term) return;
  if (tab.term.hasSelection()) { copy(tab); return; }
  const files = await API().ClipboardFiles();
  if (files.length) { insertPaths(tab, files); return; }
  const text = await RT().ClipboardGetText();
  if (text) tab.term.paste(text);
}

// A paste with files in it — a screenshot, an image copied from a browser,
// files copied in Explorer — becomes file paths in the prompt, which is how
// every agent takes attachments. Plain text is left to xterm.
function pasteEvent(e, tab) {
  const cd = e.clipboardData;
  const files = cd ? [...cd.files] : [];
  if (!files.length) return;
  e.preventDefault();
  e.stopImmediatePropagation();
  (async () => {
    const real = await API().ClipboardFiles();
    if (real.length) { insertPaths(tab, real); return; }
    const paths = [];
    for (const f of files) {
      if (f.size > 64 * 1024 * 1024) { toast(`${f.name || "That file"} is too large to paste — drop it in instead.`); continue; }
      const ext = (f.name.split(".").pop() || f.type.split("/")[1] || "png");
      paths.push(await API().SaveAttachment(await toBase64(f), ext));
    }
    insertPaths(tab, paths);
  })().catch((err) => toast(String(err)));
}

function toBase64(blob) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result).split(",")[1] || "");
    r.onerror = reject;
    r.readAsDataURL(blob);
  });
}

function quote(p) { return /[\s&()'^;,]/.test(p) ? `"${p}"` : p; }

function insertPaths(tab, paths) {
  if (!tab || !paths.length || tab.exited) return;
  if (tab.native) { chatAddFiles(tab, paths); return; }
  tab.term.paste(paths.map(quote).join(" ") + " ");
  tab.term.focus();
  const what = paths.length === 1 ? paths[0].split(/[\\/]/).pop() : `${paths.length} files`;
  if (tab.agent) toast(`Attached ${what}`, 2600);
}

async function attach() {
  const tab = tabs.get(active);
  if (!tab) return;
  const paths = await API().PickFiles();
  insertPaths(tab, paths || []);
}

function setupDrop() {
  const zone = $("#dropzone");
  let depth = 0;
  const hasFiles = (e) => e.dataTransfer && [...e.dataTransfer.types].includes("Files");
  window.addEventListener("dragenter", (e) => {
    if (!hasFiles(e)) return;
    depth++;
    const tab = tabs.get(active);
    $("#dropto").textContent = tab ? `into ${tab.agent ? profile(tab.profile).name + "'s prompt" : tab.title}` : "";
    zone.hidden = false;
  });
  window.addEventListener("dragleave", (e) => { if (hasFiles(e) && --depth <= 0) { depth = 0; zone.hidden = true; } });
  window.addEventListener("drop", () => { depth = 0; zone.hidden = true; });
  // Wails hands over real paths (the web view alone would only see contents).
  RT().OnFileDrop((x, y, paths) => { depth = 0; zone.hidden = true; insertPaths(tabs.get(active), paths || []); }, false);
}

// ---- account pill & menus --------------------------------------------------

async function updateChrome() {
  const tab = tabs.get(active);
  const btn = $("#acct");
  $("#attach").hidden = !tab || !tab.agent || tab.native;
  if (!tab || !tab.agent) { btn.hidden = true; return; }
  const list = await API().Accounts(tab.id);
  const cur = list.find((a) => a.current);
  btn.hidden = !cur;
  if (!cur) return;
  btn.querySelector(".name").textContent = cur.label;
  btn.classList.toggle("limited", cur.status === "limited");
  btn.classList.toggle("out", cur.status === "signed out");
  const free = list.filter((a) => a.status === "ready").length;
  btn.title = `${cur.label} · ${free} of ${list.length} account${list.length === 1 ? "" : "s"} ready — AIT moves to the next one by itself when this one runs out`;
  if (tab.native) {
    const c = tab.chat;
    tab.account = cur.label;
    // The usage line shows the account in use; its last reading until a fresh one arrives.
    if (cur.hasQuota) c.quota = { five: cur.five, week: cur.week, fiveReset: cur.fiveReset, weekReset: cur.weekReset };
    else if (c.quotaAcct !== cur.id) c.quota = null;
    c.quotaAcct = cur.id;
    renderStatus(tab);
  }
}

function showMenu(anchor, items, alignRight, above) {
  const m = $("#menu");
  m.innerHTML = "";
  for (const it of items) {
    if (it === "-") { m.insertAdjacentHTML("beforeend", '<div class="sep"></div>'); continue; }
    if (it.header) { m.insertAdjacentHTML("beforeend", `<div class="mh">${esc(it.header)}</div>`); continue; }
    const b = document.createElement("button");
    b.className = "mi" + (it.cls ? " " + it.cls : "");
    b.innerHTML = it.html;
    if (it.keep) {
      b.querySelectorAll("[data-z]").forEach((z) => z.addEventListener("click", (e) => {
        e.stopPropagation(); zoom(+z.dataset.z); b.querySelector("b").textContent = zoomPct();
      }));
    } else b.addEventListener("click", () => { hideMenu(); it.run?.(); });
    m.append(b);
  }
  m.hidden = false;
  anchor.classList.add("open");
  m.anchor = anchor;
  const r = anchor.getBoundingClientRect();
  const w = m.offsetWidth;
  let x = alignRight ? r.right - w : r.left;
  x = Math.max(8, Math.min(x, innerWidth - w - 8));
  m.style.left = x + "px";
  m.style.top = (above ? r.top - m.offsetHeight - 6 : r.bottom + 4) + "px";
}

function hideMenu() {
  const m = $("#menu");
  if (m.hidden) return;
  m.hidden = true;
  m.anchor?.classList.remove("open");
  if (!overlayOpen()) focusActive();
}

function focusActive() {
  const t = tabs.get(active);
  if (!t) return;
  if (t.native) chatFocus(t); else t.term?.focus();
}

function profileMenu() {
  const items = ui.profiles.filter((p) => p.installed).map((p, i) => ({
    html: `<span class="icon">${icon(p.id)}</span><span class="label">${esc(p.name)}</span><span class="key">Ctrl+Shift+${i + 1}</span>`,
    run: () => openTab(p.id),
  }));
  items.push("-", {
    cls: "zoomrow",
    html: `<span class="icon"><span class="mdl">&#xE71E;</span></span><span class="label">Zoom</span><span class="zr"><button data-z="-1">−</button><b>${zoomPct()}</b><button data-z="1">+</button></span>`,
    keep: true,
  }, "-",
    { html: `<span class="icon">${GLYPH.history}</span><span class="label">History</span><span class="key">Ctrl+Shift+H</span>`, run: toggleHistory },
    { html: `<span class="icon">${GLYPH.rules}</span><span class="label">AI rules</span>`, run: openRules },
    { html: `<span class="icon">${GLYPH.gear}</span><span class="label">Settings</span><span class="key">Ctrl+,</span>`, run: openSettings },
    { html: `<span class="icon"><span class="mdl">&#xE921;</span></span><span class="label">Hide to tray</span>`, run: () => API().HideToTray() });
  showMenu($("#more"), items, false);
}

async function accountMenu() {
  const tab = tabs.get(active);
  if (!tab) return;
  const list = await API().Accounts(tab.id);
  const items = [{ header: tab.native ? "Accounts — when one runs out, AIT moves down this list" : `${profile(tab.profile).name} accounts — the next one takes over automatically` }];
  for (const a of list) {
    const chip = a.status === "ready" ? (a.current ? "in use" : "ready") : a.detail || a.status;
    items.push({
      cls: a.current ? "current" : "",
      html: `<span class="radio"></span><span class="icon">${icon(a.provider)}</span><span class="label">${esc(a.label)}<span class="sub">${esc(a.email || (a.status === "signed out" ? "not signed in" : "signed in"))}${a.hasQuota ? ` · 5h ${Math.round(a.five * 100)}% · week ${Math.round(a.week * 100)}%` : ""}</span>${a.hasQuota ? `<span class="qbar"><i style="width:${Math.min(100, Math.round(a.five * 100))}%"></i></span>` : ""}</span><span class="chip ${a.status === "ready" ? "ready" : a.status === "limited" ? "limited" : ""}">${esc(chip)}</span>`,
      run: async () => {
        if (a.current) return;
        if (a.status === "signed out") { signIn(tab, a, false); return; }
        try { await API().Switch(tab.id, a.id); } catch (err) { toast(String(err)); }
      },
    });
  }
  items.push("-", {
    html: `<span class="icon">${GLYPH.plus}</span><span class="label">Add account</span>`,
    run: () => addAccountMenu(tab),
  }, {
    html: `<span class="icon">${GLYPH.gear}</span><span class="label">Settings</span>`,
    run: openSettings,
  });
  showMenu($("#acct"), items, true);
}

// "Add account": pick the AI first.
function addAccountMenu(tab) {
  const ais = ui.profiles.filter((p) => p.agent);
  const items = [{ header: "Add an account for" }];
  for (const p of ais) {
    items.push({
      cls: p.installed ? "" : "dim",
      html: `<span class="icon">${icon(p.id)}</span><span class="label">${esc(p.name)}<span class="sub">${p.installed ? (p.chat ? "sign in through your browser" : "sign in in a new tab") : "not installed — click for how"}</span></span>`,
      run: async () => {
        if (!p.installed) { toast(`Install ${p.name} first, then restart AIT:  ${INSTALL_HINT[p.id] || ""}`, 7000); return; }
        try { signIn(tab, await API().AddAccount(p.id), true); } catch (err) { toast(String(err)); }
      },
    });
  }
  showMenu($("#acct"), items, true);
}

// ---- signing an account in ------------------------------------------------------
// The AI's own sign-in runs in the background and opens the browser; this
// dialog follows it. A new account the user backs out of is removed again.

let signing = null;

async function signIn(tab, acct, fresh) {
  hideMenu();
  const { id, label, provider } = acct;
  if (!(await API().CanSignIn(provider))) {
    await openTab(provider, { account: id });
    toast(`Sign in to ${label} in this tab. It joins your accounts once you're in.`);
    return;
  }
  signing = { tab, id, label, fresh, done: false };
  signState("wait");
  $("#scrim").hidden = false;
  $("#signin").hidden = false;
  try { await API().SignIn(id); } catch (err) { signState("fail", String(err)); }
}

function signState(state, detail) {
  const s = signing;
  $("#stitle").textContent = state === "ok" ? `${s.label} is signed in` : state === "fail" ? "Sign-in didn't finish" : `Sign in to ${s.label}`;
  $("#sbody").innerHTML = state === "ok"
    ? `${detail ? esc(detail) + ". " : ""}AIT moves to it automatically when the account in use runs out.`
    : state === "fail"
      ? esc(detail || "The sign-in was closed before it finished.")
      : `<span class="spin"></span> Your browser opened the sign-in page. Finish there and AIT picks it up right away.`;
  $("#slink").hidden = state !== "wait" || !s.url;
  $("#sno").textContent = state === "fail" ? "Close" : "Cancel";
  $("#sno").hidden = state === "ok";
  $("#suse").hidden = state !== "ok";
  $("#syes").hidden = state === "wait";
  $("#syes").textContent = state === "fail" ? "Try again" : "Done";
  s.state = state;
}

function closeSignIn() {
  const s = signing;
  if (!s) return;
  signing = null;
  if (s.state === "wait") API().CancelSignIn();
  if (s.fresh && s.state !== "ok") API().ForgetAccount(s.id);
  $("#signin").hidden = true;
  $("#scrim").hidden = $("#historyPanel").hidden && $("#confirm").hidden;
  focusActive();
  updateChrome();
}

function signEvent(e) {
  const s = signing;
  if (!s || e.id !== s.id) return;
  if (e.url) { s.url = e.url; if (s.state === "wait") $("#slink").hidden = false; }
  if (e.done) signState(e.ok ? "ok" : "fail", e.ok ? e.email : e.err);
}

function initSignIn() {
  $("#sno").addEventListener("click", closeSignIn);
  $("#syes").addEventListener("click", () => {
    const s = signing;
    if (s?.state === "fail") { signState("wait"); API().SignIn(s.id).catch((err) => signState("fail", String(err))); }
    else closeSignIn();
  });
  $("#suse").addEventListener("click", async () => {
    const s = signing;
    closeSignIn();
    if (s && tabs.has(s.tab.id)) { try { await API().Switch(s.tab.id, s.id); } catch (err) { toast(String(err)); } }
  });
  $("#slink").addEventListener("click", (e) => { e.preventDefault(); if (signing?.url) RT().BrowserOpenURL(signing.url); });
  RT().EventsOn("signin", signEvent);
}

// ---- updates -------------------------------------------------------------------

let pendingUpdate = null;

function showUpdate(u) {
  pendingUpdate = u;
  const b = $("#update");
  b.querySelector(".upd-t").textContent = u.ready ? `${u.latest} ready` : `Update to ${u.latest}`;
  b.title = u.ready
    ? `AIT ${u.latest} is downloaded and checked. It installs when you close AIT, or click to install now.`
    : `AIT ${u.latest} is available (you have ${u.current}).`;
  b.classList.toggle("ready", !!u.ready);
  b.hidden = false;
}

// After an update: what changed, once.
async function whatsNew() {
  const u = await API().WhatsNew().catch(() => null);
  if (!u || !u.available) return;
  const el = document.createElement("div");
  el.className = "whatsnew";
  el.innerHTML = `<div class="wn-h"><span class="mdl">&#xE930;</span><b>Updated to AIT ${esc(u.latest)}</b><button class="wn-x"><span class="mdl">&#xE8BB;</span></button></div><div class="wn-b md-static"></div>`;
  const body = { el: el.querySelector(".wn-b"), kids: [] };
  renderMarkdown(body, u.notes || "Bug fixes and improvements.", true);
  el.querySelector(".wn-x").addEventListener("click", () => { el.classList.add("out"); setTimeout(() => el.remove(), 250); });
  document.body.append(el);
}

async function installUpdate() {
  const u = pendingUpdate;
  if (!u) return;
  const mb = u.size ? ` (${(u.size / 1048576).toFixed(1)} MB)` : "";
  const ok = await ask(`Install AIT ${u.latest}?`, `AIT will download the update${mb}, check it, install it and restart. Your settings and chats are kept.`);
  if (!ok) return;
  const b = $("#update");
  b.classList.add("busy");
  b.querySelector(".upd-t").textContent = "Downloading…";
  try { await API().InstallUpdate(); }
  catch (err) {
    b.classList.remove("busy");
    b.querySelector(".upd-t").textContent = `Update to ${u.latest}`;
    toast(String(err));
  }
}

// ---- always on top -------------------------------------------------------------

function renderPin() {
  const on = !!ui.settings?.alwaysOnTop;
  $("#pin").classList.toggle("on", on);
  $("#pin").title = on ? "Always on top: on (click to turn off)" : "Keep AIT above other windows";
}

function togglePin() {
  ui.settings.alwaysOnTop = !ui.settings.alwaysOnTop;
  API().SaveSettings(ui.settings);
  renderPin();
  toast(ui.settings.alwaysOnTop ? "AIT stays on top of other windows." : "Always on top is off.", 2200);
}

// ---- theme ----------------------------------------------------------------------

function applyTheme() {
  const root = document.documentElement;
  root.dataset.theme = ui.theme;
  const s = ui.settings || {};
  document.body.classList.toggle("desk", s.style === "desktop");
  const cu = s.custom || {};
  if (ui.theme === "custom") {
    root.style.setProperty("--c-bg", cu.bg || "#14161b");
    root.style.setProperty("--c-fg", cu.fg || "#d8dee9");
    root.style.setProperty("--c-accent", cu.accent || "#88c0d0");
    Object.assign(THEMES.custom.term, { background: cu.bg || "#14161b", foreground: cu.fg || "#d8dee9", cursor: cu.accent || "#88c0d0", cursorAccent: cu.bg || "#14161b", overviewRulerBorder: cu.bg || "#14161b" });
  }
  root.style.setProperty("--fs", ui.fontSize + "px");
  for (const t of tabs.values()) { if (t.term) { t.term.options.theme = { ...theme().term }; drawPred(t); } }
}

function setTheme(id) {
  ui.theme = id;
  applyTheme();
  API().SetTheme(id);
}

// ---- history ----------------------------------------------------------------------

let hItems = [];
let hSel = 0;

const overlayOpen = () => !$("#historyPanel").hidden || !$("#confirm").hidden || !$("#settings").hidden || !$("#rulesEd").hidden || !$("#signin").hidden;

async function toggleHistory() {
  if (!$("#historyPanel").hidden) { closeHistory(); return; }
  hideMenu();
  $("#scrim").hidden = false;
  $("#historyPanel").hidden = false;
  $("#history").classList.add("open");
  $("#hq").value = "";
  $("#hlist").innerHTML = '<div class="hempty">Loading…</div>';
  $("#hq").focus();
  hItems = await API().History();
  renderHistory();
}

function closeHistory() {
  $("#historyPanel").hidden = true;
  $("#scrim").hidden = $("#confirm").hidden;
  $("#history").classList.remove("open");
  focusActive();
}

function renderHistory() {
  const q = $("#hq").value.trim().toLowerCase();
  const list = q ? hItems.filter((c) => (c.title + " " + c.folder + " " + c.providerName).toLowerCase().includes(q)) : hItems;
  const box = $("#hlist");
  box.innerHTML = "";
  if (!list.length) {
    box.innerHTML = `<div class="hempty">${hItems.length ? "No chats match." : "No past chats yet."}</div>`;
    return;
  }
  hSel = Math.min(hSel, list.length - 1);
  let group = "";
  list.forEach((c, i) => {
    const g = dayGroup(c.updated);
    if (g !== group) { group = g; box.insertAdjacentHTML("beforeend", `<div class="hgroup">${g}</div>`); }
    const b = document.createElement("button");
    b.className = "hrow" + (i === hSel ? " sel" : "");
    b.innerHTML = `<span class="icon">${icon(c.provider)}</span><span class="t"><b>${esc(c.title)}</b><span>${esc(c.providerName)} · ${esc(c.folder || "")}</span></span><span class="when">${when(c.updated)}</span>`;
    b.addEventListener("click", () => resumeChat(c));
    b.addEventListener("mousemove", () => { if (hSel !== i) { hSel = i; markSel(); } });
    box.append(b);
  });
  box.list = list;
}

function markSel() {
  [...$("#hlist").querySelectorAll(".hrow")].forEach((r, i) => r.classList.toggle("sel", i === hSel));
  $("#hlist").querySelectorAll(".hrow")[hSel]?.scrollIntoView({ block: "nearest" });
}

async function resumeChat(c) {
  closeHistory();
  await openTab(c.provider, { chat: c.ref });
}

function dayGroup(ts) {
  const d = new Date(ts * 1000), now = new Date();
  const day = (x) => Math.floor((new Date(x.getFullYear(), x.getMonth(), x.getDate())) / 864e5);
  const diff = day(now) - day(d);
  if (diff === 0) return "Today";
  if (diff === 1) return "Yesterday";
  if (diff < 7) return "Earlier this week";
  if (diff < 31) return "Earlier this month";
  return d.toLocaleDateString(undefined, { month: "long", year: "numeric" });
}

function when(ts) {
  const d = new Date(ts * 1000);
  const diff = (Date.now() - d) / 1000;
  if (diff < 60) return "just now";
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400 && d.getDate() === new Date().getDate()) return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

// ---- confirm dialog -------------------------------------------------------------

let answer = null;

function ask(title, body, yes) {
  hideMenu();
  $("#cyes").textContent = yes || (title.startsWith("Close") ? "Close" : "Continue");
  $("#cyes").classList.toggle("primary", title.startsWith("Close"));
  $("#cyes").classList.toggle("go", !title.startsWith("Close"));
  $("#ctitle").textContent = title;
  $("#cbody").textContent = body;
  $("#scrim").hidden = false;
  $("#confirm").hidden = false;
  $("#cyes").focus();
  return new Promise((resolve) => { answer = resolve; });
}

function settle(ok) {
  if (!answer) return;
  $("#confirm").hidden = true;
  $("#scrim").hidden = $("#historyPanel").hidden;
  const r = answer;
  answer = null;
  r(ok);
  if (!ok) focusActive();
}

async function confirmQuit() {
  if (answer) return;
  const running = [...tabs.values()].filter((t) => t.agent && !t.exited).length;
  const n = tabs.size;
  const ok = await ask("Close AIT?", running
    ? `${n} tab${n === 1 ? "" : "s"} open, ${running} with an AI still running. Conversations stay in History.`
    : `${n} tab${n === 1 ? "" : "s"} will close.`);
  if (ok) API().Quit();
}

// ---- tray panel ------------------------------------------------------------------
// Left-clicking the tray icon turns the window into a small status panel.

let miniTimer = 0;
function miniMode(on) {
  document.body.classList.toggle("mini", on);
  $("#mini").hidden = !on;
  clearInterval(miniTimer);
  if (!on) return;
  const row = (a, glyph, label) => `<button class="mi" data-a="${a}"><span class="icon"><span class="mdl">${glyph}</span></span><span class="label">${label}</span></button>`;
  $("#mini").innerHTML = `
    <div class="mi mn-h"><span class="icon mn-icon"></span><span class="label mn-ai"></span><span class="chip mn-acct"></span></div>
    <div class="mn-status"><div class="mn-state"><span class="verb"></span><span class="meta"></span></div><progress class="mn-bar"></progress></div>
    <div class="mi mn-step"><span class="icon"><span class="tool-dot"></span></span><span class="label"><b class="tool-l"></b> <span class="tool-d"></span></span></div>
    <div class="mn-meters"></div>
    <div class="mn-act"><div class="sep"></div>
      ${row("open", "&#xE8A7;", "Open AIT")}${row("hide", "&#xE921;", "Hide to tray")}${row("settings", "&#xE713;", "Settings")}
      <div class="sep"></div>${row("quit", "&#xE7E8;", "Quit AIT")}</div>`;
  $("#mini").querySelectorAll("[data-a]").forEach((b) => b.addEventListener("click", async () => {
    const a = b.dataset.a;
    if (a === "hide") return API().HideToTray();
    await API().ShowApp();
    if (a === "settings") openSettings();
    if (a === "quit") confirmQuit();
  }));
  renderMini();
  miniTimer = setInterval(renderMini, 500);
  fitMini();
}

// Size the panel window to its content.
function fitMini() {
  const m = $("#mini");
  m.style.bottom = "auto";
  const h = Math.ceil(m.getBoundingClientRect().height);
  m.style.bottom = "";
  API().TrayFit?.(h);
}

function renderMini() {
  const m = $("#mini"), tab = tabs.get(active), c = tab?.chat;
  const others = [...tabs.values()].filter((t) => t !== tab && t.chat?.busy).length;
  m.querySelector(".mn-icon").innerHTML = tab ? icon(tab.profile) : "";
  m.querySelector(".mn-ai").textContent = tab ? profile(tab.profile).name : "AIT";
  m.querySelector(".mn-acct").textContent = tab?.account || "";
  const busy = !!(c?.busy || c?.reading);
  const state = !c ? "No AI chat open" : c.reading ? "Reading the handover" : tab.card ? "Waiting for your answer"
    : c.busy ? (c.verb || "Working") + "…" : "Done";
  const st = m.querySelector(".mn-state");
  st.querySelector(".verb").textContent = state;
  st.querySelector(".meta").textContent = (c?.busy ? secsText(performance.now() - c.started) : "") + (others ? ` · ${others} more working` : "");
  const bar = m.querySelector(".mn-bar");
  if (busy) bar.removeAttribute("value"); else bar.value = c ? 1 : 0;
  const tools = c?.thread.querySelectorAll(".tool-h");
  const last = tools?.length ? tools[tools.length - 1] : null;
  const step = m.querySelector(".mn-step");
  step.hidden = !last;
  if (last) {
    step.querySelector(".tool-dot").className = last.querySelector(".tool-dot").className;
    step.querySelector(".tool-dot").textContent = last.querySelector(".tool-dot").textContent;
    step.querySelector(".tool-l").textContent = last.querySelector(".tool-l").textContent;
    step.querySelector(".tool-d").textContent = last.querySelector(".tool-d").textContent;
  }
  const q = c?.quota, rows = [];
  if (q?.five !== undefined) rows.push(["5-hour limit", q.five, q.fiveReset ? "resets in " + untilText(q.fiveReset) : ""]);
  if (q?.week !== undefined) rows.push(["Weekly limit", q.week, q.weekReset ? "resets " + dayText(q.weekReset) : ""]);
  if (c?.ctx && c.window) rows.push(["Context", c.ctx / c.window, fmtNum(c.ctx) + " / " + fmtNum(c.window)]);
  const html = (rows.length ? '<div class="sep"></div>' : "") + rows.map(([l, f, note]) =>
    `<div class="mi mn-m"><span class="label">${l}<span class="sub">${esc(note)}</span></span>${meter(f)}<span class="chip">${pct(f)}</span></div>`).join("");
  const box = m.querySelector(".mn-meters");
  if (box.innerHTML !== html) { box.innerHTML = html; fitMini(); }
}

// ---- misc -------------------------------------------------------------------------

function toast(text, ms = 7000) {
  const t = document.createElement("div");
  t.className = "toast";
  t.textContent = text;
  $("#toasts").append(t);
  setTimeout(() => t.classList.add("gone"), ms);
  setTimeout(() => t.remove(), ms + 400);
}

function esc(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

// Native base64 decoding where the engine has it; the loop is the fallback.
const b64 = Uint8Array.fromBase64 ? (s) => Uint8Array.fromBase64(s) : (s) => {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
};

async function syncMaxIcon() {
  const max = await RT().WindowIsMaximised();
  $("#max .mdl").innerHTML = max ? "&#xE923;" : "&#xE922;";
  $("#max").title = max ? "Restore Down" : "Maximize";
}

// ---- boot ---------------------------------------------------------------------

async function boot() {
  ui = await API().Init();
  APP_VERSION = await API().AppVersion().catch(() => "");
  ui.base = ui.fontSize;
  ui.theme = ui.settings?.theme || ui.theme;
  if (ui.settings?.fontSize) ui.fontSize = ui.settings.fontSize;
  applyTheme();
  // The WebGL renderer bakes glyphs once; the font must be ready first.
  await Promise.all([
    document.fonts.load(`${ui.fontWeight || 500} ${ui.fontSize}px "${ui.fontFamily}"`),
    document.fonts.load(`bold ${ui.fontSize}px "${ui.fontFamily}"`),
  ]).catch(() => {});

  RT().EventsOn("chat:ev", (id, evs) => { const tab = tabs.get(id); if (tab?.native) chatEvents(tab, evs, true); });
  RT().EventsOn("pty:out", (id, data) => {
    const tab = tabs.get(id);
    if (!tab || !tab.term) return;
    tab.term.write(b64(data), () => {
      settlePred(tab);
      if (tab.veil && hasText(tab.term) && !(performance.now() < (tab.veilNotBefore || 0))) liftVeil(tab);
    });
  });
  RT().EventsOn("pty:exit", (id, code) => {
    const tab = tabs.get(id);
    if (!tab || tab.native) return;
    liftVeil(tab);
    if (code === 0) closeTab(id); else exited(tab, code);
  });
  RT().EventsOn("tab:reset", (id) => {
    const tab = tabs.get(id);
    if (!tab) return;
    if (tab.native) { chatDivider(tab, "↻ Passing the conversation on…"); return; }
    tab.term.reset();
    showVeil(tab, "Passing the conversation on", "switching account");
  });
  RT().EventsOn("tab:account", (id, label, model) => {
    const tab = tabs.get(id);
    if (!tab) return;
    const moved = tab.account && tab.account !== label;
    tab.account = label;
    if (tab.native) restoreModel(tab, model || "");
    if (tab.native && moved) chatDivider(tab, `↻ Continued on ${label}`);
    if (tab.veil) tab.veil.querySelector("small").textContent = "continuing on " + label;
    updateChrome();
    if (moved && id === active) { const b = $("#acct"); b.classList.remove("flash"); void b.offsetWidth; b.classList.add("flash"); }
  });
  RT().EventsOn("tab:autotrust", (id) => {
    const tab = tabs.get(id);
    if (!tab) return;
    showVeil(tab, "Opening workspace", "folder already trusted");
    tab.veilNotBefore = performance.now() + 500;
  });
  RT().EventsOn("tab:prompt", (id, kind, folder) => {
    const tab = tabs.get(id);
    if (tab && kind === "trust") showTrustCard(tab, folder);
  });
  RT().EventsOn("tab:provider", (id, pid, name, fromName, reason) => { const t = tabs.get(id); if (t?.native) chatProvider(t, pid, name, fromName, reason); updateChrome(); });
  RT().EventsOn("tab:crossask", (id, to, toName, fromName) => { const t = tabs.get(id); if (t?.native) crossAsk(t, to, toName, fromName); });
  RT().EventsOn("review:start", (id, name) => { const t = tabs.get(id); if (t?.native) reviewState(t, "start", name); });
  RT().EventsOn("review:done", (id, name, feedback) => { const t = tabs.get(id); if (t?.native) reviewState(t, "done", name, feedback); });
  RT().EventsOn("review:error", (id, message) => { const t = tabs.get(id); if (t?.native) reviewState(t, "error", "", message); });
  RT().EventsOn("tab:notice", (id, text) => { toast(text); updateChrome(); });
  RT().EventsOn("app:close-requested", confirmQuit);
  RT().EventsOn("tray:mini", miniMode);
  window.addEventListener("blur", () => { if (document.body.classList.contains("mini")) API().TrayDismiss(); });
  initSignIn();
  setInterval(() => { const t = tabs.get(active); if (t?.native) renderStatus(t); }, 30000); // keeps "resets in" current
  RT().EventsOn("update:available", showUpdate);
  window.addEventListener("focus", () => API().UpdateNudge?.().catch(() => {})); // back at AIT: check if one is due

  $("#new").addEventListener("click", () => openTab(ui.defaultProfile));
  $("#more").addEventListener("click", (e) => { e.stopPropagation(); $("#menu").hidden ? profileMenu() : hideMenu(); });
  $("#acct").addEventListener("click", (e) => { e.stopPropagation(); $("#menu").hidden ? accountMenu() : hideMenu(); });
  $("#history").addEventListener("click", toggleHistory);
  $("#pin").addEventListener("click", togglePin);
  $("#update").addEventListener("click", installUpdate);
  renderPin();
  $("#attach").addEventListener("click", attach);
  $("#min").addEventListener("click", () => RT().WindowMinimise());
  $("#max").addEventListener("click", () => RT().WindowToggleMaximise());
  $("#close").addEventListener("click", confirmQuit);
  $("#drag").addEventListener("dblclick", () => RT().WindowToggleMaximise());
  $("#tabs").addEventListener("dblclick", (e) => { if (e.target.id === "tabs") RT().WindowToggleMaximise(); });
  $("#scrim").addEventListener("mousedown", () => { if (answer) settle(false); else if (rulesOpen()) closeRules(); else if (settingsOpen()) closeSettings(); else closeHistory(); });
  $("#cyes").addEventListener("click", () => settle(true));
  // the confirm button reads "Close" for quitting; other questions relabel it

  $("#cno").addEventListener("click", () => settle(false));

  $("#hq").addEventListener("input", () => { hSel = 0; renderHistory(); });
  $("#hq").addEventListener("keydown", (e) => {
    const list = $("#hlist").list || [];
    if (e.key === "ArrowDown") { hSel = Math.min(hSel + 1, list.length - 1); markSel(); e.preventDefault(); }
    else if (e.key === "ArrowUp") { hSel = Math.max(hSel - 1, 0); markSel(); e.preventDefault(); }
    else if (e.key === "Enter" && list[hSel]) resumeChat(list[hSel]);
  });

  document.addEventListener("mousedown", (e) => {
    if (!e.target.closest("#menu, #more, #acct")) hideMenu();
    if (!e.target.closest("#modelpop, .c-model")) closeModelPop();
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      if (answer) settle(false);
      else if (signing) closeSignIn();
      else if (!$("#modelpop").hidden) closeModelPop();
      else if (rulesOpen()) closeRules();
      else if (settingsOpen()) closeSettings();
      else if (!$("#historyPanel").hidden) closeHistory();
      else hideMenu();
    } else if (e.ctrlKey && e.shiftKey && e.key.toLowerCase() === "h" && overlayOpen()) {
      e.preventDefault(); closeHistory();
    } else if (answer && e.key === "Tab") {
      e.preventDefault(); (document.activeElement === $("#cyes") ? $("#cno") : $("#cyes")).focus();
    }
  });
  document.addEventListener("contextmenu", (e) => e.preventDefault());
  window.addEventListener("wheel", (e) => {
    if (!e.ctrlKey) return;
    e.preventDefault();
    zoom(e.deltaY < 0 ? 1 : -1);
  }, { passive: false, capture: true });
  window.addEventListener("focus", updateChrome);
  setupChatFocus();
  setupDrop();

  new ResizeObserver(() => {
    const tab = tabs.get(active);
    if (tab) refit(tab);
    syncMaxIcon();
  }).observe($("#panes"));

  document.body.classList.add("ready");
  if (!ui.settings.onboarded) await runSetup();
  await openTab(ui.defaultProfile);
  whatsNew();
}

window.addEventListener("DOMContentLoaded", boot);
