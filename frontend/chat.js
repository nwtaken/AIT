"use strict";

// ============================================================================
// Native chat. The agent runs in streaming mode (chat.go) and this file draws
// the conversation: streamed markdown with a typewriter reveal, tool calls,
// permission cards, a live status line. Events arrive already normalised by
// the provider, so nothing here is specific to one AI.
// ============================================================================

const SPIN = ["·", "✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳", "✢"];
const SPIN_TERM = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];
const VERBS = ["Thinking", "Working", "Reasoning", "Crafting", "Pondering", "Composing", "Considering", "Brewing", "Weaving", "Tinkering"];
const LOCAL_COMMANDS = [
  { name: "clear", desc: "Start a new conversation in this tab" },
  { name: "model", desc: "Switch the model" },
  { name: "folder", desc: "Change the working folder" },
  { name: "history", desc: "Open past chats" },
  { name: "export", desc: "Save this conversation as a Markdown file in Downloads" },
  { name: "supereview", desc: "Let the AI ask another AI to review its finished work (on/off) · 2+ connected accounts required" },
];

marked.setOptions({ gfm: true, breaks: false });

// ---- view -------------------------------------------------------------------

function createChat(tab) {
  const name = profile(tab.profile).name;
  const root = document.createElement("div");
  root.className = "chat";
  root.innerHTML = `
    <div class="scroll"><div class="thread"></div></div>
    <button class="jump" hidden><span class="mdl">&#xE74B;</span> Latest</button>
    <div class="dock">
      <div class="trustbar" hidden>
        <span class="tb-icon">${SHIELD}</span>
        <div class="tb-text"><b>Let ${esc(name)} work in <span class="tb-folder"></span>?</b><span>It can read, edit and run files in this folder. AIT remembers your answer.</span></div>
        <button class="btn quiet tb-change">Change folder</button>
        <button class="btn go tb-trust">Trust folder</button>
      </div>
      <div class="busy" hidden><span class="spin">✻</span><span class="verb">Thinking</span><span class="meta"></span><span class="hint">esc to interrupt</span></div>
      <div class="palette" hidden></div>
      <div class="composer">
        <span class="prompt">›</span>
        <div class="field"><div class="chips"></div><textarea rows="1" spellcheck="false" placeholder="Message ${esc(name)}   ·   / for commands"></textarea></div>
        <button class="cbtn c-browser" title="The AI's browser" hidden><span class="mdl">&#xE774;</span></button>
        <button class="cbtn c-mcp" title="MCP servers"><span class="mdl">&#xE71B;</span></button>
        <button class="c-model" title="Switch model"><span class="cm-dot"></span><span class="cm-name">Default</span><span class="mdl small">&#xE70D;</span></button>
        <button class="cbtn c-attach" title="Attach files (Ctrl+Shift+O)"><span class="mdl">&#xE723;</span></button>
        <button class="cbtn c-send" title="Send (Enter)"><span class="mdl">&#xE724;</span></button>
      </div>
      <div class="statusline">
        <span class="sl-seg sl-use"></span><span class="sl-seg sl-week"></span><span class="sl-seg sl-ctx"></span><span class="sl-seg sl-full"></span><span class="sl-seg sl-review"></span><span class="sl-seg sl-turn"></span>
        <button class="sl-folder" title="Change folder"></button>
      </div>
    </div>`;
  tab.pane.append(root);
  tab.pane.classList.add("native");

  const c = {
    root, scroll: root.querySelector(".scroll"), thread: root.querySelector(".thread"),
    ta: root.querySelector("textarea"), chips: root.querySelector(".chips"), palette: root.querySelector(".palette"),
    busyEl: root.querySelector(".busy"), jump: root.querySelector(".jump"), trust: root.querySelector(".trustbar"),
    msg: null, blocks: new Map(), tools: new Map(), asks: new Map(),
    files: [], sent: [], histIdx: -1, stick: true, busy: false, started: 0, outChars: 0,
    model: "", ctx: 0, commands: [], folder: "", pending: new Set(), anim: 0, welcome: null, palSel: 0,
  };
  tab.chat = c;

  c.scroll.addEventListener("scroll", () => {
    const near = c.scroll.scrollHeight - c.scroll.scrollTop - c.scroll.clientHeight < 48;
    c.stick = near;
    if (near) c.jump.hidden = true;
  }, { passive: true });
  c.jump.addEventListener("click", () => { c.stick = true; scrollEnd(c, true); c.jump.hidden = true; });

  c.ta.addEventListener("input", () => { autosize(c.ta); updatePalette(tab); saveDraft(tab); });
  c.ta.addEventListener("keydown", (e) => composerKey(e, tab));
  c.ta.addEventListener("paste", (e) => pasteEvent(e, tab), true);
  root.querySelector(".c-send").addEventListener("click", () => submit(tab));
  root.querySelector(".c-attach").addEventListener("click", attach);
  root.querySelector(".sl-folder").addEventListener("click", () => changeFolder(tab));
  root.querySelector(".c-model").addEventListener("click", (e) => { e.stopPropagation(); $("#menu").hidden ? modelMenu(tab) : hideMenu(); });
  root.querySelector(".c-mcp").addEventListener("click", (e) => { e.stopPropagation(); const pop = $("#modelpop"); pop.hidden || pop.kind !== "mcp" ? mcpPop(tab) : closeModelPop(); });
  root.querySelector(".tb-trust").addEventListener("click", () => trustFolder(tab));
  root.querySelector(".tb-change").addEventListener("click", () => changeFolder(tab));
  root.addEventListener("contextmenu", (e) => e.preventDefault());

  showWelcome(tab);
  return c;
}

function chatOpened(tab, info, resumed, summary) {
  const c = tab.chat;
  API().CanReview(tab.id).then((yes) => { c.reviewAvailable = yes; renderStatus(tab); if (!c.palette.hidden) updatePalette(tab); }).catch(() => {});
  API().ReviewOn?.().then((on) => { reviewOn = on; renderStatus(tab); }).catch(() => {});
  restoreModel(tab, info.model || "");
  c.folder = info.folder || "";
  tab.account = info.account || "";
  c.trust.hidden = true; // agents may work anywhere (Settings → File access)
  c.trust.querySelector(".tb-folder").textContent = c.folder || "this folder";
  if (info.events?.length) chatEvents(tab, info.events, false);
  if (resumed) {
    (summary ? API().ChatSummary(tab.id) : API().ChatHistory(tab.id))
      .then(async (evs) => { if (evs?.length) { hideWelcome(c, true); await replay(tab, evs); } scrollEnd(c, true); })
      .catch((err) => errorLine(c, "Could not load this conversation: " + err))
      .finally(() => chatLoading(tab, false));
  } else chatLoading(tab, false);
  renderStatus(tab);
  if (tab.id === active) c.ta.focus();
}

function fillBoot(tab) {
  const c = tab.chat, w = c.welcome;
  if (!w) return;
  w.querySelector(".wb-acct").textContent = tab.account || "…";
  w.querySelector(".wb-folder").textContent = c.folder || "~";
  const m = prettyModel(c.model) || c.modelLabel;
  if (m) w.querySelector(".wb-model").textContent = m;
}

function chatFocus(tab) {
  if (tab.chat && !overlayOpen()) tab.chat.ta.focus();
}

