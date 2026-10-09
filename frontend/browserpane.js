// The AIs' browser, inside AIT: a pane beside the chat with the page (a native
// WebView2 that AIT keeps exactly over .bp-page), the steps the AI plans and
// takes, and the questions it asks (a site to open, a step only a person can do).

const BP_WIDTH = "aitBrowserWidth";
let bpWidth = 720;
try { bpWidth = Math.min(Math.max(parseInt(localStorage.getItem(BP_WIDTH)) || 720, 420), 1400); } catch {}

function browserPane(tab, create) {
  if (tab.bp || !create || !tab.chat) return tab.bp;
  const el = document.createElement("div");
  el.className = "bpane";
  el.innerHTML = `
    <div class="bp-grip" title="Drag to resize"></div>
    <div class="bp-main">
      <div class="bp-bar">
        <button class="bp-btn" data-nav="back" title="Back"><span class="mdl">&#xE72B;</span></button>
        <button class="bp-btn" data-nav="forward" title="Forward"><span class="mdl">&#xE72A;</span></button>
        <button class="bp-btn" data-nav="reload" title="Reload"><span class="mdl">&#xE72C;</span></button>
        <span class="bp-url"><span class="mdl bp-lock">&#xE72E;</span><span class="bp-host">New private window</span><span class="bp-title"></span></span>
        <button class="bp-btn bp-logbtn on" data-act="log" title="Steps"><span class="mdl">&#xE8FD;</span></button>
        <button class="bp-btn" data-act="hide" title="Hide the browser"><span class="mdl">&#xE8BB;</span></button>
      </div>
      <div class="bp-page"></div>
    </div>
    <div class="bp-log">
      <div class="bp-lh"><span class="bp-dot"></span><span class="bp-lt"><b></b><span>Idle</span></span></div>
      <div class="bp-asks"></div>
      <div class="bp-steps"><div class="bp-empty">The AI's steps show here.</div></div>
      <div class="bp-foot"></div>
    </div>`;
  tab.pane.append(el);
  const b = tab.bp = { el, page: el.querySelector(".bp-page"), steps: el.querySelector(".bp-steps"), asks: el.querySelector(".bp-asks"),
    dot: el.querySelector(".bp-dot"), status: el.querySelector(".bp-lt span"), last: "", at: "", shown: false, stick: true, seen: false };
  el.querySelector(".bp-lt b").textContent = `${profile(tab.profile).name} is browsing`;
  el.querySelector(".bp-foot").textContent = ui.settings?.browserKeep ? "Logins and cookies are kept between runs (Settings)." : "A private window: nothing is kept after this chat.";
  el.querySelectorAll("[data-nav]").forEach((x) => x.addEventListener("click", () => API().BrowserNav(tab.id, x.dataset.nav)));
  el.querySelector('[data-act="hide"]').addEventListener("click", () => showBrowser(tab, false));
  el.querySelector('[data-act="log"]').addEventListener("click", (e) => {
    const on = el.classList.toggle("nolog");
    e.currentTarget.classList.toggle("on", !on);
  });
  b.steps.addEventListener("scroll", () => { b.stick = b.steps.scrollTop + b.steps.clientHeight >= b.steps.scrollHeight - 24; });
  const grip = el.querySelector(".bp-grip");
  grip.addEventListener("pointerdown", (e) => {
    grip.setPointerCapture(e.pointerId);
    const move = (ev) => {
      const right = tab.pane.getBoundingClientRect().right;
      bpWidth = Math.round(Math.min(Math.max(right - ev.clientX, 420), Math.min(1400, tab.pane.clientWidth - 360)));
      document.documentElement.style.setProperty("--bw", bpWidth + "px");
    };
    const up = () => { grip.removeEventListener("pointermove", move); grip.removeEventListener("pointerup", up); try { localStorage.setItem(BP_WIDTH, bpWidth); } catch {} };
    grip.addEventListener("pointermove", move);
    grip.addEventListener("pointerup", up);
  });
  const chip = tab.chat.root.querySelector(".c-browser");
  if (chip) { chip.hidden = false; chip.onclick = (e) => { e.stopPropagation(); showBrowser(tab, !b.shown); }; }
  document.documentElement.style.setProperty("--bw", bpWidth + "px");
  return b;
}

// The pane's first sign of life opens it, once; after that it is the user's.
function browserSeen(tab) {
  const b = browserPane(tab, true);
  if (b && !b.seen) { b.seen = true; showBrowser(tab, true); }
  return b;
}

