/* CVMS server web app. */
(function () {
  "use strict";
  const { el, t, api, btn, icon, fmt } = CVMS;
  const S = { me: null, clients: [], cams: [], camByID: new Map(), layouts: [], favs: new Set(), cleanup: null, refresh: null, ws: null };

  /* ================================================================ boot */
  async function main() {
    await CVMS.i18n.load(CVMS.i18n.initial());
    CVMS.onUnauthorized = () => { if (S.me) { S.me = null; teardownLive(); showLogin(); } };
    try {
      S.me = await api("/api/me", { noAuthRedirect: true });
    } catch (e) {
      return showLogin();
    }
    afterLogin();
  }

  function showLogin() {
    CVMS.loginScreen({
      brandSub: "Server",
      title: t("auth.lead_server"),
      version: S.version || "",
      onSubmit: async (u, p) => {
        S.me = await api("/api/login", { method: "POST", body: { username: u, password: p }, noAuthRedirect: true });
        afterLogin();
      },
    });
    api("/api/ping", { noAuthRedirect: true }).then((r) => { S.version = r.version; const v = document.querySelector(".auth-foot .mono"); if (v) v.textContent = "v" + r.version; }).catch(() => {});
  }

  async function afterLogin() {
    if (S.me.lang && S.me.lang !== CVMS.i18n.lang) await CVMS.i18n.load(S.me.lang);
    CVMS.i18n.listeners.length = 0;
    CVMS.shell.mount({
      brandSub: "Server",
      version: S.me.version,
      user: S.me,
      nav: buildNav,
      onLogout: logout,
      onLang: (lang) => { api("/api/me/lang", { method: "PUT", body: { lang } }).catch(() => {}); CVMS.router.resolve(); },
    });
    CVMS.shell.setConn("warn", t("conn.connecting"));
    Activity.init(S.me.viewer_idle_minutes || 0);
    await refreshData();
    connectEvents();
    registerRoutes();
    CVMS.router.start();
  }

  async function logout() {
    try { await api("/api/logout", { method: "POST" }); } catch (e) { /* ignore */ }
    S.me = null;
    teardownLive();
    showLogin();
  }

  function teardownLive() {
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    if (S.ws) { S.ws.onclose = null; S.ws.close(); S.ws = null; }
    clearTimeout(S.wsTimer);
  }

  /* ================================================================ data */
  async function refreshData() {
    try {
      const [clients, layouts, favs] = await Promise.all([api("/api/clients"), api("/api/layouts"), api("/api/favorites")]);
      S.clients = clients || [];
      S.layouts = layouts || [];
      S.favs = new Set(favs || []);
      S.cams = [];
      S.camByID = new Map();
      for (const c of S.clients) {
        if (c.status !== "approved") continue;
        for (const cam of c.cameras) {
          cam.client = c;
          S.cams.push(cam);
          S.camByID.set(cam.id, cam);
        }
      }
      CVMS.shell.renderNav();
      if (S.refresh) S.refresh();
    } catch (e) {
      if (e.status !== 401) CVMS.err(e);
    }
  }

  /* Live status patches: update in place instead of reloading everything. */
  let navTimer = null;
  function scheduleNav() {
    if (navTimer) return;
    navTimer = setTimeout(() => { navTimer = null; CVMS.shell.renderNav(); }, 1000);
  }
  function notifyStatus() { if (S.onStatus) S.onStatus(); }
  function patchCamera(d) {
    const cam = S.camByID.get(d.camera_id);
    if (!cam) return;
    if (cam.online === d.online) return;
    cam.online = d.online && cam.client.online;
    scheduleNav();
    notifyStatus();
  }
  function patchClient(d) {
    const c = S.clients.find((x) => x.id === d.client_id);
    if (!c) { refreshData(); return; }
    c.online = d.online;
    c.last_seen = d.last_seen;
    if (!d.online) c.cameras.forEach((k) => { k.online = false; });
    scheduleNav();
    notifyStatus();
  }

  function connectEvents() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(proto + "//" + location.host + "/api/events");
    S.ws = ws;
    ws.onopen = () => CVMS.shell.setConn("ok", t("conn.live"));
    ws.onmessage = (ev) => {
      let m;
      try { m = JSON.parse(ev.data); } catch (e) { return; }
      if (m.type === "clients") refreshData();
      else if (m.type === "camera_status") patchCamera(m.data);
      else if (m.type === "client_status") patchClient(m.data);
    };
    ws.onclose = () => {
      if (!S.me) return;
      CVMS.shell.setConn("off", t("conn.offline"));
      S.wsTimer = setTimeout(() => { if (S.me) { connectEvents(); refreshData(); } }, 3000);
    };
  }

  /* ================================================================ nav */
  function buildNav() {
    const sections = [{
      items: [{ href: "#/favorites", icon: "star", label: t("nav.favorites"), count: S.favs.size || null }],
    }];
    const approved = S.clients.filter((c) => c.status === "approved");
    const offlineOnly = CVMS.store.get("cvms.nav.offline") === "1";
    const offline = approved.filter((c) => !c.online).length;
    sections.push({
      title: t("nav.clients"),
      key: "clients",
      searchable: true,
      badge: approved.length > 8 ? (offline ? offline + "/" : "") + approved.length : null,
      empty: offlineOnly ? t("nav.no_offline") : t("nav.no_clients"),
      actions: approved.length ? [{ icon: "wifioff", title: t("nav.offline_only"), active: offlineOnly, onClick: () => { CVMS.store.set("cvms.nav.offline", offlineOnly ? "0" : "1"); CVMS.shell.renderNav(); } }] : [],
      items: approved
        .filter((c) => !offlineOnly || !c.online)
        .map((c) => ({ href: "#/client/" + c.id, dot: c.online ? "ok" : "off", label: c.name, count: c.cameras.length })),
    });
    if (S.me && S.me.role === "admin") {
      const pending = S.clients.filter((c) => c.status === "pending").length;
      sections.push({
        title: t("nav.admin"),
        items: [
          { href: "#/admin/clients", icon: "server", label: t("nav.manage_clients"), count: pending || null, countClass: "warn" },
          { href: "#/admin/users", icon: "users", label: t("nav.users") },
          { href: "#/admin/audit", icon: "list", label: t("nav.audit") },
          { href: "#/settings", icon: "settings", label: t("nav.settings") },
        ],
      });
    }
    return sections;
  }

  function page(title, sub, flush) {
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    S.refresh = null;
    S.onStatus = null;
    CVMS.shell.setTitle(title, sub);
    CVMS.shell.setActions();
    return CVMS.shell.content(flush);
  }

  function registerRoutes() {
    const R = CVMS.router;
    R.add("/", () => R.go(S.favs.size ? "/favorites" : "/grid"));
    R.add("/dashboard", dashboardPage);
    R.add("/grid", () => gridPage({ mode: "all" }));
    R.add("/favorites", () => gridPage({ mode: "favorites" }));
    R.add("/client/:id", (p) => gridPage({ mode: "client", clientId: +p.id }));
    R.add("/layout/:id", (p) => gridPage({ mode: "layout", layoutId: +p.id }));
    R.add("/layouts", layoutsPage);
    R.add("/admin/clients", () => adminGuard(clientsPage));
    R.add("/admin/users", () => adminGuard(usersPage));
    R.add("/admin/audit", () => adminGuard(auditPage));
    R.add("/settings", () => adminGuard(settingsPage));
    R.fallback = () => R.go("/");
  }

  function adminGuard(fn) {
    if (!S.me || S.me.role !== "admin") return CVMS.router.go("/");
    fn();
  }

  /* ================================================================ activity (traffic saving) */
  const Activity = {
    listeners: new Set(),
    paused: false,
    idle: false,
    timer: null,
    minutes: 0,
    overlay: null,
    init(minutes) {
      this.minutes = minutes;
      if (this.inited) return;
      this.inited = true;
      const reset = () => this.touch();
      ["mousemove", "mousedown", "keydown", "touchstart", "wheel"].forEach((e) => document.addEventListener(e, reset, { passive: true }));
      document.addEventListener("visibilitychange", () => this.update());
      this.touch();
    },
    touch() {
      if (this.idle) return; // must click the overlay to resume
      clearTimeout(this.timer);
      if (this.minutes > 0) this.timer = setTimeout(() => { this.idle = true; this.showOverlay(); this.update(); }, this.minutes * 60000);
    },
    showOverlay() {
      if (this.overlay) return;
      const resume = () => { this.overlay.remove(); this.overlay = null; this.idle = false; this.touch(); this.update(); };
      this.overlay = el("div", { class: "idle-overlay" },
        el("div", { class: "idle-card" }, icon("pause"), el("h3", null, t("idle.title")), el("p", null, t("idle.text")),
          btn({ label: t("idle.resume"), icon: "play", kind: "primary", onClick: resume })));
      document.body.appendChild(this.overlay);
    },
    update() {
      const paused = document.hidden || this.idle;
      if (paused === this.paused) return;
      this.paused = paused;
      this.listeners.forEach((f) => f(paused));
    },
    on(f) { this.listeners.add(f); return () => this.listeners.delete(f); },
  };

  /* ================================================================ dashboard */
  async function dashboardPage() {
    const c = page(t("nav.dashboard"));
    const statsBox = el("div", { class: "stats" });
    const listBox = el("div", { class: "card" });
    c.append(statsBox, listBox);
    const stat = (ic, cls, value, label) => el("div", { class: "stat" }, el("div", { class: "stat-icon " + cls }, icon(ic)), el("div", null, el("div", { class: "stat-value" }, value), el("div", { class: "stat-label" }, label)));
    async function render() {
      let info;
      try { info = await api("/api/info"); } catch (e) { return CVMS.err(e); }
      CVMS.clear(statsBox).append(
        stat("server", "", info.clients_online + " / " + info.clients, t("dash.clients_online")),
        stat("camera", "ok", info.cameras_online + " / " + info.cameras, t("dash.cameras_online")),
        S.me.role === "admin" ? stat("clock", info.pending ? "warn" : "", String(info.pending || 0), t("dash.pending")) : null,
        S.me.role === "admin" ? stat("users", "", String(info.users || 0), t("dash.users")) : null);
      renderList();
    }
    const dl = { q: "", page: 1 };
    function renderList() {
      const all = S.clients.filter((x) => x.status === "approved");
      const q = dl.q.toLowerCase();
      const filtered = all.filter((x) => !q || x.name.toLowerCase().includes(q) || (x.hostname || "").toLowerCase().includes(q))
        .sort((a, b) => (a.online === b.online ? a.name.localeCompare(b.name) : a.online ? 1 : -1)); // offline first
      const pg = CVMS.paginate(filtered, dl.page, 25);
      dl.page = pg.page;
      const approved = pg.items;
      search.style.display = all.length > 10 ? "" : "none";
      CVMS.clear(listBody).append(
        approved.length ? el("div", { class: "table-wrap", style: { marginTop: "8px" } }, el("table", { class: "table" },
          el("thead", null, el("tr", null, el("th", null, t("col.name")), el("th", null, t("col.status")), el("th", null, t("col.cameras")), el("th", null, t("col.last_seen")), el("th"))),
          el("tbody", null, approved.map((x) => {
            const online = x.cameras.filter((k) => k.online).length;
            return el("tr", null,
              el("td", null, el("div", { class: "row" }, el("span", { class: "dot " + (x.online ? "ok" : "off") }), el("b", null, x.name))),
              el("td", null, CVMS.statusBadge(x.online)),
              el("td", null, online + " / " + x.cameras.length),
              el("td", { class: "muted" }, x.online ? t("status.now") : fmt.ago(x.last_seen)),
              el("td", { class: "actions" }, btn({ label: t("common.open"), icon: "eye", size: "sm", onClick: () => CVMS.router.go("/client/" + x.id) })));
          })))) : CVMS.emptyState("server", all.length ? t("common.no_match") : t("dash.no_clients"), all.length ? "" : S.me.role === "admin" ? t("dash.no_clients_admin") : t("dash.no_clients_user")),
        pg.pages > 1 ? el("div", { class: "card-body row", style: { justifyContent: "flex-end" } }, CVMS.pager(pg.page, pg.pages, (p) => { dl.page = p; renderList(); })) : null);
    }
    const search = CVMS.searchBox("", (v) => { dl.q = v; dl.page = 1; renderList(); });
    const listBody = el("div");
    listBox.append(el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("nav.clients")), el("p", { class: "card-sub" }, t("dash.clients_sub"))), search), listBody);
    S.refresh = render;
    S.onStatus = renderList;
    render();
  }

  /* ================================================================ live grid */
  function camLabel(cam) { return cam.name; }

  function gridPage(opts) {
    let layout = null;
    if (opts.mode === "layout") {
      layout = S.layouts.find((l) => l.id === opts.layoutId);
      if (!layout) return CVMS.router.go("/layouts");
    }
    const client = opts.mode === "client" ? S.clients.find((c) => c.id === opts.clientId) : null;
    if (opts.mode === "client" && !client) return CVMS.router.go("/grid");
    const fav = opts.mode === "favorites";
    const title = layout ? layout.name : client ? client.name : fav ? t("nav.favorites") : t("nav.live");
    const c = page(title, null, true);
    const orderKey = "cvms.order." + (client ? "c" + client.id : fav ? "fav" : "all");
    const st = {
      size: layout ? layout.grid : +(CVMS.store.get("cvms.grid") || 2),
      page: 1,
      loc: "",
      client: 0,
      q: "",
      order: layout ? layout.cameras.slice() : JSON.parse(CVMS.store.get(orderKey) || "[]"),
    };
    const players = new Map();
    let viewer = null;
    let sig = "";

    function allCams() {
      if (layout) return st.order.map((id) => S.camByID.get(id)).filter(Boolean);
      let list = client ? S.cams.filter((k) => k.client.id === client.id) : S.cams.slice();
      if (fav) list = list.filter((k) => S.favs.has(k.id));
      if (st.client) list = list.filter((k) => k.client.id === st.client);
      if (st.loc) list = list.filter((k) => (k.location || "") === st.loc);
      if (st.q) { const q = st.q.toLowerCase(); list = list.filter((k) => k.name.toLowerCase().includes(q) || (k.location || "").toLowerCase().includes(q) || k.client.name.toLowerCase().includes(q)); }
      const pos = new Map(st.order.map((id, i) => [id, i]));
      return list.sort((a, b) => (pos.has(a.id) ? pos.get(a.id) : 1e9 + a.id) - (pos.has(b.id) ? pos.get(b.id) : 1e9 + b.id));
    }

    const wrap = el("div", { class: "grid-wrap" });
    const toolbar = el("div", { class: "toolbar" });
    const area = el("div", { class: "grid-area" });
    wrap.append(toolbar, area);
    c.appendChild(wrap);

    function sizeGroup() {
      return el("div", { class: "btn-group" }, [1, 2, 3, 4].map((n) => {
        const b = btn({ label: n + "×" + n, size: "sm", onClick: () => setSize(n) });
        if (n === st.size) b.classList.add("active");
        return b;
      }));
    }
    function setSize(n) {
      st.size = n; st.page = 1;
      if (layout) { layout.grid = n; saveLayout(); } else CVMS.store.set("cvms.grid", String(n));
      render(true);
    }

    function renderToolbar(list, pages) {
      CVMS.clear(toolbar);
      toolbar.append(sizeGroup());
      if (!layout && !client) {
        const withCams = S.clients.filter((x) => x.status === "approved" && x.cameras.length).sort((a, b) => a.name.localeCompare(b.name));
        if (withCams.length > 1) {
          const cs = el("select", { class: "select", style: { width: "auto", maxWidth: "200px", height: "26px", fontSize: "12px" } },
            el("option", { value: "0" }, t("grid.all_clients")), withCams.map((x) => el("option", { value: x.id, selected: x.id === st.client }, x.name + " (" + x.cameras.length + ")")));
          cs.onchange = () => { st.client = +cs.value; st.page = 1; render(true); };
          toolbar.append(cs);
        }
      }
      if (!layout) {
        const locs = [...new Set((client ? S.cams.filter((k) => k.client.id === client.id) : S.cams).map((k) => k.location).filter(Boolean))].sort();
        if (locs.length) {
          const sel = el("select", { class: "select", style: { width: "auto", height: "26px", fontSize: "12px" } },
            el("option", { value: "" }, t("grid.all_locations")), locs.map((l) => el("option", { value: l, selected: l === st.loc }, l)));
          sel.onchange = () => { st.loc = sel.value; st.page = 1; render(true); };
          toolbar.append(sel);
        }
        const q = el("input", { class: "input", placeholder: t("common.search"), value: st.q, style: { width: "180px", height: "26px", fontSize: "12px" } });
        q.oninput = () => { st.q = q.value; st.page = 1; render(true); setTimeout(() => { const n = toolbar.querySelector("input"); if (n) { n.focus(); n.setSelectionRange(n.value.length, n.value.length); } }, 0); };
        toolbar.append(q);
      }
      toolbar.append(el("span", { class: "muted", style: { fontSize: "12px" } }, t("grid.count", { n: list.length })));
      toolbar.append(el("div", { class: "spacer" }));
      toolbar.append(CVMS.pager(st.page, pages, (p) => { st.page = p; render(true); }));
      if (layout) {
        toolbar.append(btn({ label: t("layout.edit_cameras"), icon: "edit", size: "sm", onClick: editLayoutCameras }));
      }
      toolbar.append(btn({ icon: "maximize", size: "sm", title: t("grid.fullscreen"), onClick: () => { const n = document.getElementById("view"); if (document.fullscreenElement) document.exitFullscreen(); else if (n.requestFullscreen) n.requestFullscreen(); } }));
    }

    function stopPlayers() { players.forEach((p) => p.destroy()); players.clear(); }

    function render(force) {
      const list = allCams();
      const per = st.size * st.size;
      const pages = Math.max(1, Math.ceil(list.length / per));
      if (st.page > pages) st.page = pages;
      const slice = list.slice((st.page - 1) * per, st.page * per);
      const newSig = st.size + "|" + slice.map((k) => k.id).join(",");
      if (force || newSig !== sig || !toolbar.firstChild) renderToolbar(list, pages);
      if (!force && newSig === sig) {
        // same cameras on the page: update names and rebuild only the tiles
        // whose online state changed (other players keep running)
        slice.forEach((k) => {
          const old = area.querySelector('.tile[data-id="' + k.id + '"]');
          if (!old) return;
          if (old.dataset.online !== String(!!k.online)) {
            const p = players.get(k.id);
            // video still arrives: an offline status report is stale, keep
            // the running player (it shows its own error if frames stop)
            if (!k.online && p && p.kind === "playing") return;
            if (p) { p.destroy(); players.delete(k.id); }
            const nt = tile(k);
            old.replaceWith(nt);
            const np = players.get(k.id);
            if (np && !Activity.paused && !viewer) np.start();
          } else {
            const n = old.querySelector(".tile-name");
            if (n) n.textContent = camLabel(k);
          }
        });
        return;
      }
      sig = newSig;
      stopPlayers();
      CVMS.clear(area);
      if (!list.length) {
        area.appendChild(CVMS.emptyState(fav ? "star" : "camera", fav ? t("fav.empty") : t("grid.empty"), layout ? t("grid.empty_layout") : fav ? t("fav.empty_text") : t("grid.empty_text"),
          layout ? btn({ label: t("layout.edit_cameras"), icon: "edit", kind: "primary", onClick: editLayoutCameras }) : null));
        return;
      }
      const grid = el("div", { class: "cam-grid fit g" + st.size, style: { width: "100%" } });
      slice.forEach((cam) => grid.appendChild(tile(cam)));
      for (let i = slice.length; i < per; i++) grid.appendChild(el("div", { class: "tile empty" }, icon("camera")));
      grid.querySelectorAll(".tile.empty svg").forEach((s) => { s.style.width = "28px"; s.style.height = "28px"; s.style.opacity = ".3"; });
      area.appendChild(grid);
      if (!Activity.paused && !viewer) players.forEach((p) => p.start());
    }

    let dragID = null;
    function tile(cam) {
      const badges = el("div", { class: "tile-badges" });
      const name = el("span", { class: "tile-name" }, camLabel(cam));
      const n = el("div", { class: "tile" + (cam.online ? "" : " offline"), "data-id": cam.id, "data-online": String(!!cam.online), draggable: "true", title: cam.client.name + " · " + cam.name + "\n" + t("grid.dblclick") },
        el("div", { class: "tile-bar" }, el("span", { class: "dot " + (cam.online ? "ok" : "off") }), name, badges));
      if (cam.location) badges.append(el("span", { class: "badge loc" }, cam.location));
      const hasSub = !!(cam.sub && cam.sub.available);
      const codec = hasSub ? cam.sub.codec : cam.main.codec;
      badges.append(hasSub
        ? el("span", { class: "badge badge-codec", title: t("grid.stream_sub") }, "SUB")
        : el("span", { class: "badge badge-codec stream-main", title: t("grid.no_sub") }, "MAIN"));
      if (codec) badges.append(el("span", { class: "badge badge-codec" }, codec.toUpperCase()));
      const star = el("button", { class: "tile-star" + (S.favs.has(cam.id) ? " on" : ""), type: "button", title: t("fav.toggle"), draggable: "false" }, icon("star"));
      star.addEventListener("click", (e) => { e.stopPropagation(); toggleFav(cam, star); });
      star.addEventListener("dblclick", (e) => e.stopPropagation());
      n.appendChild(star);
      n.addEventListener("dblclick", () => openViewer(cam));
      n.addEventListener("dragstart", (e) => { dragID = cam.id; n.classList.add("dragging"); e.dataTransfer.effectAllowed = "move"; e.dataTransfer.setData("text/plain", String(cam.id)); });
      n.addEventListener("dragend", () => { n.classList.remove("dragging"); area.querySelectorAll(".drag-over").forEach((x) => x.classList.remove("drag-over")); });
      n.addEventListener("dragover", (e) => { if (dragID && dragID !== cam.id) { e.preventDefault(); n.classList.add("drag-over"); } });
      n.addEventListener("dragleave", () => n.classList.remove("drag-over"));
      n.addEventListener("drop", (e) => { e.preventDefault(); n.classList.remove("drag-over"); if (dragID && dragID !== cam.id) swap(dragID, cam.id); dragID = null; });
      if (cam.online) {
        players.set(cam.id, new CVMS.Player(n, { cameraId: cam.id, profile: "sub", codec: codec }));
      } else {
        const img = el("img", { alt: "", src: "/api/cameras/" + cam.id + "/snapshot?profile=sub" });
        img.onerror = () => img.remove();
        img.style.opacity = ".35";
        n.prepend(img);
        n.appendChild(el("div", { class: "tile-state" }, el("div", null, icon("wifioff"), el("div", null, t("status.offline")))));
        n.querySelector(".tile-state svg").style.cssText = "width:22px;height:22px;margin-bottom:4px";
      }
      return n;
    }

    async function toggleFav(cam, star) {
      const on = !S.favs.has(cam.id);
      if (on) S.favs.add(cam.id); else S.favs.delete(cam.id);
      star.classList.toggle("on", on);
      CVMS.shell.renderNav();
      try {
        await api("/api/favorites/" + cam.id, { method: on ? "PUT" : "DELETE" });
        if (fav && !on) render(true);
      } catch (e) {
        if (on) S.favs.delete(cam.id); else S.favs.add(cam.id);
        star.classList.toggle("on", !on);
        CVMS.shell.renderNav();
        CVMS.err(e);
      }
    }

    function swap(a, b) {
      const ids = allCams().map((k) => k.id);
      const ia = ids.indexOf(a), ib = ids.indexOf(b);
      if (ia < 0 || ib < 0) return;
      [ids[ia], ids[ib]] = [ids[ib], ids[ia]];
      if (layout) {
        st.order = ids;
        layout.cameras = ids;
        saveLayout();
      } else {
        const rest = st.order.filter((id) => !ids.includes(id));
        st.order = ids.concat(rest);
        CVMS.store.set(orderKey, JSON.stringify(st.order));
      }
      render(true);
    }

    function openViewer(cam) {
      players.forEach((p) => p.stop());
      const body = el("div", { class: "viewer-body live" });
      const badges = el("div", { class: "row" });
      const close = () => {
        p.destroy(); v.remove(); document.removeEventListener("keydown", onKey); viewer = null;
        if (!Activity.paused) players.forEach((x) => x.start());
      };
      const onKey = (e) => { if (e.key === "Escape") close(); };
      const v = el("div", { class: "viewer" },
        el("div", { class: "viewer-head" },
          el("span", { class: "dot " + (cam.online ? "ok" : "off") }),
          el("div", { class: "viewer-title" }, cam.client.name + " · " + cam.name),
          badges,
          btn({ icon: "maximize", kind: "ghost", title: t("grid.fullscreen"), onClick: () => { if (document.fullscreenElement) document.exitFullscreen(); else v.requestFullscreen && v.requestFullscreen(); } }),
          btn({ icon: "x", kind: "ghost", onClick: close })),
        body);
      v.addEventListener("dblclick", (e) => { if (e.target.closest(".viewer-body")) close(); });
      document.body.appendChild(v);
      document.addEventListener("keydown", onKey);
      const p = new CVMS.Player(body, {
        cameraId: cam.id, profile: "main", codec: cam.main.codec,
        onState: (s, info) => {
          CVMS.clear(badges);
          if (cam.main.width) badges.append(el("span", { class: "badge" }, fmt.res(cam.main.width, cam.main.height)));
          const cb = CVMS.codecBadge(info.codec || cam.main.codec, !!info.transcoded);
          if (cb) badges.append(cb);
          if (info.transport === "mse") badges.append(el("span", { class: "badge badge-warn" }, "WS"));
        },
      });
      viewer = { p, close };
      if (!Activity.paused) p.start();
    }

    async function saveLayout() {
      try {
        const saved = await api("/api/layouts/" + layout.id, { method: "PUT", body: { name: layout.name, grid: layout.grid, cameras: layout.cameras } });
        Object.assign(layout, saved);
      } catch (e) { CVMS.err(e); }
    }

    function saveAsLayout(list) {
      const name = el("input", { class: "input", required: true, maxlength: "80" });
      CVMS.modal({
        title: t("layout.save_as"),
        body: el("div", null, CVMS.field(t("col.name"), name), el("p", { class: "muted", style: { margin: 0, fontSize: "12px" } }, t("layout.save_hint", { n: list.length, g: st.size + "×" + st.size }))),
        actions: [{ label: t("common.cancel") }, {
          label: t("common.save"), kind: "primary", onClick: async () => {
            if (!name.value.trim()) throw new Error(t("err.name_required"));
            const l = await api("/api/layouts", { method: "POST", body: { name: name.value.trim(), grid: st.size, cameras: list.map((k) => k.id) } });
            S.layouts.push(l);
            CVMS.shell.renderNav();
            CVMS.toast(t("layout.saved"), "ok");
            CVMS.router.go("/layout/" + l.id);
          },
        }],
      });
    }

    function editLayoutCameras() {
      pickCameras(layout.cameras, async (ids) => {
        layout.cameras = ids;
        st.order = ids.slice();
        await saveLayout();
        render(true);
      });
    }

    if (layout) {
      CVMS.shell.setActions(
        btn({ icon: "edit", kind: "ghost", title: t("common.rename"), onClick: () => renameLayout(layout, () => CVMS.shell.setTitle(layout.name)) }),
        btn({ icon: "trash", kind: "ghost", title: t("common.delete"), onClick: () => deleteLayout(layout, () => CVMS.router.go("/layouts")) }));
    }

    const offAct = Activity.on((paused) => {
      if (paused) { players.forEach((p) => p.stop()); if (viewer) viewer.p.stop(); }
      else if (viewer) viewer.p.start();
      else players.forEach((p) => p.start());
    });
    S.cleanup = () => { offAct(); stopPlayers(); if (viewer) viewer.close(); };
    S.refresh = () => {
      if (layout && !S.layouts.includes(layout)) return;
      render(false);
    };
    S.onStatus = () => render(false);
    render(true);
  }

  /* ================================================================ layouts */
  function pickCameras(selected, onSave) {
    const chosen = new Set(selected);
    const groups = el("div");
    const counter = el("span", { class: "muted", style: { fontSize: "12px" } });
    const approved = S.clients.filter((c) => c.status === "approved" && c.cameras.length);
    let q = "";
    const collapsed = new Set(approved.length > 5 ? approved.map((c) => c.id) : []);
    const count = () => { counter.textContent = t("layout.selected", { n: chosen.size }); };
    function render() {
      CVMS.clear(groups);
      if (!approved.length) { groups.append(el("p", { class: "muted" }, t("grid.empty_text"))); return; }
      const needle = q.toLowerCase();
      let shown = 0;
      approved.forEach((c) => {
        const cams = c.cameras.filter((k) => !needle || k.name.toLowerCase().includes(needle) || (k.location || "").toLowerCase().includes(needle) || c.name.toLowerCase().includes(needle));
        if (!cams.length) return;
        shown++;
        const open = needle || !collapsed.has(c.id);
        const picked = cams.filter((k) => chosen.has(k.id)).length;
        const all = el("input", { type: "checkbox", checked: picked === cams.length, title: t("common.select_all") });
        all.indeterminate = picked > 0 && picked < cams.length;
        all.onchange = () => { cams.forEach((k) => (all.checked ? chosen.add(k.id) : chosen.delete(k.id))); render(); };
        const head = el("h4", { style: { cursor: "pointer" }, onclick: (e) => { if (e.target === all) return; if (collapsed.has(c.id)) collapsed.delete(c.id); else collapsed.add(c.id); render(); } },
          el("label", { class: "check", onclick: (e) => e.stopPropagation() }, all),
          el("span", { class: "dot " + (c.online ? "ok" : "off") }), c.name,
          el("span", { class: "dim", style: { fontWeight: 400 } }, picked + "/" + cams.length),
          icon(open ? "left" : "right", "nav-caret" + (open ? " open" : "")));
        groups.append(el("div", { class: "cam-pick-group" }, head,
          open ? el("div", { class: "list-check" }, cams.map((cam) => {
            const cb = el("input", { type: "checkbox", checked: chosen.has(cam.id) });
            cb.onchange = () => { if (cb.checked) chosen.add(cam.id); else chosen.delete(cam.id); all.checked = cams.every((k) => chosen.has(k.id)); all.indeterminate = !all.checked && cams.some((k) => chosen.has(k.id)); count(); };
            return el("label", { class: "check" }, cb, cam.name, cam.location ? el("span", { class: "dim" }, " · " + cam.location) : null);
          })) : null));
      });
      if (!shown) groups.append(el("p", { class: "muted" }, t("common.no_match")));
      count();
    }
    render();
    const body = el("div", null,
      el("div", { class: "list-tools" }, CVMS.searchBox("", (v) => { q = v; render(); }, { width: "260px" }), el("div", { class: "spacer", style: { flex: 1 } }), counter),
      groups);
    CVMS.modal({
      title: t("layout.edit_cameras"), wide: true, body,
      actions: [{ label: t("common.cancel") }, {
        label: t("common.save"), kind: "primary", onClick: async () => {
          // keep existing order, append new ones
          const ids = selected.filter((id) => chosen.has(id));
          chosen.forEach((id) => { if (!ids.includes(id)) ids.push(id); });
          await onSave(ids);
        },
      }],
    });
  }

  function renameLayout(l, done) {
    const name = el("input", { class: "input", value: l.name, maxlength: "80" });
    CVMS.modal({
      title: t("common.rename"), body: CVMS.field(t("col.name"), name),
      actions: [{ label: t("common.cancel") }, {
        label: t("common.save"), kind: "primary", onClick: async () => {
          if (!name.value.trim()) throw new Error(t("err.name_required"));
          const saved = await api("/api/layouts/" + l.id, { method: "PUT", body: { name: name.value.trim(), grid: l.grid, cameras: l.cameras } });
          Object.assign(l, saved);
          CVMS.shell.renderNav();
          if (done) done();
        },
      }],
    });
  }

  async function deleteLayout(l, done) {
    if (!(await CVMS.confirm(t("common.delete"), t("layout.delete_confirm", { name: l.name })))) return;
    try {
      await api("/api/layouts/" + l.id, { method: "DELETE" });
      S.layouts = S.layouts.filter((x) => x.id !== l.id);
      CVMS.shell.renderNav();
      if (done) done();
    } catch (e) { CVMS.err(e); }
  }

  function layoutsPage() {
    const c = page(t("nav.layouts"), t("layout.sub"));
    CVMS.shell.setActions(btn({ label: t("layout.new"), icon: "plus", kind: "primary", onClick: newLayout }));
    function render() {
      CVMS.clear(c);
      if (!S.layouts.length) {
        c.append(el("div", { class: "card" }, CVMS.emptyState("layout", t("layout.empty"), t("layout.empty_text"), btn({ label: t("layout.new"), icon: "plus", kind: "primary", onClick: newLayout }))));
        return;
      }
      c.append(el("div", { class: "layout-cards" }, S.layouts.map((l) => {
        const n = l.grid * l.grid;
        const mini = el("div", { class: "mini", style: { gridTemplateColumns: "repeat(" + l.grid + ",1fr)" } });
        for (let i = 0; i < n; i++) mini.append(el("span", { class: i < l.cameras.length ? "on" : "" }));
        const card = el("div", { class: "card layout-card", onclick: () => CVMS.router.go("/layout/" + l.id) },
          mini,
          el("div", { class: "lc-head" },
            el("span", { class: "lc-name" }, l.name),
            btn({ icon: "edit", kind: "ghost", size: "sm", title: t("common.rename"), onClick: (e) => { e.stopPropagation(); renameLayout(l, render); } }),
            btn({ icon: "trash", kind: "ghost", size: "sm", title: t("common.delete"), onClick: (e) => { e.stopPropagation(); deleteLayout(l, render); } })),
          el("div", { class: "row muted", style: { fontSize: "12px" } }, el("span", { class: "badge" }, l.grid + "×" + l.grid), t("layout.cam_count", { n: l.cameras.length })));
        return card;
      })));
    }
    function newLayout() {
      const name = el("input", { class: "input", maxlength: "80" });
      const grid = el("select", { class: "select" }, [1, 2, 3, 4].map((n) => el("option", { value: n, selected: n === 2 }, n + "×" + n)));
      CVMS.modal({
        title: t("layout.new"), body: el("div", null, CVMS.field(t("col.name"), name), CVMS.field(t("layout.grid"), grid)),
        actions: [{ label: t("common.cancel") }, {
          label: t("common.create"), kind: "primary", onClick: async () => {
            if (!name.value.trim()) throw new Error(t("err.name_required"));
            const l = await api("/api/layouts", { method: "POST", body: { name: name.value.trim(), grid: +grid.value, cameras: [] } });
            S.layouts.push(l);
            CVMS.shell.renderNav();
            CVMS.router.go("/layout/" + l.id);
          },
        }],
      });
    }
    S.refresh = render;
    render();
  }

  /* ================================================================ admin: clients */
  function clientsPage() {
    const c = page(t("nav.manage_clients"), t("clients.sub"));
    let tab = CVMS.store.get("cvms.clients.tab") || "all";
    const open = new Set();
    const tabs = el("div", { class: "tabs" });
    const box = el("div", { class: "card" });
    const ls = { q: "", page: 1 };
    const tools = el("div", { class: "toolbar" }, CVMS.searchBox("", (v) => { ls.q = v; ls.page = 1; render(); }, { placeholder: t("clients.search_ph"), width: "280px" }));
    c.append(tabs, tools, box);

    function render() {
      const counts = { all: S.clients.length, pending: 0, approved: 0, blocked: 0 };
      S.clients.forEach((x) => { counts[x.status] = (counts[x.status] || 0) + 1; });
      if (tab !== "all" && !counts[tab] && tab !== "approved") tab = "all";
      CVMS.clear(tabs).append(...["all", "pending", "approved", "blocked"].map((k) => {
        const b = el("button", { class: "tab" + (tab === k ? " active" : ""), onclick: () => { tab = k; CVMS.store.set("cvms.clients.tab", k); render(); } }, t("clients.tab_" + k) + " (" + (counts[k] || 0) + ")");
        return b;
      }));
      const byTab = S.clients.filter((x) => tab === "all" || x.status === tab);
      const needle = ls.q.toLowerCase();
      const filtered = byTab.filter((x) => !needle || [x.name, x.hostname, x.ip, x.os].some((v) => (v || "").toLowerCase().includes(needle)) ||
        x.cameras.some((k) => k.name.toLowerCase().includes(needle) || (k.host || "").includes(needle)));
      // pending first, then offline, then by name
      const rank = (x) => (x.status === "pending" ? 0 : x.online ? 2 : 1);
      filtered.sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
      const pg = CVMS.paginate(filtered, ls.page, 50);
      ls.page = pg.page;
      const list = pg.items;
      CVMS.clear(box);
      if (!list.length) {
        box.append(byTab.length ? CVMS.emptyState("search", t("common.no_match")) : CVMS.emptyState("server", t("clients.empty"), t("clients.empty_text")));
        return;
      }
      const tbody = el("tbody");
      list.forEach((x) => {
        const isOpen = open.has(x.id);
        const row = el("tr", { class: "client-row" + (isOpen ? " open" : "") },
          el("td", { onclick: () => { if (open.has(x.id)) open.delete(x.id); else open.add(x.id); render(); } },
            el("div", { class: "row" }, el("span", { class: "expander" }, icon("right")), el("span", { class: "dot " + (x.online ? "ok" : "off") }),
              el("div", null, el("b", null, x.name), el("div", { class: "dim", style: { fontSize: "11px" } }, x.hostname + (x.ip ? " · " + x.ip : ""))))),
          el("td", null, statusBadge(x.status)),
          el("td", { class: "muted" }, (x.os || "—") + (x.version ? " · v" + x.version : "")),
          el("td", null, x.cameras.length),
          el("td", { class: "muted" }, x.online ? t("status.now") : fmt.ago(x.last_seen)),
          el("td", { class: "actions" },
            x.status !== "approved" ? btn({ label: t("clients.approve"), icon: "check", kind: "ok", size: "sm", onClick: () => act(x, "approve") }) : null,
            x.status !== "blocked" ? btn({ label: t("clients.block"), icon: "ban", size: "sm", onClick: () => act(x, "block") }) : null,
            btn({ icon: "edit", size: "sm", title: t("common.rename"), onClick: () => renameClient(x) }),
            btn({ icon: "trash", kind: "danger", size: "sm", title: t("common.delete"), onClick: () => act(x, "delete") })));
        tbody.append(row);
        if (isOpen) {
          tbody.append(el("tr", { class: "detail-row" }, el("td", { colspan: "6" }, el("div", { class: "cam-rows" },
            x.cameras.length ? x.cameras.map((cam) => el("div", { class: "cam-row" },
              el("span", { class: "dot " + (cam.online ? "ok" : "off") }),
              el("div", { class: "grow" }, el("b", null, cam.name), el("span", { class: "meta" }, [cam.display_name ? t("cam.orig") + ": " + cam.orig_name : null, cam.host, cam.model].filter(Boolean).join(" · "))),
              cam.location ? el("span", { class: "badge" }, icon("pin"), cam.location) : null,
              streamBadges(t("cam.main"), cam.main), streamBadges(t("cam.sub"), cam.sub),
              btn({ icon: "edit", size: "sm", title: t("common.edit"), onClick: () => editCamera(cam) }))) :
              el("div", { class: "cam-row muted" }, t("clients.no_cameras"))))));
        }
      });
      box.append(el("div", { class: "table-wrap" }, el("table", { class: "table" },
        el("thead", null, el("tr", null, el("th", null, t("col.name")), el("th", null, t("col.status")), el("th", null, t("col.system")), el("th", null, t("col.cameras")), el("th", null, t("col.last_seen")), el("th"))),
        tbody)));
      if (pg.pages > 1) box.append(el("div", { class: "card-body row", style: { justifyContent: "space-between" } }, el("span", { class: "muted" }, t("audit.total", { n: pg.total })), CVMS.pager(pg.page, pg.pages, (p) => { ls.page = p; render(); })));
      box.querySelectorAll(".cam-row .badge svg").forEach((s) => { s.style.width = "11px"; s.style.height = "11px"; });
    }

    function streamBadges(label, s) {
      if (!s || !s.available) return null;
      return el("span", { class: "row", style: { gap: "4px" } }, el("span", { class: "dim", style: { fontSize: "11px" } }, label), CVMS.codecBadge(s.codec), s.width ? el("span", { class: "badge" }, fmt.res(s.width, s.height)) : null);
    }

    async function act(x, action) {
      if (action === "delete" && !(await CVMS.confirm(t("common.delete"), t("clients.delete_confirm", { name: x.name })))) return;
      if (action === "block" && !(await CVMS.confirm(t("clients.block"), t("clients.block_confirm", { name: x.name }), "danger"))) return;
      try {
        await api("/api/clients/" + x.id + (action === "delete" ? "" : "/" + action), { method: action === "delete" ? "DELETE" : "POST" });
        CVMS.toast(t("clients.done_" + action), "ok");
        await refreshData();
      } catch (e) { CVMS.err(e); }
    }

    function renameClient(x) {
      const name = el("input", { class: "input", value: x.name, maxlength: "100" });
      CVMS.modal({
        title: t("common.rename"), body: CVMS.field(t("col.name"), name),
        actions: [{ label: t("common.cancel") }, { label: t("common.save"), kind: "primary", onClick: async () => { await api("/api/clients/" + x.id, { method: "PUT", body: { name: name.value } }); await refreshData(); } }],
      });
    }

    S.refresh = render;
    S.onStatus = render;
    render();
  }

  function statusBadge(status) {
    const cls = { approved: "badge-ok", pending: "badge-warn", blocked: "badge-danger" }[status] || "";
    return el("span", { class: "badge badge-dot " + cls }, t("clients.status_" + status));
  }

  function editCamera(cam) {
    const name = el("input", { class: "input", value: cam.display_name || "", placeholder: cam.orig_name, maxlength: "100" });
    const locs = [...new Set(S.cams.map((k) => k.location).filter(Boolean))];
    const loc = el("input", { class: "input", value: cam.location || "", list: "cvms-locs", maxlength: "100" });
    CVMS.modal({
      title: t("cam.edit"),
      body: el("div", null,
        CVMS.field(t("cam.display_name"), name, t("cam.display_hint", { name: cam.orig_name })),
        CVMS.field(t("cam.location"), loc, t("cam.location_hint")),
        el("datalist", { id: "cvms-locs" }, locs.map((l) => el("option", { value: l })))),
      actions: [{ label: t("common.cancel") }, {
        label: t("common.save"), kind: "primary", onClick: async () => {
          await api("/api/cameras/" + cam.id, { method: "PUT", body: { display_name: name.value, location: loc.value } });
          await refreshData();
        },
      }],
    });
  }

  /* ================================================================ admin: users */
  function usersPage() {
    const c = page(t("nav.users"), t("users.sub"));
    CVMS.shell.setActions(btn({ label: t("users.new"), icon: "plus", kind: "primary", onClick: () => editUser(null) }));
    const box = el("div", { class: "card" });
    c.append(box);
    let users = [];
    let idleDefault = 0;
    // viewer_idle_minutes: null = server default, 0 = never, n = minutes
    const idleText = (v) => v === 0 ? t("users.idle_never") : t("time.minutes", { n: v });
    const idleCell = (v) => v == null ? el("span", { class: "dim" }, t("users.idle_default", { v: idleDefault ? idleText(idleDefault) : t("users.idle_never") })) : el("span", { class: "badge" }, idleText(v));

    async function load() {
      try {
        const [list, info] = await Promise.all([api("/api/users"), api("/api/info").catch(() => ({}))]);
        users = list;
        idleDefault = info.viewer_idle_default || 0;
        render();
      } catch (e) { CVMS.err(e); }
    }
    function render() {
      CVMS.clear(box);
      const approved = S.clients.filter((x) => x.status === "approved");
      const name = (id) => { const x = S.clients.find((k) => k.id === id); return x ? x.name : "#" + id; };
      box.append(el("div", { class: "table-wrap" }, el("table", { class: "table" },
        el("thead", null, el("tr", null, el("th", null, t("auth.username")), el("th", null, t("users.role")), el("th", null, t("col.status")), el("th", null, t("users.clients")), el("th", null, t("users.idle")), el("th", null, t("users.last_login")), el("th"))),
        el("tbody", null, users.map((u) => el("tr", null,
          el("td", null, el("div", { class: "row" }, el("span", { class: "avatar" }, u.username.slice(0, 2)), el("b", null, u.username), u.id === S.me.id ? el("span", { class: "badge" }, t("users.you")) : null)),
          el("td", null, el("span", { class: "badge " + (u.role === "admin" ? "badge-info" : "") }, t("role." + u.role))),
          el("td", null, u.disabled ? el("span", { class: "badge badge-danger badge-dot" }, t("users.disabled")) : el("span", { class: "badge badge-ok badge-dot" }, t("users.active")),
            ),
          el("td", null, u.role === "admin" ? el("span", { class: "muted" }, t("users.all_clients")) :
            u.clients.length ? el("div", { class: "chip-list" }, u.clients.slice(0, 4).map((id) => el("span", { class: "badge" }, name(id))), u.clients.length > 4 ? el("span", { class: "badge" }, "+" + (u.clients.length - 4)) : null) :
              el("span", { class: "dim" }, t("users.no_clients"))),
          el("td", { style: { whiteSpace: "nowrap" } }, idleCell(u.viewer_idle_minutes)),
          el("td", { class: "muted" }, u.last_login ? fmt.ago(u.last_login) : "—"),
          el("td", { class: "actions" },
            btn({ icon: "edit", size: "sm", title: t("common.edit"), onClick: () => editUser(u) }),
            u.id !== S.me.id ? btn({ icon: "trash", kind: "danger", size: "sm", title: t("common.delete"), onClick: () => delUser(u) }) : null)))))));
      void approved;
    }

    function editUser(u) {
      const isNew = !u;
      const uname = el("input", { class: "input", value: u ? u.username : "", disabled: !isNew, autocomplete: "off", maxlength: "32" });
      const pw = el("input", { class: "input", type: "password", autocomplete: "new-password", placeholder: isNew ? "" : t("users.pw_keep") });
      const role = el("select", { class: "select" }, ["user", "admin"].map((r) => el("option", { value: r, selected: (u ? u.role : "user") === r }, t("role." + r))));
      const disabled = el("input", { type: "checkbox", checked: u ? u.disabled : false });
      const idleCur = u ? u.viewer_idle_minutes : null;
      const idlePresets = [5, 10, 15, 30, 60, 120];
      const idleInitial = idleCur == null ? "default" : idleCur === 0 ? "never" : idlePresets.includes(idleCur) ? String(idleCur) : "custom";
      const idleSel = el("select", { class: "select" },
        el("option", { value: "default", selected: idleInitial === "default" }, t("users.idle_default", { v: idleDefault ? idleText(idleDefault) : t("users.idle_never") })),
        el("option", { value: "never", selected: idleInitial === "never" }, t("users.idle_never")),
        idlePresets.map((n) => el("option", { value: String(n), selected: idleInitial === String(n) }, t("time.minutes", { n }))),
        el("option", { value: "custom", selected: idleInitial === "custom" }, t("users.idle_custom")));
      const idleNum = el("input", { class: "input", type: "number", min: "1", max: "1440", step: "1", value: idleInitial === "custom" ? String(idleCur) : "", placeholder: t("users.idle_minutes_ph") });
      const syncIdle = () => { idleNum.style.display = idleSel.value === "custom" ? "" : "none"; };
      idleSel.onchange = () => { syncIdle(); if (idleSel.value === "custom") idleNum.focus(); };
      syncIdle();
      const idleValue = () => {
        const v = idleSel.value;
        if (v === "default") return null;
        if (v === "never") return 0;
        const n = Number(v === "custom" ? idleNum.value : v);
        if (!Number.isInteger(n) || n < 1 || n > 1440) throw new Error(t("users.idle_invalid"));
        return n;
      };
      const chosen = new Set(u ? u.clients : []);
      const approved = S.clients.filter((x) => x.status === "approved");
      const counter = el("span", { class: "muted", style: { fontSize: "12px" } });
      const count = () => { counter.textContent = t("users.selected", { n: chosen.size, total: approved.length }); };
      const rows = approved.map((x) => {
        const cb = el("input", { type: "checkbox", checked: chosen.has(x.id) });
        cb.onchange = () => { if (cb.checked) chosen.add(x.id); else chosen.delete(x.id); count(); };
        const row = el("label", { class: "check" }, cb, el("span", { class: "dot " + (x.online ? "ok" : "off") }), x.name, el("span", { class: "dim" }, " · " + t("layout.cam_count", { n: x.cameras.length })));
        return { x, cb, row };
      });
      const list = el("div", { class: "list-check" }, approved.length ? rows.map((r) => r.row) : el("span", { class: "dim" }, t("users.no_approved")));
      const visibleRows = () => rows.filter((r) => !r.row.classList.contains("hidden-row"));
      const setAll = (on) => { visibleRows().forEach((r) => { r.cb.checked = on; if (on) chosen.add(r.x.id); else chosen.delete(r.x.id); }); count(); };
      const tools = approved.length > 1 ? el("div", { class: "list-tools" },
        approved.length > 8 ? CVMS.searchBox("", (v) => { const q = v.toLowerCase(); rows.forEach((r) => r.row.classList.toggle("hidden-row", !!q && !r.x.name.toLowerCase().includes(q))); }, { width: "220px", delay: 0 }) : null,
        btn({ label: t("common.select_all"), size: "sm", onClick: () => setAll(true) }),
        btn({ label: t("common.select_none"), size: "sm", onClick: () => setAll(false) }),
        el("span", { style: { flex: 1 } }), counter) : null;
      count();
      const permBox = el("div", null, el("div", { class: "field" }, el("label", null, t("users.allowed_clients")), tools, list));
      const syncRole = () => { permBox.style.display = role.value === "admin" ? "none" : ""; };
      role.onchange = syncRole;
      syncRole();
      CVMS.modal({
        title: isNew ? t("users.new") : t("users.edit", { name: u.username }),
        body: el("div", null,
          el("div", { class: "form-row" }, CVMS.field(t("auth.username"), uname), CVMS.field(t("users.role"), role)),
          CVMS.field(isNew ? t("auth.password") : t("users.new_password"), pw, t("auth.password_hint")),
          CVMS.field(t("users.idle"), el("div", { class: "row" }, idleSel, idleNum), t("users.idle_hint")),
          !isNew ? el("div", { class: "field" }, el("label", { class: "check" }, disabled, t("users.disable"))) : null,
          permBox),
        actions: [{ label: t("common.cancel") }, {
          label: isNew ? t("common.create") : t("common.save"), kind: "primary", onClick: async () => {
            const body = { username: uname.value.trim(), password: pw.value, role: role.value, disabled: disabled.checked, clients: role.value === "admin" ? [] : [...chosen], viewer_idle_minutes: idleValue() };
            if (isNew) await api("/api/users", { method: "POST", body });
            else await api("/api/users/" + u.id, { method: "PUT", body });
            CVMS.toast(t("users.saved"), "ok");
            load();
          },
        }],
      });
    }

    async function delUser(u) {
      if (!(await CVMS.confirm(t("common.delete"), t("users.delete_confirm", { name: u.username })))) return;
      try { await api("/api/users/" + u.id, { method: "DELETE" }); load(); } catch (e) { CVMS.err(e); }
    }

    S.refresh = render;
    load();
  }

  /* ================================================================ admin: audit */
  function auditPage() {
    const c = page(t("nav.audit"), t("audit.sub"));
    const st = { q: "", action: "", user: "", from: "", to: "", page: 1, per: 50 };
    const filters = el("div", { class: "toolbar" });
    const box = el("div", { class: "card" });
    c.append(filters, box);
    let actions = [];
    let timer;

    function renderFilters() {
      const q = el("div", { class: "input-search" }, icon("search"), el("input", { class: "input", placeholder: t("common.search"), value: st.q }));
      q.querySelector("input").oninput = (e) => { st.q = e.target.value; st.page = 1; clearTimeout(timer); timer = setTimeout(load, 300); };
      const act = el("select", { class: "select", style: { width: "auto" } }, el("option", { value: "" }, t("audit.all_actions")), actions.map((a) => el("option", { value: a, selected: a === st.action }, actionLabel(a))));
      act.onchange = () => { st.action = act.value; st.page = 1; load(); };
      const user = el("input", { class: "input", placeholder: t("auth.username"), value: st.user, style: { width: "150px" } });
      user.onchange = () => { st.user = user.value.trim(); st.page = 1; load(); };
      const from = el("input", { class: "input", type: "date", value: st.from, style: { width: "auto" }, title: t("audit.from") });
      const to = el("input", { class: "input", type: "date", value: st.to, style: { width: "auto" }, title: t("audit.to") });
      from.onchange = () => { st.from = from.value; st.page = 1; load(); };
      to.onchange = () => { st.to = to.value; st.page = 1; load(); };
      CVMS.clear(filters).append(q, act, user, from, to, el("div", { class: "spacer" }), btn({ icon: "refresh", title: t("common.refresh"), onClick: load }));
    }

    function actionLabel(a) { const k = "audit." + a; const v = t(k); return v === k ? a : v; }

    async function load() {
      const p = new URLSearchParams({ limit: st.per, offset: (st.page - 1) * st.per });
      if (st.q) p.set("q", st.q);
      if (st.action) p.set("action", st.action);
      if (st.user) p.set("user", st.user);
      if (st.from) p.set("from", new Date(st.from + "T00:00:00").getTime());
      if (st.to) p.set("to", new Date(st.to + "T23:59:59").getTime());
      let res;
      try { res = await api("/api/audit?" + p); } catch (e) { return CVMS.err(e); }
      const firstLoad = !actions.length && res.actions.length;
      actions = res.actions;
      if (firstLoad) renderFilters();
      const pages = Math.max(1, Math.ceil(res.total / st.per));
      CVMS.clear(box);
      if (!res.rows.length) { box.append(CVMS.emptyState("list", t("audit.empty"))); return; }
      box.append(
        el("div", { class: "table-wrap" }, el("table", { class: "table" },
          el("thead", null, el("tr", null, el("th", null, t("audit.time")), el("th", null, t("auth.username")), el("th", null, "IP"), el("th", null, t("audit.action")), el("th", null, t("audit.target")), el("th", null, t("audit.details")))),
          el("tbody", null, res.rows.map((r) => el("tr", null,
            el("td", { class: "muted mono", style: { whiteSpace: "nowrap", fontSize: "12px" } }, fmt.date(r.ts)),
            el("td", null, r.username || el("span", { class: "dim" }, "—")),
            el("td", { class: "mono muted", style: { fontSize: "12px" } }, r.ip || "—"),
            el("td", null, el("span", { class: "badge " + actionClass(r.action) }, actionLabel(r.action))),
            el("td", null, r.target),
            el("td", { class: "muted" }, r.details)))))),
        el("div", { class: "card-body row", style: { justifyContent: "space-between" } }, el("span", { class: "muted" }, t("audit.total", { n: res.total })), CVMS.pager(st.page, pages, (pg) => { st.page = pg; load(); })));
    }

    function actionClass(a) {
      if (/failed|blocked|unauthorized|delete/.test(a)) return "badge-danger";
      if (/login|logout/.test(a)) return "badge-accent";
      if (/view/.test(a)) return "";
      return "badge-info";
    }

    renderFilters();
    load();
  }

  /* ================================================================ settings */
  async function settingsPage() {
    const c = page(t("nav.settings"));
    const lang = CVMS.shell.langSelect();
    lang.classList.remove("lang-select");
    lang.addEventListener("change", () => { api("/api/me/lang", { method: "PUT", body: { lang: lang.value } }).catch(() => {}); setTimeout(settingsPage, 50); });
    c.append(
      el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("settings.profile")), el("p", { class: "card-sub" }, t("settings.profile_sub")))),
        el("div", { class: "card-body" },
          el("dl", { class: "kv" }, el("dt", null, t("auth.username")), el("dd", null, S.me.username), el("dt", null, t("users.role")), el("dd", null, t("role." + S.me.role))),
          el("div", { style: { maxWidth: "320px", marginTop: "16px" } }, CVMS.field(t("common.language"), lang)),
          )));
    if (S.me.role === "admin") {
      let info = {};
      try { info = await api("/api/info"); } catch (e) { /* ignore */ }
      c.append(el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("settings.server")), el("p", { class: "card-sub" }, t("settings.server_sub")))),
        el("div", { class: "card-body" }, el("dl", { class: "kv" },
          el("dt", null, t("settings.version")), el("dd", { class: "mono" }, info.version || S.me.version),
          el("dt", null, t("settings.pipeline")), el("dd", null, info.pipeline ? el("span", { class: "badge " + (info.pipeline.hwaccel ? "badge-ok" : "badge-warn") }, info.pipeline.name) : "—"),
          el("dt", null, t("settings.h265_fallback")), el("dd", null, t("settings.fallback_" + (info.h265_fallback || "server"))),
          el("dt", null, t("settings.live")), el("dd", null, info.live ? t("settings.live_value", { s: info.live.streams, v: info.live.viewers, t: info.live.transcodes, c: info.live.clients_connected }) : "—"),
          el("dt", null, t("settings.client_url")), el("dd", { class: "mono" }, location.protocol + "//" + location.host),
          el("dt", null, t("settings.idle_default")), el("dd", null, info.viewer_idle_default ? t("time.minutes", { n: info.viewer_idle_default }) : t("users.idle_never"))),
          el("p", { class: "muted", style: { margin: "14px 0 0", fontSize: "12px" } }, t("settings.config_hint")))));
      c.append(CVMS.autostartCard());
    }
  }

  main();
})();