function setupChatFocus() {
  const controls = "input, textarea, select, button, a, [contenteditable], [tabindex], [role=button]";
  const available = () => {
    const tab = tabs.get(active);
    return tab?.native && tab.chat && !overlayOpen() && $("#menu").hidden && $("#modelpop").hidden ? tab : null;
  };
  document.addEventListener("click", (e) => {
    const tab = available();
    if (!tab || e.target.closest(controls) || !window.getSelection().isCollapsed) return;
    tab.chat.ta.focus({ preventScroll: true });
  });
  // Leave selection available for copying; return to the prompt when typing resumes.
  document.addEventListener("keydown", (e) => {
    const tab = available();
    if (!tab || e.defaultPrevented || e.target.closest(controls)) return;
    const paste = (e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "v";
    const typing = !e.ctrlKey && !e.metaKey && !e.altKey &&
      (e.key.length === 1 || ["Enter", "Backspace", "Delete", "Process"].includes(e.key));
    if (!typing && !paste) return;
    tab.chat.ta.focus({ preventScroll: true });
    composerKey(e, tab);
  }, true);
}

// ---- welcome ------------------------------------------------------------------

function showWelcome(tab) {
  const c = tab.chat;
  const name = profile(tab.profile).name;
  const w = document.createElement("div");
  w.className = "welcome";
  w.innerHTML = `
    <div class="w-term">
      <pre class="w-ascii">${AIT_ASCII}</pre>
      <div class="w-boot">
        <div><span class="wb-k">ait</span><span class="wb-v">${esc(APP_VERSION)} · ${esc(name)}</span></div>
        <div><span class="wb-k">account</span><span class="wb-v wb-acct">…</span></div>
        <div><span class="wb-k">model</span><span class="wb-v wb-model">starting…</span></div>
        <div><span class="wb-k">folder</span><span class="wb-v wb-folder">…</span></div>
        <div><span class="wb-k">ready</span><span class="wb-v wb-ready"><span class="ok">✓</span> switches account automatically when one runs out</span></div>
      </div>
    </div>
    <div class="w-mark">${icon(tab.profile)}</div>
    <div class="w-title"><b>${esc(name)}</b><span class="w-model"></span></div>
    <div class="w-sub">Enter to send · Shift+Enter for a new line · / for commands · drop files to attach</div>
    <div class="w-recent" hidden><div class="w-rh">Pick up where you left off</div></div>`;
  // The most recent conversations, one click to resume.
  API().History().then((list) => {
    const mine = (list || []).filter((x) => x.provider === tab.profile).slice(0, 3);
    if (!mine.length || !c.welcome) return;
    const box = w.querySelector(".w-recent");
    mine.forEach((x, n) => {
      const b = document.createElement("button");
      b.className = "w-tip";
      b.innerHTML = `<span class="w-n">${n + 1}</span><span class="w-rt"></span><span class="w-rw">${x.size ? fmtBytes(x.size) + " · " : ""}${when(x.updated)}</span>`;
      b.querySelector(".w-rt").textContent = x.title;
      b.addEventListener("click", () => resumeChat(x)); // warns first about a very big chat
      box.append(b);
    });
    c.recent = mine;
    box.hidden = false;
  }).catch(() => {});
  c.thread.append(w);
  c.welcome = w;
}

function hideWelcome(c, instant) {
  const w = c.welcome;
  if (!w) return;
  c.welcome = null;
  if (instant) { w.remove(); return; }
  w.classList.add("out");
  setTimeout(() => w.remove(), 260);
}

// ---- events -------------------------------------------------------------------

function chatEvents(tab, evs, live = true) {
  const c = tab.chat;
  if (!c) return;
  for (const e of evs) {
    switch (e.k) {
      case "handover":
        handoverProgress(tab, e.state);
        break;
      case "init":
        c.model = e.model || c.model;
        c.commands = (e.commands || []).filter((n) => !LOCAL_COMMANDS.some((l) => l.name === n) && !(e.tuiOnly || []).includes(n));
        if (c.welcome) c.welcome.querySelector(".w-model").textContent = prettyModel(c.model);
        break;
      case "status":
        if (e.s === "requesting" && !c.busy) setBusy(tab, true);
        break;
      case "efforts":
        effortsBy[tab.profile] = e.models;
        break;
      case "effort":
        c.effort = e.effort;
        renderStatus(tab);
        break;
      case "mcp":
        c.mcp = e.servers;
        if (!$("#modelpop").hidden && $("#modelpop").kind === "mcp" && $("#modelpop").tab === tab) mcpPop(tab, true);
        break;
      case "msg":
        hideWelcome(c, !live);
        newAssistant(c, live);
        if (e.ctx) c.ctx = e.ctx;
        if (e.model && e.model !== "<synthetic>") c.model = e.model;
        break;
      case "start":
        ensureAssistant(c, live);
        if (e.type === "tool") toolRow(tab, e.id, e.name, null, live);
        else startBlock(c, e.i, e.type, live);
        break;
      case "delta": {
        const b = c.blocks.get(e.i);
        if (b) { b.target += e.text; c.outChars += e.text.length; schedule(tab, b, live); }
        break;
      }
      case "stop": {
        const b = c.blocks.get(e.i);
        if (b) { b.done = true; schedule(tab, b, live); }
        break;
      }
      case "tool":
        ensureAssistant(c, live);
        toolRow(tab, e.id, e.name, e.input, live);
        break;
      case "result":
        toolResult(tab, e.id, e.ok, e.text);
        break;
      case "ask":
        askCard(tab, e);
        break;
      case "askclose": // answered somewhere else (the browser pane)
        closeAsk(tab, e);
        break;
      case "done":
        if (e.window) c.window = e.window;
        if (e.ms && live) c.lastMs = e.ms;
        finishTurn(tab, e);
        renderStatus(tab);
        break;
      case "quota":
        c.quota = e;
        renderStatus(tab);
        break;
      case "ctx":
        c.ctx = e.ctx;
        if (e.window) c.window = e.window;
        renderStatus(tab);
        break;
      case "size":
        c.bytes = e.bytes;
        renderStatus(tab);
        break;
      case "user":
        hideWelcome(c, true);
        userTurn(c, e.text, [], false, e.ts);
        c.msg = null;
        break;
      case "text":
      case "thinking": {
        ensureAssistant(c, false);
        const i = "h" + Math.random();
        const b = startBlock(c, i, e.k === "text" ? "text" : "thinking", false);
        b.target = e.text; b.done = true; b.shown = b.target.length;
        renderBlock(b, true);
        break;
      }
      case "error":
        errorLine(c, e.text);
        break;
      case "cmdout":
        cmdOut(c, e.text, live);
        break;
      case "summary":
        summaryCard(c, e.text);
        break;
      case "note":
        sysLine(c, e.text + (e.pre ? ` · ${fmtNum(Math.round(e.pre))} → ${fmtNum(Math.round(e.post))} tokens` : ""), "ok");
        break;
      case "exit":
        setBusy(tab, false);
        if (live) exitCard(tab, e.text);
        break;
    }
  }
  renderStatus(tab);
  if (c.stick) scrollEnd(c);
  else if (live) c.jump.hidden = false;
}

function newAssistant(c, live) {
  const el = document.createElement("div");
  el.className = "turn ai" + (live ? " anim" : "");
  c.thread.append(el);
  c.msg = el;
  c.blocks = new Map();
}

function ensureAssistant(c, live) {
  if (!c.msg) newAssistant(c, live);
}

function startBlock(c, i, type, live) {
  const el = document.createElement("div");
  if (type === "thinking") {
    el.className = "think" + (live ? " live" : "");
    el.innerHTML = `<button class="think-h"><span class="think-g">✻</span><span class="think-l">Thinking</span><span class="mdl chev">&#xE76C;</span></button><div class="think-b"></div>`;
    el.querySelector(".think-h").addEventListener("click", () => el.classList.toggle("open"));
  } else {
    el.className = "md" + (live ? " live" : "");
  }
  c.msg.append(el);
  const b = { el, body: type === "thinking" ? el.querySelector(".think-b") : el, type, target: "", shown: 0, done: false, kids: [] };
  c.blocks.set(i, b);
  return b;
}

// ---- typewriter ----------------------------------------------------------------
// Deltas arrive in bursts. Text is revealed at a pace that eases toward what
// has arrived, so it flows instead of jumping, and never falls far behind.

function schedule(tab, b, live) {
  const c = tab.chat;
  if (!live) { b.shown = b.target.length; renderBlock(b, true); return; }
  c.pending.add(b);
  if (!c.anim) c.anim = requestAnimationFrame(() => frame(tab));
}

function frame(tab) {
  const c = tab.chat;
  c.anim = 0;
  if (!c) return;
  for (const b of c.pending) {
    const left = b.target.length - b.shown;
    if (left > 0) b.shown += Math.max(2, Math.ceil(left / 7));
    if (b.shown > b.target.length) b.shown = b.target.length;
    const finished = b.done && b.shown >= b.target.length;
    renderBlock(b, finished);
    if (finished || (left <= 0 && !b.done)) c.pending.delete(b);
  }
  if (c.stick) scrollEnd(c);
  else c.jump.hidden = false;
  if (c.pending.size) c.anim = requestAnimationFrame(() => frame(tab));
}

function renderBlock(b, finished) {
  const text = b.target.slice(0, b.shown);
  if (b.type === "thinking") {
    b.body.textContent = text;
    if (finished) { b.el.classList.remove("live"); b.el.querySelector(".think-l").textContent = "Thought"; }
    return;
  }
  renderMarkdown(b, text, finished);
  b.el.classList.toggle("live", !finished);
}

// Incremental markdown: top-level blocks that are complete keep their DOM
// (and play their entrance once); only the block still growing re-renders.
function renderMarkdown(b, text, finished) {
  let tokens;
  try { tokens = marked.lexer(text); } catch { return; }
  tokens = tokens.filter((t) => t.type !== "space");
  for (let i = 0; i < tokens.length; i++) {
    const tok = tokens[i];
    const last = i === tokens.length - 1 && !finished;
    const prev = b.kids[i];
    if (prev && prev.raw === tok.raw && !prev.last) continue;
    const el = tokenEl(tok, !last);
    if (prev) prev.el.replaceWith(el);
    else { if (!last || i > 0) el.classList.add("rise"); b.el.append(el); }
    b.kids[i] = { raw: tok.raw, el, last };
  }
  while (b.kids.length > tokens.length) b.kids.pop().el.remove();
}

// mdInto renders markdown into el; its links open in the browser, never in
// AIT's own window.
function mdInto(el, text) {
  el.innerHTML = marked.parse(text || "");
  el.querySelectorAll("a").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); RT().BrowserOpenURL(a.href); }));
}

function tokenEl(tok, complete) {
  if (tok.type === "code") return codeBlock(tok.text, tok.lang || "", complete);
  const wrap = document.createElement("div");
  wrap.className = "mdb";
  wrap.innerHTML = marked.parser([tok]);
  wrap.querySelectorAll("a").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); RT().BrowserOpenURL(a.href); }));
  wrap.querySelectorAll("pre code").forEach((el) => { try { hljs.highlightElement(el); } catch { /* plain */ } });
  return wrap;
}

