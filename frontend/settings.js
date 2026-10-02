"use strict";

// The settings panel. Every control applies at once and is saved straight
// away; agent options take effect for agents started after the change.

const PRESETS = [
  { name: "Nord", bg: "#2e3440", fg: "#d8dee9", accent: "#88c0d0" },
  { name: "Dracula", bg: "#1e1f29", fg: "#f8f8f2", accent: "#bd93f9" },
  { name: "Rosé", bg: "#191724", fg: "#e0def4", accent: "#ebbcba" },
  { name: "Forest", bg: "#141a15", fg: "#d6e2d4", accent: "#7fc87a" },
  { name: "Paper", bg: "#f6f3ee", fg: "#2a2722", accent: "#c4572f" },
  { name: "Mono", bg: "#000000", fg: "#e6e6e6", accent: "#ffffff" },
];

function seg(name, value, opts) {
  return `<div class="seg" data-k="${name}">${opts.map(([v, label]) => `<button data-v="${v}" class="${v === value ? "on" : ""}">${label}</button>`).join("")}</div>`;
}

function toggleCtl(name, on) {
  return `<button class="switch ${on ? "on" : ""}" data-k="${name}" role="switch" aria-checked="${on}"><i></i></button>`;
}

function openSettings() {
  hideMenu();
  const s = ui.settings;
  const cu = s.custom || {};
  const p = $("#settings");
  p.innerHTML = `
    <div class="st-head"><b>Settings</b><button class="st-x" title="Close (Esc)"><span class="mdl">&#xE8BB;</span></button></div>
    <div class="st-body">
      <section>
        <h4>Appearance</h4>
        <div class="row"><div><b>Interface style</b><span>Terminal keeps everything monospace; Desktop reads like a chat app.</span></div>
          ${seg("style", s.style, [["terminal", "Terminal"], ["desktop", "Desktop"]])}</div>
        <div class="row col"><div><b>Colours</b><span>Two built-in themes, or your own.</span></div>
          <div class="themes">
            ${[["campbell", "Campbell", "#0c0c0c", "#cccccc", "#d97757"], ["powershell", "PowerShell blue", "#012456", "#eeedf0", "#fedba9"], ["custom", "Custom", cu.bg || "#14161b", cu.fg || "#d8dee9", cu.accent || "#88c0d0"]]
              .map(([id, n, bg, fg, ac]) => `<button class="th ${s.theme === id ? "on" : ""}" data-theme="${id}">
                <span class="th-swatch" style="background:${bg}"><i style="background:${fg}"></i><i style="background:${fg};width:40%"></i><em style="background:${ac}"></em></span><span>${n}</span></button>`).join("")}
          </div></div>
        <div class="custom ${s.theme === "custom" ? "" : "dim"}">
          <div class="pickers">
            ${[["bg", "Background", cu.bg || "#14161b"], ["fg", "Text", cu.fg || "#d8dee9"], ["accent", "Accent", cu.accent || "#88c0d0"]]
              .map(([k, n, v]) => `<label class="pick"><input type="color" data-c="${k}" value="${v}"><span>${n}</span><code>${v}</code></label>`).join("")}
          </div>
          <div class="presets">${PRESETS.map((x, i) => `<button class="pre" data-i="${i}" title="${x.name}"><span style="background:${x.bg}"><i style="background:${x.accent}"></i></span>${x.name}</button>`).join("")}</div>
        </div>
        <div class="row"><div><b>Text size</b><span>Also Ctrl+mouse wheel, Ctrl+= and Ctrl+−.</span></div>
          <div class="slider"><input type="range" min="9" max="26" step="1" value="${s.fontSize}" data-k="fontSize"><code>${s.fontSize}px</code></div></div>
      </section>
      <section>
        <h4>Window</h4>
        <div class="row"><div><b>Always on top</b><span>AIT stays above other windows, even when you click elsewhere.</span></div>${toggleCtl("alwaysOnTop", s.alwaysOnTop)}</div>
        <div class="row"><div><b>Notifications</b><span>A Windows notification when the AI finishes or needs you while AIT is in the background.</span></div>${toggleCtl("notify", s.notify)}</div>
        <div class="row"><div><b>Keep an agent ready</b><span>Starts the next agent in the background so new tabs open instantly. Uses one extra process.</span></div>${toggleCtl("prewarm", s.prewarm)}</div>
      </section>
      <section>
        <h4>You</h4>
        <div class="row"><div><b>What your AI calls you</b><span>Leave empty and it won't use a name.</span></div>
          <input class="st-input" data-k="userName" maxlength="40" placeholder="Your name" spellcheck="false" value="${esc(s.userName || "")}"></div>
      </section>
      <section>
        <h4>AI</h4>
        ${ui.profiles.filter((p) => p.agent && p.installed && (p.models || []).length > 1).map((p) => `
        <div class="row"><div><b>Default ${esc(p.name)} model</b><span>New tabs start on this. Switch any time with the model button.</span></div>
          <select class="st-select" data-model="${p.id}">${p.models.map((m) => `<option value="${esc(m.id)}" ${((s.models || {})[p.id] || "") === m.id ? "selected" : ""}>${esc(m.name)}</option>`).join("")}</select></div>`).join("")}
        <div class="row"><div><b>Chat view</b><span>Native draws the conversation in AIT. Terminal shows the agent's own screen.</span></div>
          ${seg("chatView", s.chatView, [["native", "Native"], ["terminal", "Terminal"]])}</div>
        <div class="row"><div><b>File access</b><span>Everywhere lets the AI work in any folder on your drives. Approval prompts still apply.</span></div>
          ${seg("access", s.access || "everywhere", [["everywhere", "Everywhere"], ["folder", "Working folder"]])}</div>
        <div class="row"><div><b>When every account runs out</b><span>Switch AI carries the conversation over to your next AI (Claude → ChatGPT → Gemini).</span></div>
          ${seg("crossAI", s.crossAI || "switch", [["switch", "Switch AI"], ["ask", "Ask me"], ["off", "Wait"]])}</div>
        <div class="row"><div><b>Permissions</b><span>What happens when the AI wants to change files or run commands.</span></div>
          ${seg("permissions", s.permissions, [["ask", "Ask me"], ["edits", "Allow edits"], ["never", "Never ask"]])}</div>
        <div class="row"><div><b>Shared memory</b><span>One memory every AI reads and adds to. <code class="st-mem"></code></span></div>
          <span class="st-btns"><button class="btn quiet st-memopen">Open</button><button class="btn quiet st-memchange">Change</button></span></div>
        <div class="row"><div><b>AI rules</b><span>Instructions every AI follows first. Pick a preset or write your own.</span></div><button class="btn quiet st-rules">Edit rules</button></div>
        <div class="row"><div><b>Accounts &amp; advanced</b><span>Accounts, extra arguments and other options live in the config file.</span></div><button class="btn quiet st-config">Open config</button></div>
      </section>
      <section>
        <h4>Updates</h4>
        <div class="row"><div><b>AIT <span class="st-ver"></span></b><span class="st-upd">Checks GitHub for new versions. Nothing installs without your OK.</span></div>
          <button class="btn quiet st-check">Check now</button></div>
        <div class="row"><div><b>Check automatically</b><span>A button appears in the title bar when an update is ready.</span></div>${toggleCtl("autoUpdate", s.autoUpdate !== false)}</div>
        <div class="row"><div><b>Install updates automatically</b><span>Downloads and checks updates in the background, then installs them when you close AIT.</span></div>${toggleCtl("autoInstall", !!s.autoInstall)}</div>
      </section>
      <p class="st-note">Your name, default models, chat view, file access and permissions apply to tabs opened from now on.</p>
    </div>`;
  p.hidden = false;
  $("#scrim").hidden = false;

  const save = () => { API().SaveSettings(ui.settings); };
  const restyle = () => { ui.theme = ui.settings.theme; applyTheme(); renderPin(); };

  p.querySelector(".st-x").addEventListener("click", closeSettings);
  p.querySelectorAll(".seg").forEach((g) => g.querySelectorAll("button").forEach((b) => b.addEventListener("click", () => {
    g.querySelectorAll("button").forEach((x) => x.classList.toggle("on", x === b));
    ui.settings[g.dataset.k] = b.dataset.v;
    restyle(); save();
  })));
  p.querySelectorAll(".switch").forEach((b) => b.addEventListener("click", () => {
    const on = !b.classList.contains("on");
    b.classList.toggle("on", on);
    ui.settings[b.dataset.k] = on;
    restyle(); save();
  }));
  p.querySelectorAll(".th").forEach((b) => b.addEventListener("click", () => {
    p.querySelectorAll(".th").forEach((x) => x.classList.toggle("on", x === b));
    ui.settings.theme = b.dataset.theme;
    p.querySelector(".custom").classList.toggle("dim", b.dataset.theme !== "custom");
    restyle(); save();
  }));
  const setCustom = (k, v) => {
    ui.settings.custom = { ...(ui.settings.custom || {}), [k]: v };
    ui.settings.theme = "custom";
    p.querySelectorAll(".th").forEach((x) => x.classList.toggle("on", x.dataset.theme === "custom"));
    p.querySelector(".custom").classList.remove("dim");
    const sw = p.querySelector('.th[data-theme="custom"] .th-swatch');
    const c = ui.settings.custom;
    sw.style.background = c.bg || "#14161b";
    sw.querySelectorAll("i").forEach((i) => (i.style.background = c.fg || "#d8dee9"));
    sw.querySelector("em").style.background = c.accent || "#88c0d0";
    restyle();
  };
  p.querySelectorAll("input[type=color]").forEach((inp) => {
    inp.addEventListener("input", () => { setCustom(inp.dataset.c, inp.value); inp.nextElementSibling.nextElementSibling.textContent = inp.value; });
    inp.addEventListener("change", save);
  });
  p.querySelectorAll(".pre").forEach((b) => b.addEventListener("click", () => {
    const x = PRESETS[b.dataset.i];
    for (const k of ["bg", "fg", "accent"]) {
      setCustom(k, x[k]);
      const inp = p.querySelector(`input[data-c="${k}"]`);
      inp.value = x[k];
      inp.nextElementSibling.nextElementSibling.textContent = x[k];
    }
    save();
  }));
  const range = p.querySelector('input[type=range]');
  range.addEventListener("input", () => {
    ui.settings.fontSize = +range.value;
    range.nextElementSibling.textContent = range.value + "px";
    setFontSize(+range.value);
  });
  range.addEventListener("change", () => { ui.base = ui.fontSize; save(); });
  p.querySelector(".st-input").addEventListener("change", (e) => { ui.settings.userName = e.target.value.trim(); save(); });
  p.querySelectorAll(".st-select").forEach((sel) => sel.addEventListener("change", () => {
    ui.settings.models = { ...(ui.settings.models || {}), [sel.dataset.model]: sel.value };
    save();
  }));
  p.querySelector(".st-rules").addEventListener("click", () => { closeSettings(); openRules(); });
  API().MemoryFolder().then((d) => { p.querySelector(".st-mem").textContent = d; });
  p.querySelector(".st-memopen").addEventListener("click", () => API().OpenMemory());
  p.querySelector(".st-memchange").addEventListener("click", async () => {
    try { const d = await API().PickMemoryFolder(); if (d) { p.querySelector(".st-mem").textContent = d; toast("Shared memory: " + d + ". New tabs use it.", 3000); } }
    catch (err) { toast(String(err)); }
  });
  API().AppVersion().then((v) => { p.querySelector(".st-ver").textContent = v; });
  p.querySelector(".st-check").addEventListener("click", async (e) => {
    const b = e.currentTarget, line = p.querySelector(".st-upd");
    b.disabled = true; line.textContent = "Checking…";
    try {
      const u = await API().CheckUpdate();
      if (u.available) { line.textContent = `Version ${u.latest} is available.`; showUpdate(u); }
      else line.textContent = `You're on the latest version${u.latest ? ` (${u.latest})` : ""}.`;
    } catch (err) { line.textContent = "Couldn't reach GitHub: " + err; }
    b.disabled = false;
  });
  p.querySelector(".st-config").addEventListener("click", () => API().OpenSettings());
}

