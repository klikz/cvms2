/* CVMS shared UI runtime: icons, DOM helpers, i18n, API, toasts, modals,
 * router, app shell and auth screens. Used by both client and server UI. */
(function () {
  "use strict";
  const CVMS = (window.CVMS = window.CVMS || {});

  /* ------------------------------------------------------------ icons */
  const P = {
    logo: '<path d="M23 7l-7 5 7 5V7z"/><rect x="1" y="5" width="15" height="14" rx="2"/>',
    grid: '<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/>',
    camera: '<path d="M23 7l-7 5 7 5V7z"/><rect x="1" y="5" width="15" height="14" rx="2"/>',
    server: '<rect x="2" y="3" width="20" height="8" rx="2"/><rect x="2" y="13" width="20" height="8" rx="2"/><path d="M6 7h.01M6 17h.01"/>',
    monitor: '<rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/>',
    users: '<path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="4"/><path d="M23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75"/>',
    user: '<path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/>',
    settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>',
    list: '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
    clock: '<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>',
    layout: '<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M3 9h18M9 21V9"/>',
    search: '<circle cx="11" cy="11" r="8"/><path d="m21 21-4.35-4.35"/>',
    plus: '<path d="M12 5v14M5 12h14"/>',
    refresh: '<path d="M23 4v6h-6M1 20v-6h6"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/>',
    trash: '<path d="M3 6h18M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6M10 11v6M14 11v6M9 6V4a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2"/>',
    edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"/>',
    check: '<path d="M20 6 9 17l-5-5"/>',
    x: '<path d="M18 6 6 18M6 6l12 12"/>',
    logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>',
    globe: '<circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>',
    left: '<path d="m15 18-6-6 6-6"/>',
    right: '<path d="m9 18 6-6-6-6"/>',
    maximize: '<path d="M8 3H5a2 2 0 0 0-2 2v3M21 8V5a2 2 0 0 0-2-2h-3M3 16v3a2 2 0 0 0 2 2h3M16 21h3a2 2 0 0 0 2-2v-3"/>',
    eye: '<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>',
    shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>',
    wifi: '<path d="M5 12.55a11 11 0 0 1 14.08 0M1.42 9a16 16 0 0 1 21.16 0M8.53 16.11a6 6 0 0 1 6.95 0M12 20h.01"/>',
    wifioff: '<path d="m1 1 22 22M16.72 11.06A10.94 10.94 0 0 1 19 12.55M5 12.55a10.94 10.94 0 0 1 5.17-2.39M10.71 5.05A16 16 0 0 1 22.58 9M1.42 9a15.91 15.91 0 0 1 4.7-2.88M8.53 16.11a6 6 0 0 1 6.95 0M12 20h.01"/>',
    menu: '<path d="M3 12h18M3 6h18M3 18h18"/>',
    key: '<path d="M21 2l-2 2m-7.61 7.61a5.5 5.5 0 1 1-7.778 7.778 5.5 5.5 0 0 1 7.777-7.777zm0 0L15.5 7.5m0 0 3 3L22 7l-3-3m-3.5 3.5L19 4"/>',
    save: '<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><path d="M17 21v-8H7v8M7 3v5h8"/>',
    pin: '<path d="M21 10c0 7-9 13-9 13s-9-6-9-13a9 9 0 0 1 18 0z"/><circle cx="12" cy="10" r="3"/>',
    link: '<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>',
    ban: '<circle cx="12" cy="12" r="10"/><path d="m4.93 4.93 14.14 14.14"/>',
    home: '<path d="m3 9 9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M9 22V12h6v10"/>',
    image: '<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="m21 15-5-5L5 21"/>',
    activity: '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
    alert: '<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0zM12 9v4M12 17h.01"/>',
    info: '<circle cx="12" cy="12" r="10"/><path d="M12 16v-4M12 8h.01"/>',
    filter: '<path d="M22 3H2l8 9.46V19l4 2v-8.54L22 3z"/>',
    star: '<path d="m12 2 3.09 6.26L22 9.27l-5 4.87 1.18 6.88L12 17.77l-6.18 3.25L7 14.14 2 9.27l6.91-1.01L12 2z"/>',
    folder: '<path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"/>',
    radar: '<path d="M19.07 4.93A10 10 0 0 0 6.99 3.34M4 6h.01M2.29 9.62A10 10 0 1 0 21.31 8.35M16.24 7.76A6 6 0 1 0 8.23 16.67M12 18h.01M17.99 11.66A6 6 0 0 1 15.77 16.67"/><circle cx="12" cy="12" r="2"/><path d="m13.41 10.59 5.66-5.66"/>',
    play: '<path d="m5 3 14 9-14 9V3z"/>',
    pause: '<path d="M6 4h4v16H6zM14 4h4v16h-4z"/>',
  };
  CVMS.icon = function (name, cls) {
    const span = document.createElement("span");
    span.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"' + (cls ? ' class="' + cls + '"' : "") + ">" + (P[name] || P.info) + "</svg>";
    return span.firstChild;
  };

  /* ------------------------------------------------------------ DOM */
  CVMS.el = function (tag, attrs, ...children) {
    const e = document.createElement(tag);
    if (attrs) {
      for (const [k, v] of Object.entries(attrs)) {
        if (v === undefined || v === null || v === false) continue;
        if (k === "class") e.className = v;
        else if (k === "style" && typeof v === "object") Object.assign(e.style, v);
        else if (k.startsWith("on") && typeof v === "function") e.addEventListener(k.slice(2).toLowerCase(), v);
        else if (k === "html") e.innerHTML = v;
        else if (k === "text") e.textContent = v;
        else if (k === "value") e.value = v;
        else if (k === "checked") e.checked = !!v;
        else if (v === true) e.setAttribute(k, "");
        else e.setAttribute(k, v);
      }
    }
    for (const c of children.flat(Infinity)) {
      if (c === null || c === undefined || c === false) continue;
      e.appendChild(c instanceof Node ? c : document.createTextNode(String(c)));
    }
    return e;
  };
  const el = CVMS.el;
  CVMS.clear = (n) => { while (n.firstChild) n.removeChild(n.firstChild); return n; };
  CVMS.btn = function (opts) {
    const b = el("button", { type: opts.type || "button", class: "btn " + (opts.kind ? "btn-" + opts.kind : "") + (opts.size ? " btn-" + opts.size : "") + (opts.label ? "" : " btn-icon") + (opts.cls ? " " + opts.cls : ""), title: opts.title, disabled: opts.disabled, onclick: opts.onClick });
    if (opts.icon) b.appendChild(CVMS.icon(opts.icon));
    if (opts.label) b.appendChild(el("span", null, opts.label));
    return b;
  };

  /* ------------------------------------------------------------ i18n */
  const LANGS = [
    { code: "uz", name: "O'zbekcha" },
    { code: "ru", name: "Русский" },
    { code: "en", name: "English" },
  ];
  const i18n = (CVMS.i18n = { lang: "uz", dict: {}, langs: LANGS, listeners: [] });
  function storageGet(k) { try { return localStorage.getItem(k); } catch (e) { return null; } }
  function storageSet(k, v) { try { localStorage.setItem(k, v); } catch (e) { /* ignore */ } }
  CVMS.store = { get: storageGet, set: storageSet };
  i18n.initial = function () {
    const saved = storageGet("cvms.lang");
    if (saved && LANGS.some((l) => l.code === saved)) return saved;
    const nav = (navigator.language || "uz").slice(0, 2);
    return LANGS.some((l) => l.code === nav) ? nav : "uz";
  };
  i18n.load = async function (lang) {
    if (!LANGS.some((l) => l.code === lang)) lang = "uz";
    try {
      const r = await fetch("shared/i18n/" + lang + ".json", { cache: "no-cache" });
      i18n.dict = await r.json();
      i18n.lang = lang;
      storageSet("cvms.lang", lang);
      document.documentElement.lang = lang;
    } catch (e) { console.error("i18n load", e); }
    i18n.apply(document);
    i18n.listeners.forEach((f) => f(lang));
  };
  i18n.t = function (key, vars) {
    let s = i18n.dict[key];
    if (s === undefined) s = key;
    if (vars) for (const [k, v] of Object.entries(vars)) s = s.split("{" + k + "}").join(v);
    return s;
  };
  i18n.apply = function (root) {
    root.querySelectorAll("[data-i18n]").forEach((n) => (n.textContent = i18n.t(n.dataset.i18n)));
    root.querySelectorAll("[data-i18n-ph]").forEach((n) => (n.placeholder = i18n.t(n.dataset.i18nPh)));
    root.querySelectorAll("[data-i18n-title]").forEach((n) => (n.title = i18n.t(n.dataset.i18nTitle)));
  };
  i18n.onChange = (f) => i18n.listeners.push(f);
  const t = (CVMS.t = i18n.t);

  /* ------------------------------------------------------------ format */
  CVMS.fmt = {
    date(v) {
      if (!v) return "—";
      const d = new Date(v);
      if (isNaN(d)) return "—";
      return d.toLocaleString(i18n.lang === "en" ? "en-GB" : i18n.lang === "ru" ? "ru-RU" : "uz-UZ", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
    },
    ago(v) {
      if (!v) return "—";
      const s = Math.max(0, Math.round((Date.now() - new Date(v).getTime()) / 1000));
      if (s < 60) return t("time.sec", { n: s });
      if (s < 3600) return t("time.min", { n: Math.round(s / 60) });
      if (s < 86400) return t("time.hour", { n: Math.round(s / 3600) });
      return t("time.day", { n: Math.round(s / 86400) });
    },
    res(w, h) { return w && h ? w + "×" + h : ""; },
  };

  /* ------------------------------------------------------------ API */
  CVMS.onUnauthorized = null;
  CVMS.api = async function (path, opts) {
    opts = opts || {};
    const init = { method: opts.method || "GET", headers: {}, credentials: "same-origin" };
    if (opts.body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(opts.body);
    }
    let r;
    try {
      r = await fetch(path, init);
    } catch (e) {
      throw new Error(t("err.network"));
    }
    let data = null;
    const ct = r.headers.get("Content-Type") || "";
    if (ct.includes("application/json")) data = await r.json().catch(() => null);
    if (r.status === 401 && !opts.noAuthRedirect) {
      if (CVMS.onUnauthorized) CVMS.onUnauthorized();
    }
    if (!r.ok) {
      const msg = data && data.error ? (data.code ? t("err." + data.code) !== "err." + data.code ? t("err." + data.code, data.vars) : data.error : data.error) : r.status + " " + r.statusText;
      const err = new Error(msg);
      err.status = r.status;
      err.code = data && data.code;
      throw err;
    }
    return data;
  };

  /* ------------------------------------------------------------ toast */
  let toasts;
  CVMS.toast = function (msg, kind) {
    if (!toasts) { toasts = el("div", { class: "toasts" }); document.body.appendChild(toasts); }
    const icon = kind === "ok" ? "check" : kind === "error" ? "alert" : kind === "warn" ? "alert" : "info";
    const n = el("div", { class: "toast " + (kind || "") }, CVMS.icon(icon), el("div", null, msg));
    n.firstChild.style.cssText = "width:16px;height:16px;flex:none;margin-top:2px";
    toasts.appendChild(n);
    setTimeout(() => { n.style.opacity = "0"; n.style.transition = "opacity .3s"; setTimeout(() => n.remove(), 300); }, kind === "error" ? 6000 : 3500);
  };
  CVMS.err = (e) => CVMS.toast(e && e.message ? e.message : String(e), "error");

  /* ------------------------------------------------------------ modal */
  CVMS.modal = function (opts) {
    const body = el("div", { class: "modal-body" }, opts.body);
    const foot = el("div", { class: "modal-foot" });
    const close = () => { back.remove(); document.removeEventListener("keydown", onKey); if (opts.onClose) opts.onClose(); };
    const onKey = (e) => { if (e.key === "Escape") close(); };
    const m = el("div", { class: "modal" + (opts.wide ? " wide" : "") },
      el("div", { class: "modal-head" }, el("h3", { class: "modal-title" }, opts.title), CVMS.btn({ icon: "x", kind: "ghost", size: "sm", onClick: close })),
      body, opts.actions && opts.actions.length ? foot : null);
    (opts.actions || []).forEach((a) => {
      const b = CVMS.btn({ label: a.label, kind: a.kind, icon: a.icon, type: a.submit ? "submit" : "button" });
      b.addEventListener("click", async () => {
        if (!a.onClick) return close();
        b.disabled = true;
        try { const keep = await a.onClick(close); if (keep !== true) close(); }
        catch (e) { CVMS.err(e); }
        finally { b.disabled = false; }
      });
      foot.appendChild(b);
    });
    const back = el("div", { class: "modal-backdrop", onmousedown: (e) => { if (e.target === back) close(); } }, m);
    document.body.appendChild(back);
    document.addEventListener("keydown", onKey);
    const first = m.querySelector("input,select,textarea");
    if (first) setTimeout(() => first.focus(), 30);
    return { close, body, el: m };
  };
  CVMS.confirm = function (title, text, kind) {
    return new Promise((resolve) => {
      let ok = false;
      CVMS.modal({
        title, body: el("p", { style: { margin: 0, color: "var(--text-2)" } }, text),
        onClose: () => resolve(ok),
        actions: [
          { label: t("common.cancel") },
          { label: t("common.confirm"), kind: kind || "danger", onClick: () => { ok = true; } },
        ],
      });
    });
  };
  CVMS.field = function (label, input, hint) {
    return el("div", { class: "field" }, el("label", null, label), input, hint ? el("div", { class: "hint" }, hint) : null);
  };

  /* ------------------------------------------------------------ router */
  const routes = [];
  CVMS.router = {
    add(pattern, handler) {
      const keys = [];
      const re = new RegExp("^" + pattern.replace(/:[a-zA-Z]+/g, (m) => { keys.push(m.slice(1)); return "([^/]+)"; }) + "$");
      routes.push({ re, keys, handler });
    },
    path() { return location.hash.replace(/^#/, "") || "/"; },
    go(p) { if (location.hash === "#" + p) CVMS.router.resolve(); else location.hash = p; },
    resolve() {
      const p = CVMS.router.path();
      for (const r of routes) {
        const m = p.match(r.re);
        if (m) {
          const params = {};
          r.keys.forEach((k, i) => (params[k] = decodeURIComponent(m[i + 1])));
          if (CVMS.router.beforeEach) CVMS.router.beforeEach(p);
          r.handler(params);
          if (CVMS.shell.renderNav) CVMS.shell.renderNav();
          return;
        }
      }
      if (CVMS.router.fallback) CVMS.router.fallback(p);
    },
    start() { window.addEventListener("hashchange", CVMS.router.resolve); CVMS.router.resolve(); },
  };

  /* ------------------------------------------------------------ shell */
  const shell = (CVMS.shell = {});
  let S = null;
  function brand(sub) {
    return el("div", { class: "brand" },
      el("div", { class: "brand-logo" }, CVMS.icon("logo")),
      el("div", null, el("div", { class: "brand-name" }, "CVMS"), el("div", { class: "brand-sub" }, sub)));
  }
  function langSelect() {
    const s = el("select", { class: "select lang-select", title: t("common.language") },
      LANGS.map((l) => el("option", { value: l.code, selected: l.code === i18n.lang }, l.name)));
    s.addEventListener("change", async () => {
      await i18n.load(s.value);
      if (S && S.opts.onLang) S.opts.onLang(s.value);
    });
    return s;
  }
  shell.langSelect = langSelect;

  /* opts: { brandSub, version, user:{username, role}, nav: () => sections,
   *         onLogout, onChangePassword, onLang } */
  shell.mount = function (opts) {
    const root = document.getElementById("app");
    CVMS.clear(root);
    const nav = el("nav", { class: "nav" });
    const title = el("h1");
    const sub = el("span", { class: "sub" });
    const actions = el("div", { class: "topbar-actions" });
    const content = el("main", { class: "content", id: "view" });
    const conn = el("span", { class: "conn" });
    const app = el("div", { class: "app" });
    const userChip = opts.user ? el("div", { class: "user-chip", tabindex: "0" }, el("span", { class: "avatar" }, (opts.user.username || "?").slice(0, 2)), el("span", { class: "uname" }, opts.user.username)) : null;
    if (userChip) {
      let dd = null;
      const closeDD = () => { if (dd) { dd.remove(); dd = null; document.removeEventListener("mousedown", outside); } };
      const outside = (e) => { if (dd && !userChip.contains(e.target)) closeDD(); };
      userChip.addEventListener("click", (e) => {
        if (dd) { if (!dd.contains(e.target)) closeDD(); return; }
        dd = el("div", { class: "dropdown" },
          el("div", { class: "dropdown-head" }, opts.user.username + (opts.user.role ? " · " + t("role." + opts.user.role) : "")),
          opts.onChangePassword ? el("div", { class: "dropdown-item", onclick: () => { closeDD(); opts.onChangePassword(); } }, CVMS.icon("key"), t("auth.change_password")) : null,
          el("div", { class: "dropdown-sep" }),
          el("div", { class: "dropdown-item", onclick: () => { closeDD(); opts.onLogout && opts.onLogout(); } }, CVMS.icon("logout"), t("auth.logout")));
        userChip.appendChild(dd);
        document.addEventListener("mousedown", outside);
      });
    }
    app.append(
      el("aside", { class: "sidebar" }, brand(opts.brandSub), nav,
        el("div", { class: "sidebar-foot" }, conn, el("span", { class: "mono" }, "v" + (opts.version || "")))),
      el("div", { class: "backdrop-mobile", onclick: () => app.classList.remove("nav-open") }),
      el("div", { class: "main" },
        el("header", { class: "topbar" },
          CVMS.btn({ icon: "menu", kind: "ghost", cls: "menu-toggle", onClick: () => app.classList.toggle("nav-open") }),
          el("div", { class: "topbar-title" }, title, sub),
          actions, langSelect(), userChip),
        content));
    root.appendChild(app);
    S = { opts, nav, title, sub, actions, content, conn, app };
    i18n.onChange(() => { shell.renderNav(); });
    shell.renderNav();
    return S;
  };

  /* Section options: title, items, empty, key (enables collapse + search
   * state), searchable (shows a filter when there are more than 8 items),
   * actions: [{icon, title, active, onClick}], limit (max rendered items). */
  const navQuery = {};
  shell.renderNav = function () {
    if (!S) return;
    const path = CVMS.router.path();
    const focused = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.navSearch : null;
    CVMS.clear(S.nav);
    const sections = S.opts.nav();
    for (const sec of sections) {
      const collapsed = sec.key && storageGet("cvms.nav.collapsed." + sec.key) === "1";
      const actions = (sec.actions || (sec.action ? [sec.action] : [])).map((a) => {
        const b = CVMS.btn({ icon: a.icon, kind: "ghost", size: "sm", title: a.title, onClick: (e) => { e.stopPropagation(); a.onClick(e); } });
        if (a.active) b.classList.add("active");
        return b;
      });
      const head = sec.title ? el("div", { class: "nav-section-title" + (sec.key ? " collapsible" : ""), onclick: sec.key ? () => { storageSet("cvms.nav.collapsed." + sec.key, collapsed ? "0" : "1"); shell.renderNav(); } : null },
        el("span", { class: "row", style: { gap: "4px" } }, sec.key ? CVMS.icon(collapsed ? "right" : "left", "nav-caret" + (collapsed ? "" : " open")) : null, sec.title, sec.badge ? el("span", { class: "nav-count" }, sec.badge) : null),
        actions.length ? el("span", { class: "nav-actions" }, actions) : null) : null;
      const box = el("div", { class: "nav-section" }, head);
      S.nav.appendChild(box);
      if (collapsed) continue;
      let items = sec.items;
      if (sec.searchable && sec.key && (items.length > 8 || navQuery[sec.key])) {
        const inp = el("input", { class: "input nav-search", placeholder: t("common.search"), value: navQuery[sec.key] || "", "data-nav-search": sec.key });
        inp.addEventListener("input", () => { navQuery[sec.key] = inp.value; shell.renderNav(); });
        box.appendChild(inp);
        if (focused === sec.key) setTimeout(() => { inp.focus(); inp.setSelectionRange(inp.value.length, inp.value.length); }, 0);
        const q = (navQuery[sec.key] || "").toLowerCase().trim();
        if (q) items = items.filter((it) => it.label.toLowerCase().includes(q));
      }
      if (!items.length && (sec.empty || sec.items.length)) box.appendChild(el("div", { class: "nav-empty" }, sec.items.length ? t("common.no_match") : sec.empty));
      const limit = sec.limit || 200;
      if (items.length > limit) {
        box.appendChild(el("div", { class: "nav-empty" }, t("common.more", { n: items.length - limit })));
        items = items.slice(0, limit);
      }
      for (const it of items) {
        const href = it.href.replace(/^#/, "");
        const active = it.exact ? path === href : path === href || path.startsWith(href + "/");
        const a = el("a", { class: "nav-item" + (active ? " active" : ""), href: it.href, title: it.label, onclick: () => S.app.classList.remove("nav-open") },
          it.dot ? el("span", { class: "dot " + it.dot }) : CVMS.icon(it.icon || "grid"),
          el("span", { class: "nav-label" }, it.label),
          it.count !== undefined && it.count !== null && it.count !== 0 ? el("span", { class: "nav-count " + (it.countClass || "") }, String(it.count)) : null);
        box.appendChild(a);
      }
    }
  };

  shell.setTitle = function (title, sub) {
    if (!S) return;
    S.title.textContent = title || "";
    S.sub.textContent = sub || "";
    document.title = (title ? title + " · " : "") + "CVMS " + (S.opts.brandSub || "");
  };
  shell.setActions = function (...nodes) { if (S) { CVMS.clear(S.actions); nodes.flat().forEach((n) => n && S.actions.appendChild(n)); } };
  shell.content = function (flush) { if (!S) return null; S.content.className = "content" + (flush ? " flush" : ""); return CVMS.clear(S.content); };
  shell.setConn = function (dotCls, text) { if (S) { CVMS.clear(S.conn); S.conn.append(el("span", { class: "dot " + dotCls }), el("span", null, text)); } };
  shell.unmount = function () { S = null; };

  /* ------------------------------------------------------------ auth screens */
  /* opts: { brandSub, title, version, onSubmit(username, password) -> Promise } */
  CVMS.loginScreen = function (opts) {
    shell.unmount();
    const root = CVMS.clear(document.getElementById("app"));
    const err = el("div", { class: "auth-error hidden" });
    const u = el("input", { class: "input", name: "username", autocomplete: "username", required: true });
    const p = el("input", { class: "input", name: "password", type: "password", autocomplete: "current-password", required: true });
    const submit = CVMS.btn({ label: t("auth.sign_in"), kind: "primary", size: "lg", type: "submit", cls: "btn-block" });
    const form = el("form", { class: "auth-card" },
      brand(opts.brandSub),
      el("h2", null, t("auth.welcome")),
      el("p", { class: "lead" }, opts.title || t("auth.lead")),
      err,
      CVMS.field(t("auth.username"), u),
      CVMS.field(t("auth.password"), p),
      submit,
      el("div", { class: "auth-foot" }, el("span", { class: "mono" }, "v" + (opts.version || "")), langSelect()));
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      err.classList.add("hidden");
      submit.disabled = true;
      try { await opts.onSubmit(u.value.trim(), p.value); }
      catch (ex) { err.textContent = ex.message; err.classList.remove("hidden"); p.value = ""; p.focus(); }
      finally { submit.disabled = false; }
    });
    root.appendChild(el("div", { class: "auth" }, form));
    i18n.listeners.length = 0;
    i18n.onChange(() => CVMS.loginScreen(opts));
    setTimeout(() => u.focus(), 30);
  };

  /* Forced or voluntary password change form.
   * opts: { brandSub, forced, onSubmit(oldPw, newPw) -> Promise, onCancel } */
  CVMS.passwordForm = function (opts) {
    const err = el("div", { class: "auth-error hidden" });
    const o = el("input", { class: "input", type: "password", autocomplete: "current-password", required: true });
    const n1 = el("input", { class: "input", type: "password", autocomplete: "new-password", required: true, minlength: "6" });
    const n2 = el("input", { class: "input", type: "password", autocomplete: "new-password", required: true, minlength: "6" });
    const body = el("div", null, err,
      CVMS.field(t("auth.old_password"), o),
      CVMS.field(t("auth.new_password"), n1, t("auth.password_hint")),
      CVMS.field(t("auth.repeat_password"), n2));
    async function submit() {
      err.classList.add("hidden");
      if (n1.value.length < 6) throw new Error(t("auth.password_hint"));
      if (n1.value !== n2.value) throw new Error(t("auth.password_mismatch"));
      await opts.onSubmit(o.value, n1.value);
    }
    if (opts.forced) {
      shell.unmount();
      const root = CVMS.clear(document.getElementById("app"));
      const btn = CVMS.btn({ label: t("common.save"), kind: "primary", size: "lg", type: "submit", cls: "btn-block" });
      const form = el("form", { class: "auth-card" }, brand(opts.brandSub), el("h2", null, t("auth.change_password")), el("p", { class: "lead" }, t("auth.must_change")), body, btn);
      form.addEventListener("submit", async (e) => {
        e.preventDefault();
        btn.disabled = true;
        try { await submit(); } catch (ex) { err.textContent = ex.message; err.classList.remove("hidden"); } finally { btn.disabled = false; }
      });
      root.appendChild(el("div", { class: "auth" }, form));
      return;
    }
    CVMS.modal({
      title: t("auth.change_password"), body,
      actions: [{ label: t("common.cancel") }, { label: t("common.save"), kind: "primary", onClick: async () => { await submit(); CVMS.toast(t("auth.password_changed"), "ok"); } }],
    });
  };

  /* ------------------------------------------------------------ misc helpers */
  CVMS.emptyState = function (icon, title, text, action) {
    return el("div", { class: "empty-state" }, CVMS.icon(icon), el("h3", null, title), text ? el("p", { style: { margin: "0 0 14px" } }, text) : null, action || null);
  };
  CVMS.loading = function () { return el("div", { class: "empty-state" }, el("div", { class: "spinner", style: { margin: "0 auto" } })); };
  CVMS.statusBadge = function (online) {
    return el("span", { class: "badge badge-dot " + (online ? "badge-ok" : "badge-danger") }, online ? t("status.online") : t("status.offline"));
  };
  CVMS.codecBadge = function (codec, transcoded) {
    if (!codec) return null;
    return el("span", { class: "badge badge-codec " + (codec === "h265" ? "badge-info" : "badge-accent"), title: transcoded ? t("cam.transcoded") : "" }, codec.toUpperCase().replace("H26", "H.26") + (transcoded ? " → H.264" : ""));
  };
  /* Pagination control. With many pages the page number is an input to
   * jump directly. onChange(page) */
  CVMS.pager = function (page, pages, onChange) {
    pages = Math.max(1, pages);
    let info;
    if (pages > 5) {
      const inp = el("input", { class: "input page-input", type: "number", min: "1", max: String(pages), value: String(page), title: t("common.go_to_page") });
      const go = () => { const n = Math.min(pages, Math.max(1, parseInt(inp.value, 10) || 1)); if (n !== page) onChange(n); else inp.value = page; };
      inp.addEventListener("change", go);
      inp.addEventListener("keydown", (e) => { if (e.key === "Enter") go(); });
      info = el("span", { class: "page-info row", style: { gap: "4px" } }, inp, "/ " + pages);
    } else {
      info = el("span", { class: "page-info" }, page + " / " + pages);
    }
    return el("div", { class: "pager" },
      CVMS.btn({ icon: "left", size: "sm", disabled: page <= 1, onClick: () => onChange(page - 1) }),
      info,
      CVMS.btn({ icon: "right", size: "sm", disabled: page >= pages, onClick: () => onChange(page + 1) }));
  };

  /* Paginates and filters a list. Returns {items, page, pages, total}. */
  CVMS.paginate = function (list, page, per) {
    const pages = Math.max(1, Math.ceil(list.length / per));
    page = Math.min(Math.max(1, page), pages);
    return { items: list.slice((page - 1) * per, page * per), page, pages, total: list.length };
  };

  /* Search input with icon. onInput(value) is debounced. */
  CVMS.searchBox = function (value, onInput, opts) {
    opts = opts || {};
    const inp = el("input", { class: "input", placeholder: opts.placeholder || t("common.search"), value: value || "" });
    let timer;
    inp.addEventListener("input", () => { clearTimeout(timer); timer = setTimeout(() => onInput(inp.value.trim()), opts.delay === undefined ? 200 : opts.delay); });
    return el("div", { class: "input-search", style: { width: opts.width || "240px" } }, CVMS.icon("search"), inp);
  };
})();