function codeBlock(code, lang, complete) {
  const el = document.createElement("div");
  el.className = "code";
  const n = code.replace(/\n$/, "").split("\n").length;
  el.innerHTML = `<div class="code-h"><span class="code-l"></span><button class="code-copy"><span class="mdl">&#xE8C8;</span><span>Copy</span></button></div><div class="code-b"><div class="code-gut">${Array.from({ length: n }, (_, i) => i + 1).join("\n")}</div><pre><code></code></pre></div>`;
  el.querySelector(".code-l").textContent = lang || "text";
  const codeEl = el.querySelector("code");
  if (complete) {
    try {
      const res = lang && hljs.getLanguage(lang) ? hljs.highlight(code, { language: lang }) : hljs.highlightAuto(code);
      codeEl.innerHTML = res.value;
      codeEl.classList.add("hljs");
    } catch { codeEl.textContent = code; }
  } else codeEl.textContent = code;
  el.querySelector(".code-copy").addEventListener("click", (e) => {
    RT().ClipboardSetText(code);
    const b = e.currentTarget;
    b.classList.add("done");
    b.lastElementChild.textContent = "Copied";
    setTimeout(() => { b.classList.remove("done"); b.lastElementChild.textContent = "Copy"; }, 1400);
  });
  return el;
}

// ---- user turns -------------------------------------------------------------------

// msgTime is when a message was sent: its saved time when the chat is
// reopened (with the date unless it was today), else now.
function msgTime(ts) {
  const d = ts ? new Date(ts * 1000) : new Date();
  const time = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  return d.toDateString() === new Date().toDateString() ? time : d.toLocaleDateString(undefined, { month: "short", day: "numeric" }) + ", " + time;
}

function userTurn(c, text, files, live, ts) {
  const el = document.createElement("div");
  el.className = "turn user" + (live ? " anim" : "") + (/^\/\S/.test(text) ? " cmd" : "");
  el.innerHTML = `<span class="u-p">›</span><div class="u-body"><div class="u-text"></div></div><span class="u-time">${msgTime(ts)}</span>`;
  el.querySelector(".u-text").textContent = text;
  if (files?.length) {
    const fl = document.createElement("div");
    fl.className = "u-files";
    for (const f of files) {
      const ch = document.createElement("span");
      ch.className = "chip";
      ch.innerHTML = `<span class="mdl">&#xE8A5;</span><span></span>`;
      ch.lastElementChild.textContent = baseName(f.path);
      if (f.thumb) ch.insertAdjacentHTML("afterbegin", `<img src="${f.thumb}">`);
      fl.append(ch);
    }
    el.querySelector(".u-body").append(fl);
  }
  c.thread.append(el);
  return el;
}

// ---- tools ----------------------------------------------------------------------------

const TOOL_ICON = {
  Bash: "&#xE756;", PowerShell: "&#xE756;", Read: "&#xE8A5;", Write: "&#xE70F;", Edit: "&#xE70F;", MultiEdit: "&#xE70F;",
  NotebookEdit: "&#xE70F;", Grep: "&#xE721;", Glob: "&#xE721;", WebFetch: "&#xE774;", WebSearch: "&#xE774;",
  Task: "&#xE8F1;", Agent: "&#xE8F1;", TodoWrite: "&#xE73A;", Skill: "&#xE945;",
};

function toolInfo(name, input, cwdFolder) {
  const i = input || {};
  const rel = (p) => shortPath(p || "", cwdFolder);
  switch (name) {
    case "Bash": case "PowerShell": return { label: name, detail: i.command || "", sub: i.description || "" };
    case "Read": return { label: "Read", detail: rel(i.file_path) };
    case "Write": return { label: "Write", detail: rel(i.file_path), diff: { add: lines(i.content), del: [] } };
    case "Edit": return { label: "Edit", detail: rel(i.file_path), diff: { add: lines(i.new_string), del: lines(i.old_string) } };
    case "MultiEdit": {
      const add = [], del = [];
      for (const e of i.edits || []) { del.push(...lines(e.old_string)); add.push(...lines(e.new_string)); }
      return { label: "Edit", detail: rel(i.file_path), diff: { add, del } };
    }
    case "Patch": {
      const add = [], del = [];
      for (const ch of i.changes || []) {
        if (ch.kind === "add") add.push(...lines(ch.diff));
        else for (const l of lines(ch.diff)) {
          if (l.startsWith("+") && !l.startsWith("+++")) add.push(l.slice(1));
          else if (l.startsWith("-") && !l.startsWith("---")) del.push(l.slice(1));
        }
      }
      const n = (i.changes || []).length;
      return { label: (i.changes || [])[0]?.kind === "add" ? "Create" : "Edit", detail: rel(i.file_path) + (n > 1 ? ` (+${n - 1} more)` : ""), diff: { add, del } };
    }
    case "Grep": return { label: "Search", detail: i.pattern || "", sub: i.path ? rel(i.path) : "" };
    case "Glob": return { label: "Find", detail: i.pattern || "" };
    case "WebFetch": return { label: "Fetch", detail: i.url || "" };
    case "WebSearch": return { label: "Web search", detail: i.query || "" };
    case "Task": case "Agent": return { label: "Agent", detail: i.description || i.subagent_type || "" };
    case "TodoWrite": return { label: "Plan", detail: `${(i.todos || []).length} steps`, todos: i.todos || [] };
    default: {
      const s = Object.values(i).find((v) => typeof v === "string") || "";
      return { label: name.replace(/^mcp__([^_]+)__/, "$1 · "), detail: s };
    }
  }
}

function toolRow(tab, id, name, input, live) {
  const c = tab.chat;
  let t = c.tools.get(id);
  if (!t) {
    const el = document.createElement("div");
    el.className = "trow running" + (live ? " anim" : "");
    el.innerHTML = `<button class="tool-h"><span class="tool-dot"></span><span class="mdl tool-i"></span><span class="tool-l"></span><span class="tool-d"></span><span class="tool-x"></span><span class="tool-t"></span></button><div class="tool-b"></div>`;
    el.querySelector(".tool-h").addEventListener("click", () => { if (el.querySelector(".tool-b").childElementCount) el.classList.toggle("open"); });
    (c.msg || c.thread).append(el);
    t = { el, name, input: null, start: performance.now(), done: false };
    c.tools.set(id, t);
    t.timer = setInterval(() => {
      if (t.done) return clearInterval(t.timer);
      const s = (performance.now() - t.start) / 1000;
      if (s > 1) el.querySelector(".tool-t").textContent = fmtSecs(s);
    }, 250);
  }
  if (input && !t.input) {
    t.input = input;
    const info = toolInfo(name, input, c.folder);
    t.info = info;
    t.el.querySelector(".tool-i").innerHTML = TOOL_ICON[name] || "&#xE90F;";
    t.el.querySelector(".tool-l").textContent = info.label;
    t.el.querySelector(".tool-d").textContent = info.detail;
    t.el.title = info.sub || "";
    const body = t.el.querySelector(".tool-b");
    if (info.diff && (info.diff.add.length || info.diff.del.length)) {
      t.el.querySelector(".tool-x").innerHTML = `<span class="add">+${info.diff.add.length}</span> <span class="del">−${info.diff.del.length}</span>`;
      body.append(diffView(info.diff));
    }
    if (info.todos?.length) { body.append(todoView(info.todos)); t.el.classList.add("open"); }
  } else if (!input) {
    t.el.querySelector(".tool-i").innerHTML = TOOL_ICON[name] || "&#xE90F;";
    t.el.querySelector(".tool-l").textContent = name;
  }
  return t;
}

function toolResult(tab, id, ok, text) {
  const t = tab.chat.tools.get(id);
  if (!t) return;
  t.done = true;
  clearInterval(t.timer);
  t.el.classList.remove("running");
  t.el.classList.add(ok ? "ok" : "fail");
  const secs = (performance.now() - t.start) / 1000;
  if (secs > 0.05 && secs < 3600) t.el.querySelector(".tool-t").textContent = fmtSecs(secs);
  const body = t.el.querySelector(".tool-b");
  const out = (text || "").replace(/\s+$/, "");
  if (out && !t.info?.todos && !(t.info?.diff && ok)) {
    const all = out.split("\n");
    const pre = document.createElement("pre");
    pre.className = "tool-out" + (ok ? "" : " err");
    pre.textContent = all.slice(0, 400).join("\n") + (all.length > 400 ? `\n… ${all.length - 400} more lines` : "");
    body.append(pre);
    const peek = document.createElement("div");
    peek.className = "tool-peek";
    peek.textContent = `└ ${all[0].slice(0, 160)}${all.length > 1 ? `  (+${all.length - 1} lines)` : ""}`;
    t.el.append(peek);
  }
}

function diffView(d) {
  const el = document.createElement("div");
  el.className = "diff";
  const add = (cls, sign, l) => { const r = document.createElement("div"); r.className = cls; r.textContent = `${sign} ${l}`; el.append(r); };
  d.del.slice(0, 200).forEach((l) => add("dl", "−", l));
  d.add.slice(0, 200).forEach((l) => add("al", "+", l));
  return el;
}

function todoView(todos) {
  const el = document.createElement("div");
  el.className = "todos";
  for (const t of todos) {
    const r = document.createElement("div");
    r.className = "todo " + (t.status || "pending");
    r.innerHTML = `<span class="tick"></span><span></span>`;
    r.lastElementChild.textContent = t.content || t.activeForm || "";
    el.append(r);
  }
  return el;
}

// ---- permission cards ----------------------------------------------------------------