function showBrowser(tab, on) {
  const b = tab.bp;
  if (!b) return;
  b.shown = on;
  tab.pane.classList.toggle("bshow", on);
  tab.chat?.root.querySelector(".c-browser")?.classList.toggle("open", on);
  syncBrowser(tab);
}

function browserStep(tab, kind, text) {
  const b = browserSeen(tab);
  if (!b) return;
  b.steps.querySelector(".bp-empty")?.remove();
  const el = document.createElement("div");
  el.className = "bp-s " + kind;
  el.textContent = text;
  b.steps.append(el);
  while (b.steps.children.length > 300) b.steps.firstChild.remove();
  if (kind === "do") b.last = text;
  if (b.stick) b.steps.scrollTop = b.steps.scrollHeight;
  browserStatus(tab);
}

function browserStatus(tab) {
  const b = tab.bp;
  if (!b) return;
  const busy = !!tab.chat?.busy;
  b.dot.classList.toggle("on", busy);
  b.status.textContent = busy ? (b.last || "Working…") : "Idle";
}

function browserPage(tab, url, title) {
  const b = browserSeen(tab);
  if (!b) return;
  let host = "";
  try { host = new URL(url).host; } catch { host = url; }
  const secure = /^https:/.test(url);
  b.el.querySelector(".bp-host").textContent = host || "New private window";
  b.el.querySelector(".bp-title").textContent = title && title !== host ? title : "";
  b.el.querySelector(".bp-lock").innerHTML = secure ? "&#xE72E;" : "&#xE7BA;";
  b.el.querySelector(".bp-lock").classList.toggle("warn", !!url && !secure && !/^about:/.test(url));
}

function browserGone(tab) {
  const b = tab.bp;
  if (!b) return;
  b.el.remove();
  tab.bp = null;
  tab.pane.classList.remove("bshow");
  const chip = tab.chat?.root.querySelector(".c-browser");
  if (chip) { chip.hidden = true; chip.classList.remove("open"); }
}

// What the AI is waiting for, shown in the pane too (they are the cards in the chat).
function browserAsks(tab) {
  const c = tab.chat;
  if (!c) return;
  const mine = [...c.asks.entries()].filter(([req]) => req.startsWith("ait-browser-"));
  const b = mine.length ? browserSeen(tab) : tab.bp;
  if (!b) return;
  b.asks.replaceChildren(...mine.map(([req, a]) => {
    const box = document.createElement("div");
    box.className = "bp-ask";
    const help = req.startsWith("ait-browser-help-");
    box.innerHTML = `<b></b><p></p><div class="row"><button class="go" data-d="allow"></button>${a.always ? '<button data-d="always">Always</button>' : ""}<button data-d="deny"></button></div>`;
    const detail = a.el.querySelector(".ak-d").textContent;
    box.querySelector("b").textContent = help ? `${profile(tab.profile).name} needs your help` : `${profile(tab.profile).name} wants to open a site`;
    box.querySelector("p").textContent = help ? detail : detail.replace(/^open /, "");
    box.querySelector('[data-d="allow"]').textContent = a.labels?.allow || "Allow";
    box.querySelector('[data-d="deny"]').textContent = a.labels?.deny || "Deny";
    box.querySelectorAll("[data-d]").forEach((x) => x.addEventListener("click", () => answerAsk(tab, req, x.dataset.d)));
    return box;
  }));
  if (mine.length) {
    b.el.classList.remove("nolog");
    b.el.querySelector(".bp-logbtn").classList.add("on");
  }
}

// ---- keeping the native page over its place ---------------------------------------

// Anything AIT draws over the pane's page would end up behind the native page.
const BP_OVERLAYS = "#scrim:not([hidden]), .sheet:not([hidden]), #menu:not([hidden]), #modelpop:not([hidden]), #setup:not([hidden]), .ask:not(.out)";

function syncBrowser(tab) {
  const b = tab.bp;
  if (!b) return;
  const visible = b.shown && tab.id === active && !document.hidden && !document.querySelector(BP_OVERLAYS);
  const r = b.page.getBoundingClientRect();
  const key = visible ? [r.x, r.y, r.width, r.height].map(Math.round).join() : "off";
  if (key === b.at) return;
  b.at = key;
  API().BrowserBounds?.(tab.id, r.x, r.y, r.width, r.height, visible)?.catch?.(() => {});
}

setInterval(() => { for (const t of tabs.values()) if (t.bp) syncBrowser(t); }, 80);
