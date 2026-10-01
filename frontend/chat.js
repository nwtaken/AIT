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
        <button class="c-model" title="Switch model"><span class="cm-dot"></span><span class="cm-name">Default</span><span class="mdl small">&#xE70D;</span></button>
        <button class="cbtn c-attach" title="Attach files (Ctrl+Shift+O)"><span class="mdl">&#xE723;</span></button>
        <button class="cbtn c-send" title="Send (Enter)"><span class="mdl">&#xE724;</span></button>
      </div>
      <div class="statusline">
        <span class="sl-model"></span><span class="sl-sep"></span>
        <span class="sl-acct"><span class="sl-name"></span><span class="sl-bar"><i></i></span><span class="sl-pct"></span></span><span class="sl-sep"></span>
        <span class="sl-ctx"></span><span class="sl-sep"></span>
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

  c.ta.addEventListener("input", () => { autosize(c.ta); updatePalette(tab); });
  c.ta.addEventListener("keydown", (e) => composerKey(e, tab));
  c.ta.addEventListener("paste", (e) => pasteEvent(e, tab), true);
  root.querySelector(".c-send").addEventListener("click", () => submit(tab));
  root.querySelector(".c-attach").addEventListener("click", attach);
  root.querySelector(".sl-folder").addEventListener("click", () => changeFolder(tab));
  root.querySelector(".c-model").addEventListener("click", (e) => { e.stopPropagation(); $("#menu").hidden ? modelMenu(tab) : hideMenu(); });
  root.querySelector(".sl-model").addEventListener("click", (e) => { e.stopPropagation(); modelMenu(tab); });
  root.querySelector(".tb-trust").addEventListener("click", () => trustFolder(tab));
  root.querySelector(".tb-change").addEventListener("click", () => changeFolder(tab));
  root.addEventListener("contextmenu", (e) => e.preventDefault());

  showWelcome(tab);
  return c;
}