function askCard(tab, e) {
  const c = tab.chat;
  ensureAssistant(c, true);
  const info = toolInfo(e.tool, e.input, c.folder);
  const el = document.createElement("div");
  el.className = "askcard anim";
  el.innerHTML = `
    <div class="ak-h"><span class="ak-glyph">${SHIELD}</span><span><b>${esc(profile(tab.profile).name)} ${e.tool === "BrowserHelp" ? "needs your help in the browser" : "wants to " + esc(verbFor(e.tool))}</b><span class="ak-d"></span></span></div>
    <div class="ak-body"></div>
    <div class="ak-act">
      <button class="btn go" data-d="allow">${esc(e.labels?.allow || "Allow")} <kbd>1</kbd></button>
      ${e.always ? '<button class="btn quiet" data-d="always">Always allow <kbd>2</kbd></button>' : ""}
      <button class="btn quiet deny" data-d="deny">${esc(e.labels?.deny || "Deny")} <kbd>${e.always ? 3 : 2}</kbd></button>
    </div>`;
  const isCmd = e.tool === "Bash" || e.tool === "PowerShell";
  el.querySelector(".ak-d").textContent = isCmd ? (info.sub || "") : (info.detail || e.desc || "");
  const body = el.querySelector(".ak-body");
  if (info.diff) body.append(diffView(info.diff));
  else if (e.tool === "Bash" || e.tool === "PowerShell") { const p = document.createElement("pre"); p.className = "ak-cmd"; p.textContent = e.input?.command || ""; body.append(p); }
  else body.remove();
  el.querySelectorAll("[data-d]").forEach((b) => b.addEventListener("click", () => answerAsk(tab, e.req, b.dataset.d)));
  c.msg.append(el);
  c.asks.set(e.req, { el, always: e.always, labels: e.labels });
  browserAsks(tab);
  setBusy(tab, true, "Waiting for you");
  if (c.stick) scrollEnd(c);
}

function closeAsk(tab, e) {
  const c = tab.chat, a = c.asks.get(e.req);
  if (!a) return;
  c.asks.delete(e.req);
  a.el.classList.add("answered", /^✕/.test(e.text) ? "denied" : "allowed");
  a.el.querySelector(".ak-act").innerHTML = `<span class="ak-res">${esc(e.text || "")}</span>`;
  browserAsks(tab);
  setBusy(tab, true);
}

function answerAsk(tab, req, d) {
  const c = tab.chat;
  const a = c.asks.get(req);
  if (!a) return;
  c.asks.delete(req);
  API().ChatAnswer(tab.id, req, d).catch((err) => toast(String(err)));
  a.el.classList.add("answered", d === "deny" ? "denied" : "allowed");
  const word = a.labels ? (d === "deny" ? "✕ " + a.labels.deny : "✓ " + a.labels.allow) : d === "deny" ? "✕ Denied" : d === "always" ? "✓ Always allowed" : "✓ Allowed";
  a.el.querySelector(".ak-act").innerHTML = `<span class="ak-res">${esc(word)}</span>`;
  browserAsks(tab);
  setBusy(tab, true);
  c.ta.focus();
}

function verbFor(tool) {
  return { Bash: "run a command", PowerShell: "run a command", Write: "create a file", Edit: "edit a file", MultiEdit: "edit a file", Patch: "change files",
    WebFetch: "open a web page", WebSearch: "search the web", NotebookEdit: "edit a notebook" }[tool] || `use ${tool}`;
}

// ---- turn state ------------------------------------------------------------------------

function setBusy(tab, on, verb) {
  const c = tab.chat;
  setTimeout(trayPush);
  if (on && !c.busy) { c.started = performance.now(); c.outChars = 0; c.verb = VERBS[Math.floor(Math.random() * VERBS.length)]; }
  c.busy = on;
  browserStatus(tab);
  c.busyEl.hidden = !on || !!c.reading;
  c.busyEl.querySelector(".verb").textContent = verb || c.verb || "Thinking";
  c.busyEl.classList.toggle("waiting", !!verb);
  tab.el?.classList.toggle("working", on);
  clearInterval(c.busyTimer);
  if (!on) return;
  let f = 0;
  c.busyTimer = setInterval(() => {
    const frames = document.body.classList.contains("desk") ? SPIN : SPIN_TERM;
    c.busyEl.querySelector(".spin").textContent = frames[f++ % frames.length];
    const s = (performance.now() - c.started) / 1000;
    const tok = Math.round(c.outChars / 4);
    c.busyEl.querySelector(".meta").textContent = `${fmtSecs(s)}${tok ? ` · ↓ ${fmtNum(tok)} tokens` : ""}`;
  }, 110);
}

// replyCopy puts a Copy action under a finished reply: its answer (the text
// of its last message) as the Markdown the AI wrote.
function replyCopy(c) {
  const text = [...(c.blocks?.values() || [])].filter((b) => b.type === "text").map((b) => b.target).join("\n\n").trim();
  if (!text || !c.msg || c.msg.querySelector(".turn-actions")) return;
  const bar = document.createElement("div");
  bar.className = "turn-actions";
  bar.innerHTML = `<button title="Copy this reply as Markdown"><span class="mdl">&#xE8C8;</span><span>Copy</span></button>`;
  bar.querySelector("button").addEventListener("click", (ev) => {
    RT().ClipboardSetText(text);
    const label = ev.currentTarget.lastElementChild;
    label.textContent = "Copied";
    setTimeout(() => { label.textContent = "Copy"; }, 1500);
  });
  c.msg.append(bar);
}

function finishTurn(tab, e) {
  const c = tab.chat;
  for (const b of c.blocks.values()) { b.done = true; schedule(tab, b, true); }
  setBusy(tab, false);
  replyCopy(c);
  if (e.error) errorLine(c, e.error);
  c.msg = null;
  if (tab.id !== active) tab.el?.classList.add("unread");
}

// A slash command's output (/context, /cost …): a framed panel with the
// command as its title, its markdown rendered.
function cmdOut(c, text, live) {
  const el = document.createElement("div");
  el.className = "cmdout" + (live ? " anim" : "");
  el.innerHTML = `<div class="co-h"><span class="mdl">&#xE756;</span><span class="co-t"></span></div><div class="co-b md-static"></div>`;
  el.querySelector(".co-t").textContent = c.lastCmd || "Output";
  const b = { el: el.querySelector(".co-b"), kids: [] };
  renderMarkdown(b, text, true);
  c.thread.append(el);
  c.lastCmd = "";
  c.msg = null;
}

// A one-line confirmation in the conversation (model switched, compacted …).
// The summary the AI wrote when it compacted the conversation: what it keeps
// of everything before this point.
function summaryCard(c, text) {
  const el = document.createElement("details");
  el.className = "summary-card";
  el.open = true;
  el.innerHTML = `<summary><span class="mdl">&#xE8BC;</span> Summary of the earlier conversation <em>written by the AI when it compacted</em></summary><div class="md"></div>`;
  mdInto(el.querySelector(".md"), text.replace(/^This session is being continued[^\n]*\n+/, ""));
  c.thread.append(el);
}

function sysLine(c, text, kind) {
  const el = document.createElement("div");
  el.className = "sysline anim" + (kind ? " " + kind : "");
  el.innerHTML = `<span class="sl-g">◆</span><span></span>`;
  el.lastElementChild.textContent = text;
  c.thread.append(el);
  if (c.stick) scrollEnd(c, true);
}

function errorLine(c, text) {
  const el = document.createElement("div");
  el.className = "errline anim";
  el.innerHTML = `<span class="mdl">&#xE783;</span><span></span>`;
  el.lastElementChild.textContent = text || "Something went wrong.";
  c.thread.append(el);
}

function exitCard(tab, text) {
  const c = tab.chat;
  const el = document.createElement("div");
  el.className = "errline exit anim";
  el.innerHTML = `<span class="mdl">&#xE783;</span><span><b>${esc(profile(tab.profile).name)} stopped.</b> <span class="ex-t"></span></span><button class="btn quiet">Restart</button>`;
  el.querySelector(".ex-t").textContent = text || "";
  el.querySelector("button").addEventListener("click", async () => {
    el.remove();
    const list = await API().Accounts(tab.id);
    const cur = list.find((a) => a.current) || list[0];
    if (cur) API().Switch(tab.id, cur.id).catch((err) => toast(String(err)));
  });
  c.thread.append(el);
}

// Every account of this AI is out and the user asked to be asked.
function crossAsk(tab, to, toName, fromName) {
  const c = tab.chat;
  const el = document.createElement("div");
  el.className = "askcard cross anim";
  el.innerHTML = `
    <div class="ak-h"><span class="ak-glyph">${icon(to)}</span><span><b>Every ${esc(fromName)} account is at its limit</b><span class="ak-d">${esc(toName)} can carry on with this conversation from here.</span></span></div>
    <div class="ak-act"><button class="btn go">Continue on ${esc(toName)}</button><button class="btn quiet">Wait for ${esc(fromName)}</button></div>`;
  el.querySelector(".go").addEventListener("click", async () => {
    el.querySelector(".ak-act").innerHTML = '<span class="ak-res">Moving the conversation…</span>';
    try { await API().ContinueOn(tab.id, to); } catch (err) { toast(String(err)); }
  });
  el.querySelector(".quiet").addEventListener("click", () => { el.querySelector(".ak-act").innerHTML = `<span class="ak-res">Waiting for ${esc(fromName)} to reset</span>`; });
  c.thread.append(el);
  scrollEnd(c, true);
}

// The tab now runs another AI: same thread, new agent.
function chatProvider(tab, id, name, fromName, reason) {
  const c = tab.chat;
  tab.profile = id;
  tab.el.querySelector(".icon").innerHTML = icon(id);
  c.commands = []; c.effort = "";
  c.ctx = 0; c.window = 0; // the new AI reads the conversation afresh
  c.ta.placeholder = `Message ${name}   ·   / for commands`;
  chatDivider(tab, `↻ Continued on ${name} — ${fromName} ${reason}`);
  setBusy(tab, true);
  renderStatus(tab);
}

