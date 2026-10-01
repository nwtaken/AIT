"use strict";

// First-run setup. Shown once, before the first tab, to a new user: nothing
// about anyone else is in AIT; everything here is the user's own answer.

const AIT_ASCII = [
  " █████╗ ██╗████████╗",
  "██╔══██╗██║╚══██╔══╝",
  "███████║██║   ██║   ",
  "██╔══██║██║   ██║   ",
  "██║  ██║██║   ██║   ",
  "╚═╝  ╚═╝╚═╝   ╚═╝   ",
].join("\n");

function runSetup() {
  return new Promise((done) => {
    const s = ui.settings;
    const el = $("#setup");
    el.hidden = false;
    let step = 0;
    const steps = [intro, name, look, perms, agents];

    function show() {
      el.innerHTML = `<div class="su-card"><div class="su-dots">${steps.map((_, i) => `<i class="${i === step ? "on" : i < step ? "done" : ""}"></i>`).join("")}</div><div class="su-step"></div></div>`;
      steps[step](el.querySelector(".su-step"));
      el.querySelector(".su-step").classList.add("enter");
      el.querySelector("input, .su-next")?.focus();
    }
    function next() { if (step < steps.length - 1) { step++; show(); } else finish(); }
    function back() { if (step > 0) { step--; show(); } }
    const nav = (label = "Continue", first) => `<div class="su-nav">${first ? "" : '<button class="btn quiet su-back">Back</button>'}<button class="btn go su-next">${label} <kbd>Enter</kbd></button></div>`;
    const wire = (box) => {
      box.querySelector(".su-next")?.addEventListener("click", next);
      box.querySelector(".su-back")?.addEventListener("click", back);
    };

    function intro(box) {
      box.innerHTML = `<pre class="su-ascii">${AIT_ASCII}</pre><h1>Welcome to AIT</h1><p>A few questions to set things up. You can change all of it later in Settings.</p>${nav("Get started", true)}`;
      wire(box);
    }
    function name(box) {
      box.innerHTML = `<h2>What should your AI call you?</h2><p>Your AI will use this name when it talks to you. Leave it empty to skip.</p>
        <input class="su-input" maxlength="40" placeholder="Your name" spellcheck="false" value="${esc(s.userName || "")}">${nav()}`;
      const inp = box.querySelector("input");
      inp.addEventListener("input", () => { s.userName = inp.value.trim(); });
      wire(box);
    }
    function look(box) {
      box.innerHTML = `<h2>How should it look?</h2>
        <div class="su-styles">
          <button class="su-opt" data-style="terminal"><span class="su-prev term"><i>› fix the bug</i><b>⏺ Found it.</b><em>├─ Edit auth.ts</em></span><b>Terminal</b><span>Monospace, like a shell</span></button>
          <button class="su-opt" data-style="desktop"><span class="su-prev desk"><i>fix the bug</i><b>Found it — fixed in auth.ts.</b></span><b>Desktop</b><span>Like a chat app</span></button>
        </div>
        <div class="su-themes">${[["campbell", "Campbell", "#0c0c0c", "#d97757"], ["powershell", "PowerShell blue", "#012456", "#fedba9"]]
          .map(([id, n, bg, ac]) => `<button class="su-th" data-theme="${id}"><span style="background:${bg}"><i style="background:${ac}"></i></span>${n}</button>`).join("")}
          <span class="su-more">More colours in Settings</span></div>${nav()}`;
      const sync = () => {
        box.querySelectorAll("[data-style]").forEach((b) => b.classList.toggle("on", b.dataset.style === s.style));
        box.querySelectorAll("[data-theme]").forEach((b) => b.classList.toggle("on", b.dataset.theme === s.theme));
      };
      box.querySelectorAll("[data-style]").forEach((b) => b.addEventListener("click", () => { s.style = b.dataset.style; applyTheme(); sync(); }));
      box.querySelectorAll("[data-theme]").forEach((b) => b.addEventListener("click", () => { s.theme = ui.theme = b.dataset.theme; applyTheme(); sync(); }));
      sync();
      wire(box);
    }
    function perms(box) {
      const opts = [
        ["ask", "Ask me first", "Approve each file change and command. Recommended."],
        ["edits", "Allow file edits", "Edits go ahead; commands still ask."],
        ["never", "Never ask", "The AI does everything without asking."],
      ];
      box.innerHTML = `<h2>When the AI wants to change things</h2><p>For example, editing a file or running a command.</p>
        <div class="su-list">${opts.map(([id, t, d]) => `<button class="su-row" data-p="${id}"><span class="radio"></span><span><b>${t}</b><span>${d}</span></span></button>`).join("")}</div>${nav()}`;
      const sync = () => box.querySelectorAll("[data-p]").forEach((b) => b.classList.toggle("on", b.dataset.p === s.permissions));
      box.querySelectorAll("[data-p]").forEach((b) => b.addEventListener("click", () => { s.permissions = b.dataset.p; sync(); }));
      sync();
      wire(box);
    }
    async function agents(box) {
      box.innerHTML = `<h2>Your AI tools</h2><p>AIT works with the command-line tools you have installed, using the accounts you're signed in to.</p><div class="su-list su-agents"></div>${nav("Start using AIT")}`;
      wire(box);
      const list = await API().Agents();
      const holder = box.querySelector(".su-agents");
      for (const a of list) {
        const row = document.createElement("div");
        row.className = "su-agent";
        const state = !a.installed ? "Not installed" : a.signedIn ? `Signed in${a.email ? " as " + a.email : ""}` : "Installed. You'll sign in on first use.";
        row.innerHTML = `<span class="icon">${icon(a.id)}</span><span><b>${esc(a.name)}</b><span>${esc(state)}</span></span>`;
        if (!a.installed && INSTALL_HINT[a.id]) {
          const b = document.createElement("button");
          b.className = "btn quiet";
          b.textContent = "Copy install command";
          b.addEventListener("click", () => { RT().ClipboardSetText(INSTALL_HINT[a.id]); b.textContent = "Copied"; });
          row.append(b);
        } else row.insertAdjacentHTML("beforeend", `<span class="su-ok ${a.installed && a.signedIn ? "" : "dim"}">${a.installed && a.signedIn ? "✓" : "–"}</span>`);
        holder.append(row);
      }
    }

    async function finish() {
      s.onboarded = true;
      await API().SaveSettings(s);
      el.classList.add("out");
      setTimeout(() => { el.hidden = true; el.classList.remove("out"); done(); }, 320);
    }

    el.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); next(); }
      else if (e.key === "Escape") { e.preventDefault(); back(); }
    });
    show();
  });
}