function chatOpened(tab, info, resumed) {
  const c = tab.chat;
  c.folder = info.folder || "";
  tab.account = info.account || "";
  c.trust.hidden = true; // agents may work anywhere (Settings → File access)
  c.trust.querySelector(".tb-folder").textContent = c.folder || "this folder";
  if (info.events?.length) chatEvents(tab, info.events, false);
  if (resumed) {
    API().ChatHistory(tab.id).then((evs) => { if (evs?.length) { hideWelcome(c, true); chatEvents(tab, evs, false); } scrollEnd(c, true); });
  }
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
        <div><span class="wb-k">ait</span><span class="wb-v">1.0 · ${esc(name)}</span></div>
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
      b.innerHTML = `<span class="w-n">${n + 1}</span><span class="w-rt"></span><span class="w-rw">${when(x.updated)}</span>`;
      b.querySelector(".w-rt").textContent = x.title;
      b.addEventListener("click", () => openTab(x.provider, { chat: x.ref }));
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
      case "init":
        c.model = e.model || c.model;
        c.commands = (e.commands || []).filter((n) => !LOCAL_COMMANDS.some((l) => l.name === n) && !(e.tuiOnly || []).includes(n));
        if (c.welcome) c.welcome.querySelector(".w-model").textContent = prettyModel(c.model);
        break;
      case "status":
        if (e.s === "requesting" && !c.busy) setBusy(tab, true);
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
      case "done":
        finishTurn(tab, e);
        break;
      case "quota":
        c.quota = e;
        break;
      case "user":
        hideWelcome(c, true);
        userTurn(c, e.text, [], false);
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

function userTurn(c, text, files, live) {
  const el = document.createElement("div");
  el.className = "turn user" + (live ? " anim" : "") + (/^\/\S/.test(text) ? " cmd" : "");
  el.innerHTML = `<span class="u-p">›</span><div class="u-body"><div class="u-text"></div></div><span class="u-time">${new Date().toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}</span>`;
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
    <div class="ak-h"><span class="ak-glyph">${SHIELD}</span><span><b>${esc(profile(tab.profile).name)} wants to ${esc(verbFor(e.tool))}</b><span class="ak-d"></span></span></div>
    <div class="ak-body"></div>
    <div class="ak-act">
      <button class="btn go" data-d="allow">Allow <kbd>1</kbd></button>
      ${e.always ? '<button class="btn quiet" data-d="always">Always allow <kbd>2</kbd></button>' : ""}
      <button class="btn quiet deny" data-d="deny">Deny <kbd>${e.always ? 3 : 2}</kbd></button>
    </div>`;
  const isCmd = e.tool === "Bash" || e.tool === "PowerShell";
  el.querySelector(".ak-d").textContent = isCmd ? (info.sub || "") : (info.detail || e.desc || "");
  const body = el.querySelector(".ak-body");
  if (info.diff) body.append(diffView(info.diff));
  else if (e.tool === "Bash" || e.tool === "PowerShell") { const p = document.createElement("pre"); p.className = "ak-cmd"; p.textContent = e.input?.command || ""; body.append(p); }
  else body.remove();
  el.querySelectorAll("[data-d]").forEach((b) => b.addEventListener("click", () => answerAsk(tab, e.req, b.dataset.d)));
  c.msg.append(el);
  c.asks.set(e.req, { el, always: e.always });
  setBusy(tab, true, "Waiting for you");
  if (c.stick) scrollEnd(c);
}

function answerAsk(tab, req, d) {
  const c = tab.chat;
  const a = c.asks.get(req);
  if (!a) return;
  c.asks.delete(req);
  API().ChatAnswer(tab.id, req, d).catch((err) => toast(String(err)));
  a.el.classList.add("answered", d === "deny" ? "denied" : "allowed");
  a.el.querySelector(".ak-act").innerHTML = `<span class="ak-res">${d === "deny" ? "✕ Denied" : d === "always" ? "✓ Always allowed" : "✓ Allowed"}</span>`;
  setBusy(tab, true);
  c.ta.focus();
}

function verbFor(tool) {
  return { Bash: "run a command", PowerShell: "run a command", Write: "create a file", Edit: "edit a file", MultiEdit: "edit a file",
    WebFetch: "open a web page", WebSearch: "search the web", NotebookEdit: "edit a notebook" }[tool] || `use ${tool}`;
}

// ---- turn state ------------------------------------------------------------------------

function setBusy(tab, on, verb) {
  const c = tab.chat;
  if (on && !c.busy) { c.started = performance.now(); c.outChars = 0; c.verb = VERBS[Math.floor(Math.random() * VERBS.length)]; }
  c.busy = on;
  c.busyEl.hidden = !on;
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

function finishTurn(tab, e) {
  const c = tab.chat;
  for (const b of c.blocks.values()) { b.done = true; schedule(tab, b, true); }
  setBusy(tab, false);
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
    if (x) { e.preventDefault(); openTab(x.provider, { chat: x.ref }); return; }
  }
  if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); submit(tab); return; }
  if (e.key === "ArrowUp" && !c.ta.value && c.sent.length) {
    c.histIdx = c.histIdx < 0 ? c.sent.length - 1 : Math.max(0, c.histIdx - 1);
    c.ta.value = c.sent[c.histIdx]; autosize(c.ta); e.preventDefault(); return;
  }
  if (e.key === "ArrowDown" && c.histIdx >= 0) {
    c.histIdx++;
    c.ta.value = c.histIdx < c.sent.length ? c.sent[c.histIdx] : "";
    if (c.histIdx >= c.sent.length) c.histIdx = -1;
    autosize(c.ta); e.preventDefault(); return;
  }
  // App shortcuts reach the shared handler (tabs, history, zoom…).
  if (e.ctrlKey) { const r = keys(e, tab); if (r === false) return; }
}

function submit(tab) {
  const c = tab.chat;
  const text = c.ta.value.trim();
  if (!text && !c.files.length) return;
  if (text.startsWith("/") && runLocal(tab, text)) { c.ta.value = ""; autosize(c.ta); return; }
  const files = c.files.slice();
  if (/^\/\S/.test(text)) c.lastCmd = text.split(/\s+/)[0];
  hideWelcome(c);
  userTurn(c, text, files, true);
  c.sent.push(text); c.histIdx = -1;
  c.ta.value = ""; autosize(c.ta);
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
  }
  return false;
}

function updatePalette(tab) {
  const c = tab.chat;
  const v = c.ta.value;
  if (!/^\/\S*$/.test(v)) { c.palette.hidden = true; return; }
  const q = v.slice(1).toLowerCase();
  const items = [...LOCAL_COMMANDS.map((l) => ({ ...l, local: true })), ...c.commands.map((n) => ({ name: n, desc: "" }))]
    .filter((x) => x.name.toLowerCase().includes(q)).slice(0, 8);
  if (!items.length) { c.palette.hidden = true; return; }
  c.palette.innerHTML = items.map((x) => `<div class="pi"><b>/${esc(x.name)}</b><span>${esc(x.desc || "")}</span>${x.local ? '<i>AIT</i>' : ""}</div>`).join("");
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
  if (!it) return;
  if (it.local) { c.ta.value = ""; autosize(c.ta); runLocal(tab, "/" + it.name); return; }
  c.ta.value = "/" + it.name + " ";
  autosize(c.ta);
  c.ta.focus();
}

// The model switcher: every agent's models, grouped. A model of this tab's
// agent switches in place; another agent's opens a new tab on it.
function modelMenu(tab) {
  const c = tab.chat;
  const items = [];
  const agents = ui.profiles.filter((p) => p.agent);
  const mine = agents.find((p) => p.id === tab.profile);
  for (const p of [mine, ...agents.filter((x) => x !== mine)].filter(Boolean)) {
    const here = p.id === tab.profile;
    items.push({ header: here ? `${p.name} · this tab` : p.name });
    if (!p.installed) {
      const hint = INSTALL_HINT[p.id] || "";
      items.push({ cls: "disabled", html: `<span class="icon">${icon(p.id)}</span><span class="label">Not installed<span class="sub">${esc(hint)}</span></span>${hint ? '<span class="key">copy</span>' : ""}`,
        run: () => { if (hint) { RT().ClipboardSetText(hint); toast("Install command copied", 2000); } } });
      continue;
    }
    for (const m of p.models || []) {
      const cur = here && (c.modelChoice ?? "") === m.id;
      items.push({
        cls: cur ? "current" : "",
        html: `<span class="${here ? "radio" : "icon"}">${here ? "" : icon(p.id)}</span><span class="label">${esc(m.name)}<span class="sub">${esc(m.desc || "")}</span></span>${here ? "" : '<span class="key">new tab</span>'}`,
        run: () => here ? setModel(tab, m.id, m.name) : openTab(p.id, { model: m.id }),
      });
    }
  }
  items.push("-", { html: `<span class="icon"><span class="mdl">&#xE70F;</span></span><span class="label">Other model…<span class="sub">Type any model ID</span></span>`,
    run: () => { c.ta.value = "/model "; autosize(c.ta); c.ta.focus(); } });
  showMenu(c.root.querySelector(".c-model"), items, false, true);
}