// replay draws a conversation in slices, yielding between them, so a long
// one keeps AIT responsive and the loading bar shows how far it is.
async function replay(tab, evs) {
  const c = tab.chat, bar = c.loadingEl?.querySelector("progress");
  c.scroll.style.display = "none"; // laid out once at the end, not after every slice
  try {
    for (let i = 0; i < evs.length; i += 400) {
      chatEvents(tab, evs.slice(i, i + 400), false);
      if (bar) bar.value = Math.min(1, (i + 400) / evs.length);
      if (i + 400 < evs.length) await new Promise((r) => setTimeout(r));
    }
  } finally {
    c.scroll.style.display = "";
  }
}

// chatLoading covers a reopened chat while its conversation loads; nothing
// can be typed or sent until it is drawn.
function chatLoading(tab, on, size) {
  const c = tab.chat;
  c.loading = on;
  c.loadingEl?.remove();
  c.loadingEl = null;
  if (on) {
    c.loadingEl = document.createElement("div");
    c.loadingEl.className = "chat-loading";
    c.loadingEl.innerHTML = `<div class="cl-card" role="status" aria-live="polite"><b>Loading conversation…</b><span></span><progress></progress></div>`;
    c.loadingEl.querySelector("span").textContent = size ? fmtBytes(size) + (size >= 100 * 1024 * 1024 ? " · this one is big, it can take a moment" : "") : "";
    c.root.append(c.loadingEl);
  }
  for (const sel of [".c-send", ".c-model", ".c-mcp", ".c-attach"]) c.root.querySelector(sel).disabled = on || !!c.reading;
  c.ta.disabled = on;
  if (!on && tab.id === active) c.ta.focus();
  renderStatus(tab);
}

function handoverProgress(tab, state) {
  const c = tab.chat;
  c.reading = state === "reading";
  if (!c.handoverEl || c.reading) {
    c.handoverEl = document.createElement("div");
    c.handoverEl.className = "handover-progress";
    c.handoverEl.innerHTML = '<span role="status" aria-live="polite"></span><progress max="1" aria-label="Conversation handover"></progress>';
    c.thread.append(c.handoverEl);
  }
  const bar = c.handoverEl.querySelector("progress");
  if (c.reading) bar.removeAttribute("value");
  else bar.value = state === "complete" ? 1 : 0;
  c.handoverEl.querySelector("span").textContent = c.reading ? "Reading conversation…" :
    state === "complete" ? "Conversation ready" : "Conversation handover failed";
  c.handoverEl.classList.toggle("failed", state === "failed");
  c.root.querySelector(".c-send").disabled = c.reading;
  c.root.querySelector(".c-model").disabled = c.reading;
  setBusy(tab, state !== "failed");
  if (c.stick) scrollEnd(c);
}

function reviewState(tab, state, name, feedback = "") {
  const c = tab.chat;
  // The AI's reply that asked for the review is just the marker: drop it.
  for (const md of c.thread.querySelectorAll(".turn.ai .md")) {
    if (md.textContent.trim() === "[[AIT_SUPEREVIEW]]") md.closest(".turn.ai").remove();
  }
  if (!c.reviewEl || state === "start") {
    c.reviewEl = document.createElement("div");
    c.reviewEl.className = "review-card";
    c.reviewEl.innerHTML = '<b role="status" aria-live="polite"></b><progress></progress><div class="review-step"></div><div class="review-body md"></div>';
    c.thread.append(c.reviewEl);
  }
  const bar = c.reviewEl.querySelector("progress");
  c.reviewEl.classList.toggle("failed", state === "error");
  c.reviewEl.querySelector("b").textContent = state === "start" ? `${name} is reviewing the work…` :
    state === "done" ? `${name} reviewed the work. The AI is acting on it.` : "The review could not run";
  bar.hidden = state === "error";
  if (state === "start") bar.removeAttribute("value"); else bar.value = 1;
  mdInto(c.reviewEl.querySelector(".review-body"), feedback);
  if (state !== "start") c.reviewEl.querySelector(".review-step").textContent = "";
  c.reviewing = state === "start";
  // While the other AI reviews, the busy line says so (with its timer); the
  // feedback then starts the working AI's next turn, which takes it over.
  if (state === "start") setBusy(tab, true, `${name} is reviewing`);
  else if (state === "error") setBusy(tab, false);
  if (c.stick) scrollEnd(c);
}

// reviewStep shows what the reviewing AI is doing right now ("Read app.go").
function reviewStep(tab, step) {
  const el = tab.chat.reviewing && tab.chat.reviewEl?.querySelector(".review-step");
  if (el) el.textContent = step;
}

function restoreModel(tab, model) {
  const c = tab.chat;
  c.model = model;
  c.modelChoice = model;
  c.modelLabel = prettyModel(model) || model || "Default";
  renderStatus(tab);
}

function chatDivider(tab, text) {
  const c = tab.chat;
  if (!c) return;
  let d = c.thread.lastElementChild;
  if (!d || !d.classList.contains("divider")) {
    d = document.createElement("div");
    d.className = "divider anim";
    d.innerHTML = `<span></span>`;
    c.thread.append(d);
  }
  d.firstElementChild.textContent = text;
  setBusy(tab, false);
  c.msg = null;
  if (c.stick) scrollEnd(c);
}

// ---- composer ----------------------------------------------------------------------------

function composerKey(e, tab) {
  const c = tab.chat;
  const pal = !c.palette.hidden;
  if (pal && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
    const n = c.palette.querySelectorAll(".pi").length;
    c.palSel = (c.palSel + (e.key === "ArrowDown" ? 1 : -1) + n) % n;
    markPalette(c); e.preventDefault(); return;
  }
  if (pal && (e.key === "Tab" || (e.key === "Enter" && !e.shiftKey))) { e.preventDefault(); pickPalette(tab); return; }
  if (e.key === "Escape") {
    if (c.reading) { e.preventDefault(); return; }
    if (pal) { c.palette.hidden = true; e.preventDefault(); return; }
    if (c.busy) { API().ChatControl(tab.id, "interrupt"); e.preventDefault(); return; }
  }
  // Permission card shortcuts while the composer is empty.
  if (c.asks.size && !c.ta.value && /^[123]$/.test(e.key)) {
    const [req, a] = [...c.asks.entries()].pop();
    const map = a.always ? { 1: "allow", 2: "always", 3: "deny" } : { 1: "allow", 2: "deny" };
    if (map[e.key]) { answerAsk(tab, req, map[e.key]); e.preventDefault(); return; }
  }
  if (c.welcome && c.recent?.length && !c.ta.value && /^[1-3]$/.test(e.key) && !c.asks.size) {
    const x = c.recent[+e.key - 1];
    if (x) { e.preventDefault(); resumeChat(x); return; }
  }
  if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(tab); return; }
  if (e.key === "ArrowUp" && !c.ta.value && c.sent.length) {
    c.histIdx = c.histIdx < 0 ? c.sent.length - 1 : Math.max(0, c.histIdx - 1);
    c.ta.value = c.sent[c.histIdx]; autosize(c.ta); saveDraft(tab); e.preventDefault(); return;
  }
  if (e.key === "ArrowDown" && c.histIdx >= 0) {
    c.histIdx++;
    c.ta.value = c.histIdx < c.sent.length ? c.sent[c.histIdx] : "";
    if (c.histIdx >= c.sent.length) c.histIdx = -1;
    autosize(c.ta); saveDraft(tab); e.preventDefault(); return;
  }
  // App shortcuts reach the shared handler (tabs, history, zoom…).
  if (e.ctrlKey) { const r = keys(e, tab); if (r === false) return; }
}

function submit(tab) {
  const c = tab.chat;
  if (c.reading || c.loading) return;
  const text = c.ta.value.trim();
  if (!text && !c.files.length) return;
  if (text.startsWith("/") && runLocal(tab, text)) { c.ta.value = ""; autosize(c.ta); saveDraft(tab); return; }
  const files = c.files.slice();
  if (/^\/\S/.test(text)) c.lastCmd = text.split(/\s+/)[0];
  hideWelcome(c);
  userTurn(c, text, files, true);
  c.sent.push(text); c.histIdx = -1;
  c.ta.value = ""; autosize(c.ta); saveDraft(tab);
  c.files = []; renderChips(tab);
  c.palette.hidden = true;
  if (!c.trust.hidden) hideTrust(c);
  c.stick = true; scrollEnd(c, true);
  c.msg = null;
  setBusy(tab, true);
  API().ChatSend(tab.id, text, files.map((f) => f.path)).catch((err) => { setBusy(tab, false); errorLine(c, String(err)); });
}