function closeSettings() {
  $("#settings").hidden = true;
  $("#scrim").hidden = !$("#historyPanel").hidden ? false : $("#confirm").hidden;
  focusActive();
}

const settingsOpen = () => !$("#settings").hidden;

// The AI rules editor: rules.md, edited in the app, with presets to start from.
async function openRules() {
  hideMenu();
  const r = await API().GetRules();
  const p = $("#rulesEd");
  p.innerHTML = `
    <div class="st-head"><b>AI rules</b><button class="st-x" title="Close (Esc)"><span class="mdl">&#xE8BB;</span></button></div>
    <div class="st-body">
      <p class="rl-note">Every AI follows these rules first, above its own instructions. New chats get them as soon as you save; a running chat gets them at its next restart or account switch.</p>
      <h4>Presets</h4>
      <div class="rl-presets">${r.presets.map((x, i) => `<button class="rl-pre" data-i="${i}"><b>${esc(x.name)}</b><span>${esc(x.desc)}</span></button>`).join("")}</div>
      <h4>Rules</h4>
      <textarea class="rl-text" spellcheck="false" placeholder="No rules: each AI uses its own behaviour."></textarea>
    </div>
    <div class="rl-foot"><span class="rl-state"></span><button class="btn quiet rl-revert">Revert</button><button class="btn go rl-save">Save</button></div>`;
  const ta = p.querySelector(".rl-text"), state = p.querySelector(".rl-state");
  let saved = r.text;
  ta.value = saved;
  const mark = () => {
    const dirty = ta.value.trim() !== saved.trim();
    state.textContent = dirty ? "Unsaved changes" : "Saved";
    p.querySelector(".rl-save").disabled = !dirty;
    p.querySelector(".rl-revert").disabled = !dirty;
    p.querySelectorAll(".rl-pre").forEach((b) => b.classList.toggle("on", r.presets[b.dataset.i].text.trim() === ta.value.trim()));
  };
  ta.addEventListener("input", mark);
  p.querySelectorAll(".rl-pre").forEach((b) => b.addEventListener("click", () => { ta.value = r.presets[b.dataset.i].text; mark(); ta.focus({ preventScroll: true }); }));
  p.querySelector(".rl-revert").addEventListener("click", () => { ta.value = saved; mark(); });
  p.querySelector(".rl-save").addEventListener("click", async () => {
    try { await API().SaveRules(ta.value); saved = ta.value; mark(); toast("AI rules saved.", 3000); }
    catch (err) { toast(String(err)); }
  });
  p.querySelector(".st-x").addEventListener("click", closeRules);
  mark();
  p.hidden = false;
  $("#scrim").hidden = false;
  ta.focus({ preventScroll: true });
}

function closeRules() {
  $("#rulesEd").hidden = true;
  $("#scrim").hidden = !$("#historyPanel").hidden ? false : $("#confirm").hidden;
  focusActive();
}

const rulesOpen = () => !$("#rulesEd").hidden;