const INSTALL_HINT = {
  claude: "npm install -g @anthropic-ai/claude-code",
  codex: "npm install -g @openai/codex",
  gemini: "npm install -g @google/gemini-cli",
};

function setModel(tab, id, name) {
  const c = tab.chat;
  c.modelChoice = id;
  c.modelLabel = name;
  API().ChatControl(tab.id, "model:" + id).catch((err) => toast(String(err)));
  sysLine(c, id ? `Model switched to ${name}` : "Model set back to the default", "ok");
  renderStatus(tab);
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
  const live = prettyModel(c.model);
  r.querySelector(".sl-model").textContent = live || profile(tab.profile).name;
  r.querySelector(".cm-name").textContent = live || c.modelLabel || "Default";
  r.querySelector(".sl-name").textContent = tab.account || "";
  const q = c.quota;
  const bar = r.querySelector(".sl-bar");
  if (q && q.five !== undefined) {
    const pct = Math.round(q.five * 100);
    bar.hidden = false;
    bar.firstElementChild.style.width = Math.min(100, pct) + "%";
    bar.classList.toggle("hot", pct >= 80);
    r.querySelector(".sl-pct").textContent = `${pct}% of 5h`;
    r.querySelector(".sl-acct").title = `5-hour window ${pct}% used · weekly ${Math.round((q.week || 0) * 100)}%`;
  } else { bar.hidden = true; r.querySelector(".sl-pct").textContent = ""; }
  r.querySelector(".sl-ctx").textContent = c.ctx ? `${fmtNum(c.ctx)} context` : "";
  r.querySelector(".sl-folder").textContent = c.folder || "";
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
function fmtNum(n) { return n >= 1000 ? (n / 1000).toFixed(n >= 10000 ? 0 : 1) + "k" : String(n); }