function runLocal(tab, text) {
  const [cmd] = text.slice(1).split(/\s+/);
  switch (cmd) {
    case "clear":
      API().ChatNew(tab.id).then(() => {
        const c = tab.chat; c.thread.innerHTML = ""; c.tools.clear(); c.asks.clear(); showWelcome(tab);
        sysLine(c, "Started a new conversation", "ok");
      });
      return true;
    case "model": {
      const arg = text.slice(1).split(/\s+/)[1];
      if (arg) { setModel(tab, arg, arg); return true; }
      modelMenu(tab);
      return true;
    }
    case "folder": changeFolder(tab); return true;
    case "history": toggleHistory(); return true;
    case "export":
      API().ExportChat(tab.id).then((p) => sysLine(tab.chat, "Saved to " + p, "ok")).catch((err) => toast(String(err)));
      return true;
    case "supereview":
      // An on/off switch: while on, the AI may ask another AI to review its
      // work once it thinks it is finished. Nothing happens on its own.
      API().ToggleReview(tab.id).then((on) => {
        reviewOn = on;
        renderStatus(tab);
        sysLine(tab.chat, on ? "Supereview on: when the AI thinks it's finished, it can ask another AI what to improve. The chat restarts on the same conversation to apply it."
          : "Supereview off", "ok");
      }).catch((err) => toast(String(err)));
      return true;
  }
  return false;
}

let reviewOn = false; // /supereview, read from AIT when the page starts
window.go?.main?.App?.ReviewOn?.().then((on) => { reviewOn = on; }).catch(() => {});

function updatePalette(tab) {
  const c = tab.chat;
  const v = c.ta.value;
  if (!/^\/\S*$/.test(v)) { c.palette.hidden = true; return; }
  const q = v.slice(1).toLowerCase();
  const items = [...LOCAL_COMMANDS.map((l) => ({ ...l, local: true, disabled: l.name === "supereview" && !c.reviewAvailable,
    desc: l.name === "supereview" ? (reviewOn ? "On · " : "Off · ") + l.desc : l.desc })), ...c.commands.map((n) => ({ name: n, desc: "" }))]
    .filter((x) => x.name.toLowerCase().includes(q)).slice(0, 8);
  if (!items.length) { c.palette.hidden = true; return; }
  c.palette.innerHTML = items.map((x) => `<div class="pi ${x.disabled ? "disabled" : ""}"><b>/${esc(x.name)}</b><span>${esc(x.desc || "")}</span>${x.local ? '<i>AIT</i>' : ""}</div>`).join("");
  c.palette.items = items;
  c.palSel = 0;
  c.palette.querySelectorAll(".pi").forEach((el, i) => el.addEventListener("mousedown", (e) => { e.preventDefault(); c.palSel = i; pickPalette(tab); }));
  markPalette(c);
  c.palette.hidden = false;
}

function markPalette(c) {
  c.palette.querySelectorAll(".pi").forEach((el, i) => el.classList.toggle("sel", i === c.palSel));
}

function pickPalette(tab) {
  const c = tab.chat;
  const it = c.palette.items?.[c.palSel];
  c.palette.hidden = true;
  if (!it || it.disabled) return;
  if (it.local) { c.ta.value = ""; autosize(c.ta); saveDraft(tab); runLocal(tab, "/" + it.name); return; }
  c.ta.value = "/" + it.name + " ";
  autosize(c.ta);
  c.ta.focus();
}

// The model picker: only this chat's AI, its models grouped by family with
// a pill per version. Each chat keeps its own choice.
function modelMenu(tab) {
  const c = tab.chat;
  const pop = $("#modelpop");
  if (!pop.hidden && pop.tab === tab) { closeModelPop(); return; }
  hideMenu();
  const prof = profile(tab.profile);
  const models = prof.models || [];
  const choice = (c.modelChoice ?? "").replace(/\[1m\]$/, "");
  const long = /\[1m\]$/.test(c.modelChoice || "");

  // group by family, keeping the order the AI gives (newest first)
  const fams = [];
  for (const m of models.filter((m) => m.id)) {
    const f = m.family || m.name;
    let g = fams.find((x) => x.name === f);
    if (!g) fams.push(g = { name: f, desc: m.desc, list: [] });
    g.list.push(m);
  }
  const order = ["Fable", "Opus", "Sonnet", "Haiku"];
  fams.sort((a, b) => (order.indexOf(a.name) + 1 || 99) - (order.indexOf(b.name) + 1 || 99));
  const pillLabel = (m, fam) => {
    const rest = m.name.startsWith(fam) ? m.name.slice(fam.length).replace(/^[\s-]+/, "") : m.name;
    return rest.replace(/\s*\(latest\)$/, "") || m.name;
  };
  const hasLong = models.some((m) => m.long);
  const levels = effortLevels(tab, choice);
  const effort = c.effort || (models.find((m) => m.id && m.id === (choice || c.model))?.effort ?? "");

  pop.innerHTML = `
    <div class="mp-h"><span class="mp-i">${icon(prof.id)}</span><b>${esc(prof.name)} models</b><span class="mp-acct">${esc(tab.account || "")}</span></div>
    <div class="mp-list">
      <button class="mp-def ${choice === "" ? "on" : ""}" data-id=""><span class="mp-radio"></span><span><b>Default</b><span>${esc(models[0]?.desc || "Your settings")}</span></span></button>
      ${fams.map((g) => {
        const extra = g.list.length > 4;
        return `<div class="mp-fam ${g.list.some((m) => m.id === choice) ? "has" : ""}">
          <div class="mp-ft"><b>${esc(g.name)}</b><span>${esc(g.desc || "")}</span></div>
          <div class="mp-pills">${g.list.map((m, n) => `<button class="mp-pill ${m.id === choice ? "on" : ""} ${n >= 4 ? "more" : ""}" data-id="${esc(m.id)}" data-name="${esc(m.name)}" data-long="${m.long ? 1 : 0}" title="${esc(m.name)}${m.desc && m.desc !== g.desc ? " — " + esc(m.desc) : ""}">${esc(pillLabel(m, g.name))}</button>`).join("")}
          ${extra ? `<button class="mp-showall">+${g.list.length - 4}</button>` : ""}</div>
        </div>`;
      }).join("")}
    </div>
    ${levels.length ? `<div class="mp-eff"><span class="mp-el" title="How long the model thinks before it answers">Effort</span><div class="mp-levels">${levels.map((l, n) =>
      `<button class="mp-lv ${l === effort ? "on" : ""}" data-e="${esc(l)}" style="--n:${n + 1}" title="${esc(effortLabel(l))}"><i></i>${esc(effortLabel(l))}</button>`).join("")}</div></div>` : ""}
    <div class="mp-f">
      ${hasLong ? `<button class="mp-long ${long ? "on" : ""}" title="Use a 1M-token context window where the model supports it"><span class="switch ${long ? "on" : ""}"><i></i></span>1M context</button>` : ""}
      <span class="mp-custom"><input placeholder="Other model ID" spellcheck="false"><span class="mdl">&#xE751;</span></span>
    </div>`;

  const pick = (id, name, supportsLong) => {
    const useLong = pop.querySelector(".mp-long")?.classList.contains("on") && supportsLong && id;
    const full = useLong ? id + "[1m]" : id;
    setModel(tab, full, useLong ? name + " · 1M" : name);
    closeModelPop();
  };
  pop.querySelector(".mp-def").addEventListener("click", () => pick("", "Default", false));
  pop.querySelectorAll(".mp-lv").forEach((b) => b.addEventListener("click", () => { setEffort(tab, b.dataset.e); closeModelPop(); }));
  pop.querySelectorAll(".mp-pill").forEach((b) => b.addEventListener("click", () => pick(b.dataset.id, b.dataset.name, b.dataset.long === "1")));
  pop.querySelectorAll(".mp-showall").forEach((b) => b.addEventListener("click", () => { b.parentElement.classList.add("all"); b.remove(); }));
  pop.querySelector(".mp-long")?.addEventListener("click", (e) => {
    const b = e.currentTarget;
    b.classList.toggle("on");
    b.querySelector(".switch").classList.toggle("on");
    const cur = models.find((m) => m.id === choice);
    if (cur?.long) pick(cur.id, cur.name, true); // re-apply to the chosen model
  });
  const inp = pop.querySelector(".mp-custom input");
  inp.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && inp.value.trim()) pick(inp.value.trim(), inp.value.trim(), false);
    if (e.key === "Escape") closeModelPop();
  });

  const chip = c.root.querySelector(".c-model");
  chip.classList.add("open");
  pop.tab = tab;
  pop.hidden = false;
  const r = chip.getBoundingClientRect();
  const w = pop.offsetWidth;
  pop.style.left = Math.max(8, Math.min(r.right - w, innerWidth - w - 8)) + "px";
  pop.style.top = Math.max(46, r.top - pop.offsetHeight - 8) + "px";
}

// ---- find in chat (Ctrl+F) ---------------------------------------------------
// Every match is highlighted (CSS Custom Highlight API, so focus stays in the
// box); Enter / Shift+Enter step through them, Esc closes.

function openFind(tab) {
  const c = tab.chat;
  if (!c.find) {
    const bar = document.createElement("div");
    bar.className = "find-bar";
    bar.innerHTML = `<span class="mdl">&#xE721;</span><input spellcheck="false" placeholder="Find in this chat"><span class="fb-n"></span>
      <button class="fb-prev" title="Previous (Shift+Enter or Shift+F3)"><span class="mdl">&#xE70E;</span></button><button class="fb-next" title="Next (Enter or F3)"><span class="mdl">&#xE70D;</span></button><button class="fb-x" title="Close (Esc)"><span class="mdl">&#xE8BB;</span></button>`;
    c.root.append(bar);
    const inp = bar.querySelector("input");
    c.find = { bar, inp, ranges: [], at: -1 };
    inp.addEventListener("input", () => findRun(tab, 0));
    inp.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); findStep(tab, e.shiftKey ? -1 : 1); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closeFind(tab); }
    });
    bar.querySelector(".fb-prev").addEventListener("click", () => findStep(tab, -1));
    bar.querySelector(".fb-next").addEventListener("click", () => findStep(tab, 1));
    bar.querySelector(".fb-x").addEventListener("click", () => closeFind(tab));
  }
  c.find.bar.hidden = false;
  c.find.inp.focus();
  c.find.inp.select();
  findRun(tab, 0);
}

function closeFind(tab) {
  const c = tab.chat;
  if (!c.find) return;
  c.find.bar.hidden = true;
  CSS.highlights?.delete("find"); CSS.highlights?.delete("find-now");
  c.ta.focus();
}

function findRun(tab, dir) {
  const f = tab.chat.find, q = f.inp.value.toLowerCase();
  f.ranges = [];
  if (q) {
    const walk = document.createTreeWalker(tab.chat.thread, NodeFilter.SHOW_TEXT);
    for (let n = walk.nextNode(); n; n = walk.nextNode()) {
      const t = n.data.toLowerCase();
      for (let i = t.indexOf(q); i >= 0; i = t.indexOf(q, i + q.length)) {
        const r = new Range(); r.setStart(n, i); r.setEnd(n, i + q.length); f.ranges.push(r);
      }
    }
  }
  f.at = f.ranges.length ? f.ranges.length - 1 : -1; // start at the latest match
  CSS.highlights?.set("find", new Highlight(...f.ranges));
  findStep(tab, dir);
}

function findStep(tab, dir) {
  const f = tab.chat.find, n = f.ranges.length;
  if (n) f.at = (f.at + dir + n) % n;
  f.bar.querySelector(".fb-n").textContent = f.inp.value ? (n ? `${f.at + 1} of ${n}` : "No matches") : "";
  if (!n) { CSS.highlights?.delete("find-now"); return; }
  const r = f.ranges[f.at];
  CSS.highlights?.set("find-now", new Highlight(r));
  tab.chat.stick = false;
  r.startContainer.parentElement?.closest("details:not([open])")?.setAttribute("open", "");
  r.startContainer.parentElement?.scrollIntoView({ block: "center" });
}

// ---- MCP servers ------------------------------------------------------------------
// The tab's AI's own MCP servers, as the running chat reports them, with a
// switch each. What is switched off applies to every chat of that AI.

const MCP_STATUS = { connected: "Connected", failed: "Failed", "needs-auth": "Needs sign-in", pending: "Connecting…", disabled: "Off", "no tools": "No tools" };

const MCP_GROUPS = ["Roblox", "Minecraft", "Websites", "Other AIs", "Other"];
const MCP_FIXABLE = ["failed", "needs-auth"];

// Fixing a server: an AI works on it out of sight; the list shows only its
// progress, any sign-in page it opened (with the link, should the browser
// not open), and the outcome.
function mcpFixView(f) {
  const title = f.done ? (f.fixed ? `${f.name} is fixed` : `${f.name} is not fixed yet`) : `Fixing ${f.name}`;
  return `<div class="mcp-fix ${f.done ? (f.fixed ? "ok" : "bad") : ""}"><b>${esc(title)}</b>
    ${f.done ? "" : `<progress></progress><div class="mcp-fix-step">${esc(f.step || "")}</div>`}
    ${f.url ? `<div class="mcp-fix-open">Opened the sign-in page in your browser. Didn't open? <a href="${esc(f.url)}">${esc(f.url)}</a></div>` : ""}
    ${f.done ? `<div class="mcp-fix-sum">${esc(f.summary || "")}</div><button class="btn quiet mcp-fix-back">Back to the list</button>` : ""}</div>`;
}

function mcpFixUpdate(tab, name, change) {
  const c = tab.chat;
  if (!c || c.mcpFix?.name !== name) return false;
  Object.assign(c.mcpFix, change);
  const pop = $("#modelpop");
  const shown = !pop.hidden && pop.kind === "mcp" && pop.tab === tab;
  if (shown) mcpPop(tab, true);
  return shown;
}

function mcpFixStep(tab, name, step) { mcpFixUpdate(tab, name, { step }); }

function mcpFixOpen(tab, name, url) {
  RT().BrowserOpenURL(url);
  mcpFixUpdate(tab, name, { url });
}

function mcpFixDone(tab, name, fixed, summary) {
  if (!mcpFixUpdate(tab, name, { done: true, fixed, summary }))
    toast(fixed ? `MCP ${name} is fixed` : `MCP ${name} is not fixed yet: open the MCP list for why`, 6000);
}

async function mcpPop(tab, refresh) {
  const c = tab.chat, pop = $("#modelpop");
  if (!refresh) {
    c.mcp = null;
    API().ChatControl(tab.id, "mcp-status").catch((err) => toast(String(err)));
  }
  const off = new Set(await API().McpOff(tab.profile));
  const restarts = tab.profile !== "claude";
  const row = (m) => {
    const isOff = off.has(m.name) || m.status === "disabled";
    const st = isOff ? "Off" : (MCP_STATUS[m.status] || m.status) + (m.tools && !isOff ? ` · ${m.tools} tool${m.tools === 1 ? "" : "s"}` : "") + (m.error ? ": " + m.error : "");
    const dot = isOff ? "off" : m.status === "connected" ? "ok" : m.status === "failed" ? "bad" : "warn";
    const fix = !isOff && MCP_FIXABLE.includes(m.status) ? `<button class="mp-showall mcp-fixbtn" data-fix="${esc(m.name)}" title="Let the AI find out what is wrong and fix it">Fix</button>` : "";
    return `<div class="mp-def mcp-row"><span class="mcp-dot ${dot}"></span><span class="mcp-t"><b>${esc(m.name)}</b><span>${esc(st)}</span></span>${fix}
      <button class="switch ${isOff ? "" : "on"}" role="switch" aria-checked="${!isOff}" data-name="${esc(m.name)}" title="${isOff ? "Switch on" : "Switch off"}"><i></i></button></div>`;
  };
  // Grouped by what each server is for (sorted by AIT from its name, address and tools).
  const groups = MCP_GROUPS.map((g) => [g, (c.mcp || []).filter((m) => (MCP_GROUPS.includes(m.category) ? m.category : "Other") === g)]).filter(([, list]) => list.length);
  const rows = groups.map(([g, list]) => (groups.length > 1 ? `<div class="mcp-cat">${esc(g)}<span>${list.length}</span></div>` : "") + list.map(row).join("")).join("");
  const scroll = refresh && pop.kind === "mcp" ? pop.querySelector(".mp-list")?.scrollTop || 0 : 0; // a switch re-lists: stay put
  pop.innerHTML = `
    <div class="mp-h"><span class="mp-i">${icon(tab.profile)}</span><b>MCP servers</b><span class="mp-acct">${esc(profile(tab.profile).name)}</span></div>
    <div class="mp-list">${c.mcpFix ? mcpFixView(c.mcpFix) : c.mcp ? rows || '<div class="mcp-empty">No MCP servers set up for this AI.</div>' : '<div class="mcp-empty">Loading…</div>'}</div>
    <div class="mcp-note">${restarts ? "Switching restarts the chat on the same conversation. " : ""}Applies to every ${esc(profile(tab.profile).name)} chat.</div>`;
  pop.querySelector(".mp-list").scrollTop = c.mcpFix ? 0 : scroll;
  pop.querySelectorAll("[data-fix]").forEach((b) => b.addEventListener("click", async (e) => {
    e.stopPropagation();
    const m = c.mcp.find((m) => m.name === b.dataset.fix);
    c.mcpFix = { name: m.name, step: "Looking at what is wrong…" };
    mcpPop(tab, true);
    try { await API().FixMcp(tab.id, m.name, m.status || "", m.error || "", m.where || ""); } catch (err) { mcpFixDone(tab, m.name, false, String(err)); }
  }));
  pop.querySelector(".mcp-fix-back")?.addEventListener("click", (e) => { e.stopPropagation(); c.mcpFix = null; mcpPop(tab); });
  pop.querySelectorAll(".switch[data-name]").forEach((b) => b.addEventListener("click", async () => {
    const on = !b.classList.contains("on");
    b.classList.toggle("on", on);
    try { await API().McpToggle(tab.id, b.dataset.name, on); } catch (err) { toast(String(err)); b.classList.toggle("on", !on); return; }
    if (restarts) closeModelPop();
  }));
  const chip = c.root.querySelector(".c-mcp");
  chip.classList.add("open");
  pop.tab = tab;
  pop.kind = "mcp";
  pop.hidden = false;
  const r = chip.getBoundingClientRect();
  const w = pop.offsetWidth;
  pop.style.left = Math.max(8, Math.min(r.right - w, innerWidth - w - 8)) + "px";
  pop.style.top = Math.max(46, r.top - pop.offsetHeight - 8) + "px";
}

// ---- thinking effort ----------------------------------------------------------------
// Levels come with the model list (ChatGPT) or from the running agent
// ("efforts" event, Claude), keyed by model id, alias or name; "" is the
// default model. A model without levels shows no effort row.

const effortsBy = {};
const EFFORT_NAMES = { none: "None", minimal: "Minimal", low: "Low", medium: "Medium", high: "High", xhigh: "Extra high", max: "Max", ultra: "Ultra" };
const effortLabel = (l) => EFFORT_NAMES[l] || l[0].toUpperCase() + l.slice(1);

function effortLevels(tab, choice) {
  const models = profile(tab.profile).models || [];
  const m = models.find((m) => m.id && m.id === (choice || tab.chat.model));
  if (m?.efforts?.length) return m.efforts;
  const known = effortsBy[tab.profile];
  if (!known) return [];
  return known[choice] || (m && known[m.name]) || [];
}

function setEffort(tab, level) {
  const c = tab.chat;
  c.effort = level;
  API().ChatControl(tab.id, "effort:" + level).catch((err) => toast(String(err)));
  sysLine(c, `Effort set to ${effortLabel(level)}`, "ok");
  renderStatus(tab);
}

function closeModelPop() {
  const pop = $("#modelpop");
  if (pop.hidden) return;
  pop.hidden = true;
  pop.kind = "";
  pop.tab?.chat?.root.querySelector(".c-model")?.classList.remove("open");
  pop.tab?.chat?.root.querySelector(".c-mcp")?.classList.remove("open");
  if (pop.tab) chatFocus(pop.tab);
}

const INSTALL_HINT = {
  claude: "npm install -g @anthropic-ai/claude-code",
  codex: "npm install -g @openai/codex",
  gemini: "npm install -g @google/gemini-cli",
};

async function setModel(tab, id, name) {
  const c = tab.chat;
  try { await API().ChatControl(tab.id, "model:" + id); }
  catch (err) { toast(String(err)); return; }
  c.modelChoice = id;
  c.modelLabel = name;
  c.model = id;
  sysLine(c, id ? `Model switched to ${name}` : "Model set back to the default", "ok");
  renderStatus(tab);
}

// The unsent text in a tab's box is kept with its tab, so it is still there
// when the tab reopens after a restart or an update.
function saveDraft(tab) {
  clearTimeout(tab.chat.draftTimer);
  tab.chat.draftTimer = setTimeout(() => API().SetDraft?.(tab.id, tab.chat.ta.value), 400);
}

function autosize(ta) {
  ta.style.height = "auto";
  ta.style.height = Math.min(ta.scrollHeight, Math.round(innerHeight * 0.4)) + "px";
}

// ---- attachments -----------------------------------------------------------------------

async function chatAddFiles(tab, paths) {
  const c = tab.chat;
  for (const p of paths) {
    if (c.files.some((f) => f.path === p)) continue;
    const f = { path: p, thumb: "" };
    c.files.push(f);
    API().ImageData(p).then((u) => { if (u) { f.thumb = u; renderChips(tab); } });
  }
  renderChips(tab);
  c.ta.focus();
}

function renderChips(tab) {
  const c = tab.chat;
  c.chips.innerHTML = "";
  c.files.forEach((f, i) => {
    const ch = document.createElement("span");
    ch.className = "chip anim";
    ch.innerHTML = `${f.thumb ? `<img src="${f.thumb}">` : '<span class="mdl">&#xE8A5;</span>'}<span class="cn"></span><button class="cx"><span class="mdl">&#xE711;</span></button>`;
    ch.querySelector(".cn").textContent = baseName(f.path);
    ch.title = f.path;
    ch.querySelector(".cx").addEventListener("click", () => { c.files.splice(i, 1); renderChips(tab); });
    c.chips.append(ch);
  });
  c.chips.hidden = !c.files.length;
}

// ---- folder & trust -----------------------------------------------------------------------

function trustFolder(tab) {
  API().TrustFolder(tab.id);
  hideTrust(tab.chat);
  tab.chat.ta.focus();
}

function hideTrust(c) {
  c.trust.classList.add("out");
  setTimeout(() => { c.trust.hidden = true; c.trust.classList.remove("out"); }, 260);
}

async function changeFolder(tab) {
  try {
    const f = await API().ChatFolder(tab.id);
    if (!f) return;
    const c = tab.chat;
    c.folder = f;
    c.thread.innerHTML = ""; c.tools.clear(); c.asks.clear();
    showWelcome(tab);
    const list = await API().Accounts(tab.id);
    c.trust.querySelector(".tb-folder").textContent = f;
    renderStatus(tab);
    toast(`Working in ${f}`, 2200);
    void list;
  } catch (err) { toast(String(err)); }
}

// ---- status line ------------------------------------------------------------------------

function renderStatus(tab) {
  const c = tab.chat;
  if (!c) return;
  fillBoot(tab);
  const r = c.root;
  const known = (profile(tab.profile).models || []).find((m) => m.id && (m.id === c.model || m.id === (c.modelChoice || "").replace(/\[1m\]$/, "")));
  const live = (c.model && (profile(tab.profile).models || []).find((m) => m.id === c.model)?.name) || prettyModel(c.model) || (known && c.modelChoice ? c.modelLabel : "");
  const lv = c.effort && effortLevels(tab, (c.modelChoice || "").replace(/\[1m\]$/, "")).includes(c.effort) ? " · " + effortLabel(c.effort).toLowerCase() : "";
  r.querySelector(".cm-name").textContent = (live || c.modelLabel || "Default") + lv;
  const q = c.quota;
  const seg = (cls, html, title) => {
    const el = r.querySelector(cls);
    if (el.innerHTML !== html) el.innerHTML = html;
    el.title = title || "";
  };
  seg(".sl-use", q?.five !== undefined
    ? `<b>5h</b>${meter(q.five)}<span>${pct(q.five)}</span>${q.fiveReset ? `<em>resets in ${untilText(q.fiveReset)}</em>` : ""}` : "",
    q?.five !== undefined ? `${tab.account}: ${pct(q.five)} of the 5-hour usage window used${q.fiveReset ? ", resets at " + clockText(q.fiveReset) : ""}` : "");
  seg(".sl-week", q?.week !== undefined ? `<b>week</b><span>${pct(q.week)}</span>` : "",
    q?.week !== undefined ? `${pct(q.week)} of the weekly limit used${q.weekReset ? ", resets " + dayText(q.weekReset) : ""}` : "");
  seg(".sl-ctx", c.ctx ? `<b>ctx</b>${c.window ? meter(c.ctx / c.window, 0.8) : ""}<span>${fmtNum(c.ctx)}${c.window ? " / " + fmtNum(c.window) : ""}</span>` : "",
    c.ctx ? `Token context: ${c.ctx.toLocaleString()} tokens${c.window ? ` of ${c.window.toLocaleString()} (${pct(c.ctx / c.window)}); when it fills, the AI compacts the conversation` : ""}` : "");
  seg(".sl-full", c.bytes ? `<b>full</b><span>${fmtBytes(c.bytes)}</span>` : "",
    c.bytes ? "The whole conversation on disk: every message and tool output, earlier AIs included. Only the token context counts toward the limit." : "");
  seg(".sl-review", reviewOn && c.reviewAvailable ? "<b>review</b><span>on</span>" : "",
    reviewOn && c.reviewAvailable ? "Supereview is on: when the AI thinks it's finished, it can ask another AI what to improve (/supereview to switch off)" : "");
  seg(".sl-turn", c.lastMs ? `<b>last</b><span>${secsText(c.lastMs)}</span>` : "", c.lastMs ? "How long the last reply took" : "");
  r.querySelector(".sl-folder").textContent = c.folder || "";
  trayPush();
}

// A small usage bar; turns to the warning colour past `hot`.
function meter(f, hot = 0.8) {
  const w = Math.min(100, Math.max(3, Math.round(f * 100)));
  return `<span class="sl-bar ${f >= hot ? "hot" : ""}"><i style="width:${w}%"></i></span>`;
}
const pct = (f) => Math.round(f * 100) + "%";
const clockText = (s) => new Date(s * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const dayText = (s) => new Date(s * 1000).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" });
const secsText = (ms) => ms < 60000 ? (ms / 1000).toFixed(ms < 10000 ? 1 : 0) + "s" : Math.floor(ms / 60000) + "m " + Math.round(ms % 60000 / 1000) + "s";
function untilText(s) {
  const m = Math.max(0, Math.round(s - Date.now() / 1000) / 60);
  if (m < 60) return Math.ceil(m) + "m";
  if (m < 1440) return Math.floor(m / 60) + "h " + Math.round(m % 60) + "m";
  return Math.round(m / 1440) + "d";
}

// ---- small helpers -------------------------------------------------------------------------

function scrollEnd(c, force) {
  if (force || c.stick) c.scroll.scrollTop = c.scroll.scrollHeight;
}

function prettyModel(m) {
  if (!m) return "";
  const long = /\[1m\]$/.test(m) ? " · 1M" : "";
  m = m.replace(/\[1m\]$/, "");
  const x = /claude-(opus|sonnet|haiku|fable)-(\d+)(?:-(\d+))?/i.exec(m);
  if (x) return `${x[1][0].toUpperCase()}${x[1].slice(1)} ${x[2]}${x[3] && x[3].length < 3 ? "." + x[3] : ""}${long}`;
  return m[0].toUpperCase() + m.slice(1) + long;
}

function shortPath(p, folder) {
  if (!p) return "";
  const parts = p.split(/[\\/]/);
  return parts.length > 3 ? "…\\" + parts.slice(-3).join("\\") : p;
}

function lines(s) { return s ? String(s).split("\n") : []; }
function baseName(p) { return String(p).split(/[\\/]/).pop(); }
function fmtSecs(s) { return s < 60 ? `${s.toFixed(s < 10 ? 1 : 0)}s` : `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`; }
function fmtNum(n) { return n >= 1e6 ? +(n / 1e6).toFixed(2) + "M" : n >= 1000 ? (n / 1000).toFixed(n >= 10000 ? 0 : 1) + "k" : String(n); }
function fmtBytes(n) {
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(n >= 100 ? 0 : 1) : n) + " " + u[i];
}
