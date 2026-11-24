/* CVMS client web app. */
(function () {
  "use strict";
  const { el, t, api, btn, icon, fmt } = CVMS;
  const S = { me: null, status: null, cams: [], cleanup: null, refresh: null, poll: null };

  /* ================================================================ boot */
  async function main() {
    await CVMS.i18n.load(CVMS.i18n.initial());
    CVMS.onUnauthorized = () => { if (S.me) { S.me = null; stopPolling(); showLogin(); } };
    try { S.me = await api("/api/me", { noAuthRedirect: true }); } catch (e) { return showLogin(); }
    afterLogin();
  }

  function showLogin() {
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    CVMS.loginScreen({
      brandSub: "Client",
      title: t("auth.lead_client"),
      onSubmit: async (u, p) => {
        S.me = await api("/api/login", { method: "POST", body: { username: u, password: p }, noAuthRedirect: true });
        afterLogin();
      },
    });
    api("/api/ping", { noAuthRedirect: true }).then((r) => { const v = document.querySelector(".auth-foot .mono"); if (v) v.textContent = "v" + r.version; }).catch(() => {});
  }

  async function afterLogin() {
    if (S.me.must_change) {
      return CVMS.passwordForm({
        brandSub: "Client", forced: true,
        onSubmit: async (o, n) => { S.me = await api("/api/me/password", { method: "POST", body: { old: o, new: n } }); CVMS.toast(t("auth.password_changed"), "ok"); afterLogin(); },
      });
    }
    CVMS.i18n.listeners.length = 0;
    CVMS.shell.mount({
      brandSub: "Client",
      version: S.me.version,
      user: S.me,
      nav: buildNav,
      onLogout: logout,
      onChangePassword: changePassword,
      onLang: () => CVMS.router.resolve(),
    });
    await Promise.all([loadStatus(), loadCameras()]);
    startPolling();
    registerRoutes();
    CVMS.router.start();
  }

  async function logout() {
    try { await api("/api/logout", { method: "POST" }); } catch (e) { /* ignore */ }
    S.me = null;
    stopPolling();
    showLogin();
  }

  function changePassword() {
    CVMS.passwordForm({ onSubmit: (o, n) => api("/api/me/password", { method: "POST", body: { old: o, new: n } }) });
  }

  /* ================================================================ data */
  async function loadStatus() {
    try {
      S.status = await api("/api/status");
      updateConn();
    } catch (e) { if (e.status !== 401) CVMS.shell.setConn("off", t("conn.offline")); }
  }

  async function loadCameras() {
    try { S.cams = await api("/api/cameras"); CVMS.shell.renderNav(); } catch (e) { if (e.status !== 401) CVMS.err(e); }
  }

  function startPolling() {
    stopPolling();
    let n = 0;
    S.poll = setInterval(async () => {
      if (document.hidden) return;
      await loadStatus();
      if (++n % 3 === 0) await loadCameras();
      if (S.refresh) S.refresh();
    }, 3000);
  }
  function stopPolling() { clearInterval(S.poll); S.poll = null; }

  function serverState() {
    const s = S.status && S.status.server;
    if (!s || s.state === "disabled") return { cls: "warn", dot: "warn", key: "srv.not_configured", icon: "link" };
    if (s.state !== "connected") return { cls: "danger", dot: "off", key: s.state === "connecting" ? "srv.connecting" : "srv.disconnected", icon: "wifioff" };
    switch (s.approval) {
      case "approved": return { cls: "ok", dot: "ok", key: "srv.approved", icon: "check" };
      case "blocked": return { cls: "danger", dot: "off", key: "srv.blocked", icon: "ban" };
      case "unauthorized": return { cls: "danger", dot: "off", key: "srv.unauthorized", icon: "alert" };
      default: return { cls: "warn", dot: "warn", key: "srv.pending", icon: "clock" };
    }
  }

  function updateConn() {
    const st = serverState();
    CVMS.shell.setConn(st.dot, t(st.key));
    CVMS.shell.renderNav();
  }

  /* ================================================================ nav */
  function buildNav() {
    const online = S.cams.filter((c) => c.online).length;
    const st = serverState();
    return [
      {
        items: [
          { href: "#/dashboard", icon: "home", label: t("nav.dashboard") },
          { href: "#/grid", icon: "grid", label: t("nav.cameras_grid"), count: S.cams.length ? online + "/" + S.cams.length : null },
          { href: "#/cameras", icon: "camera", label: t("nav.cameras") },
          { href: "#/add", icon: "plus", label: t("nav.add_camera") },
        ],
      },
      {
        title: t("nav.system"),
        items: [
          { href: "#/server", icon: "server", label: t("nav.server"), count: st.dot === "ok" ? null : "!", countClass: "warn" },
          { href: "#/settings", icon: "settings", label: t("nav.settings") },
        ],
      },
    ];
  }

  function page(title, sub, flush) {
    if (S.cleanup) { S.cleanup(); S.cleanup = null; }
    S.refresh = null;
    CVMS.shell.setTitle(title, sub);
    CVMS.shell.setActions();
    return CVMS.shell.content(flush);
  }

  function registerRoutes() {
    const R = CVMS.router;
    R.add("/", () => R.go("/dashboard"));
    R.add("/dashboard", dashboardPage);
    R.add("/grid", gridPage);
    R.add("/cameras", camerasPage);
    R.add("/add", addPage);
    R.add("/server", serverPage);
    R.add("/settings", settingsPage);
    R.fallback = () => R.go("/dashboard");
  }

  function notice(kind, ic, text, action) {
    return el("div", { class: "notice " + (kind || "") }, icon(ic), el("div", { class: "grow" }, text), action || null);
  }

  /* ================================================================ dashboard */
  function dashboardPage() {
    const c = page(t("nav.dashboard"));
    const box = el("div");
    c.append(box);
    const stat = (ic, cls, value, label) => el("div", { class: "stat" }, el("div", { class: "stat-icon " + cls }, icon(ic)), el("div", null, el("div", { class: "stat-value" }, value), el("div", { class: "stat-label" }, label)));
    function render() {
      const s = S.status || {};
      const st = serverState();
      const online = S.cams.filter((k) => k.online).length;
      CVMS.clear(box);
      if (s.ffmpeg === false) box.append(notice("danger", "alert", t("dash.no_ffmpeg", { path: s.ffmpeg_path || "ffmpeg" })));
      if (st.key === "srv.not_configured") box.append(notice("info", "info", t("dash.hint_server"), btn({ label: t("nav.server"), size: "sm", onClick: () => CVMS.router.go("/server") })));
      else if (st.key === "srv.pending") box.append(notice("", "clock", t("dash.hint_pending")));
      if (!S.cams.length) box.append(notice("info", "camera", t("dash.hint_cameras"), btn({ label: t("nav.add_camera"), size: "sm", onClick: () => CVMS.router.go("/add") })));
      box.append(el("div", { class: "stats" },
        stat("camera", "ok", online + " / " + S.cams.length, t("dash.cameras_online")),
        stat(st.icon, st.cls, t(st.key), t("dash.server_status")),
        stat("activity", "", String((s.server && s.server.streams) || 0) + ((s.server && s.server.transcodes) ? " · " + t("dash.transcodes", { n: s.server.transcodes }) : ""), t("dash.streams")),
        stat("monitor", s.pipeline && s.pipeline.hwaccel ? "ok" : "warn", s.pipeline ? (s.pipeline.hwaccel ? "GPU" : "CPU") : "—", s.pipeline ? s.pipeline.name : t("dash.pipeline"))));
      box.append(el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("dash.about")), el("p", { class: "card-sub" }, t("dash.about_sub")))),
        el("div", { class: "card-body" }, el("dl", { class: "kv" },
          el("dt", null, t("settings.client_name")), el("dd", null, s.name || "—"),
          el("dt", null, t("settings.version")), el("dd", { class: "mono" }, s.version || ""),
          el("dt", null, t("srv.url")), el("dd", { class: "mono" }, (s.server && s.server.server_url) || "—"),
          el("dt", null, "FFmpeg"), el("dd", { class: "mono" }, s.ffmpeg_path || "—"),
          el("dt", null, t("settings.config_file")), el("dd", { class: "mono" }, s.config_path || "—")))));
    }
    S.refresh = render;
    render();
  }

  /* ================================================================ snapshot grid */
  function snapURL(id, profile, maxAge) {
    return "/api/cameras/" + encodeURIComponent(id) + "/snapshot?profile=" + profile + (maxAge ? "&max_age=" + maxAge : "") + "&_=" + Date.now();
  }

  function gridPage() {
    const c = page(t("nav.cameras_grid"), t("grid.client_sub"), true);
    let cols = CVMS.store.get("cvms.client.cols") || "auto";
    const toolbar = el("div", { class: "toolbar", style: { padding: "4px 4px 0" } });
    const area = el("div", { style: { padding: "4px" } });
    c.append(toolbar, area);
    const loops = new Map();
    let alive = true;
    let paused = document.hidden;
    let viewer = null;
    // only tiles on screen refresh their snapshot
    const visible = new Set();
    const io = "IntersectionObserver" in window ? new IntersectionObserver((entries) => {
      entries.forEach((e) => { if (e.isIntersecting) visible.add(e.target); else visible.delete(e.target); });
    }, { rootMargin: "100px" }) : null;

    function renderToolbar() {
      CVMS.clear(toolbar);
      toolbar.append(el("div", { class: "btn-group" }, ["auto", "2", "3", "4"].map((n) => {
        const b = btn({ label: n === "auto" ? t("grid.auto") : n, size: "sm", onClick: () => { cols = n; CVMS.store.set("cvms.client.cols", n); render(); } });
        if (n === cols) b.classList.add("active");
        return b;
      })));
      toolbar.append(el("span", { class: "muted", style: { fontSize: "12px" } }, t("grid.refresh_every", { n: (S.status && S.status.snapshot_interval) || 5 })));
      toolbar.append(el("div", { class: "spacer" }));
      toolbar.append(btn({ label: t("nav.add_camera"), icon: "plus", size: "sm", onClick: () => CVMS.router.go("/add") }));
    }

    function stopLoops() { loops.forEach((l) => clearTimeout(l.timer)); loops.clear(); }

    function loop(cam, img, tile) {
      const l = { timer: null };
      loops.set(cam.id, l);
      const interval = () => ((S.status && S.status.snapshot_interval) || 5) * 1000;
      const next = () => {
        if (!alive || loops.get(cam.id) !== l) return;
        if (paused || viewer || (io && !visible.has(tile))) { l.timer = setTimeout(next, 1000); return; }
        tile.classList.add("busy");
        const im = new Image();
        im.onload = () => { tile.classList.remove("busy", "offline"); img.src = im.src; img.style.visibility = "visible"; setStateText(tile, null); l.timer = setTimeout(next, interval()); };
        im.onerror = () => { tile.classList.remove("busy"); tile.classList.add("offline"); setStateText(tile, t("grid.no_image")); l.timer = setTimeout(next, interval() * 2); };
        im.src = snapURL(cam.id, "sub");
      };
      next();
    }

    function setStateText(tile, text) {
      let s = tile.querySelector(".tile-state");
      if (!text) { if (s) s.remove(); return; }
      if (!s) { s = el("div", { class: "tile-state" }); tile.appendChild(s); }
      s.textContent = text;
    }

    function render() {
      renderToolbar();
      stopLoops();
      if (io) io.disconnect();
      visible.clear();
      CVMS.clear(area);
      const cams = S.cams.filter((k) => k.enabled);
      if (!cams.length) {
        area.append(el("div", { class: "card" }, CVMS.emptyState("camera", t("cams.empty"), t("cams.empty_text"), btn({ label: t("nav.add_camera"), icon: "plus", kind: "primary", onClick: () => CVMS.router.go("/add") }))));
        return;
      }
      const grid = el("div", { class: "cam-grid " + (cols === "auto" ? "auto" : "g" + cols) });
      cams.forEach((cam) => {
        const img = el("img", { alt: "" });
        img.style.visibility = "hidden";
        const badges = el("div", { class: "tile-badges" });
        const codec = (cam.sub && cam.sub.url ? cam.sub.codec : cam.main.codec);
        if (codec) badges.append(el("span", { class: "badge badge-codec" }, codec.toUpperCase()));
        const tile = el("div", { class: "tile", title: cam.name + "\n" + t("grid.click_enlarge"), onclick: () => openViewer(cam) },
          img, el("span", { class: "refresh-dot" }),
          el("div", { class: "tile-state" }, el("div", null, el("div", { class: "spinner" }))),
          el("div", { class: "tile-bar" }, el("span", { class: "dot " + (cam.online ? "ok" : "off") }), el("span", { class: "tile-name" }, cam.name), badges));
        grid.append(tile);
        if (io) io.observe(tile);
        loop(cam, img, tile);
      });
      area.append(grid);
    }

    function openViewer(cam) {
      const img = el("img", { alt: "" });
      const state = el("div", { class: "tile-state" }, el("div", null, el("div", { class: "spinner" }), el("div", null, t("grid.loading"))));
      const info = el("div", { class: "viewer-foot" });
      let open = true;
      let timer;
      const close = () => { open = false; clearTimeout(timer); v.remove(); document.removeEventListener("keydown", onKey); viewer = null; };
      const onKey = (e) => { if (e.key === "Escape") close(); };
      const v = el("div", { class: "viewer" },
        el("div", { class: "viewer-head" }, el("span", { class: "dot " + (cam.online ? "ok" : "off") }), el("div", { class: "viewer-title" }, cam.name),
          cam.main.width ? el("span", { class: "badge" }, fmt.res(cam.main.width, cam.main.height)) : null,
          CVMS.codecBadge(cam.main.codec),
          btn({ icon: "x", kind: "ghost", onClick: close })),
        el("div", { class: "viewer-body", onclick: (e) => { if (e.target === e.currentTarget) close(); } }, img, state),
        info);
      document.body.appendChild(v);
      document.addEventListener("keydown", onKey);
      viewer = { close };
      const next = () => {
        if (!open) return;
        if (document.hidden) { timer = setTimeout(next, 1000); return; }
        const started = Date.now();
        const im = new Image();
        im.onload = () => {
          if (!open) return;
          img.src = im.src; state.style.display = "none";
          info.textContent = t("grid.updated", { time: new Date().toLocaleTimeString() }) + " · " + ((Date.now() - started) / 1000).toFixed(1) + "s";
          timer = setTimeout(next, 300);
        };
        im.onerror = () => { if (!open) return; state.style.display = "grid"; state.textContent = t("grid.no_image"); timer = setTimeout(next, 3000); };
        im.src = snapURL(cam.id, "main", 1);
      };
      next();
    }

    const onVis = () => { paused = document.hidden; };
    document.addEventListener("visibilitychange", onVis);
    let sig = S.cams.map((k) => k.id + k.enabled + k.online).join();
    S.refresh = () => {
      const n = S.cams.map((k) => k.id + k.enabled + k.online).join();
      if (n !== sig) { sig = n; render(); }
    };
    S.cleanup = () => { alive = false; stopLoops(); if (io) io.disconnect(); document.removeEventListener("visibilitychange", onVis); if (viewer) viewer.close(); };
    render();
  }

  /* ================================================================ cameras list */
  function camerasPage() {
    const c = page(t("nav.cameras"), t("cams.sub"));
    CVMS.shell.setActions(btn({ label: t("nav.add_camera"), icon: "plus", kind: "primary", onClick: () => CVMS.router.go("/add") }));
    const box = el("div", { class: "card" });
    c.append(box);
    function streamCell(s) {
      if (!s || !s.url) return el("span", { class: "dim" }, "—");
      return el("div", { class: "row", style: { gap: "4px", flexWrap: "wrap" } }, CVMS.codecBadge(s.codec) || el("span", { class: "badge" }, "?"), s.width ? el("span", { class: "badge" }, fmt.res(s.width, s.height)) : null);
    }
    function render() {
      CVMS.clear(box);
      if (!S.cams.length) {
        box.append(CVMS.emptyState("camera", t("cams.empty"), t("cams.empty_text"), btn({ label: t("nav.add_camera"), icon: "plus", kind: "primary", onClick: () => CVMS.router.go("/add") })));
        return;
      }
      box.append(el("div", { class: "table-wrap" }, el("table", { class: "table" },
        el("thead", null, el("tr", null, el("th"), el("th", null, t("col.name")), el("th", null, t("cams.host")), el("th", null, t("cam.main")), el("th", null, t("cam.sub")), el("th", null, t("col.status")), el("th", null, t("cams.enabled")), el("th"))),
        el("tbody", null, S.cams.map((cam) => {
          const thumb = el("img", { class: "cam-thumb", alt: "", loading: "lazy", src: cam.enabled ? snapURL(cam.id, "sub", 60) : "" });
          thumb.onerror = () => { thumb.style.visibility = "hidden"; };
          const toggle = el("input", { type: "checkbox", checked: cam.enabled });
          toggle.onchange = async () => {
            try { await api("/api/cameras/" + cam.id, { method: "PUT", body: camBody(cam, { enabled: toggle.checked }) }); await loadCameras(); render(); }
            catch (e) { CVMS.err(e); toggle.checked = !toggle.checked; }
          };
          return el("tr", null,
            el("td", { style: { width: "110px" } }, thumb),
            el("td", null, el("b", null, cam.name), el("div", { class: "dim", style: { fontSize: "11px" } }, [cam.manufacturer, cam.model].filter(Boolean).join(" "))),
            el("td", { class: "mono muted", style: { fontSize: "12px" } }, cam.host),
            el("td", null, streamCell(cam.main)),
            el("td", null, streamCell(cam.sub)),
            el("td", null, cam.enabled ? CVMS.statusBadge(cam.online) : el("span", { class: "badge" }, t("cams.disabled"))),
            el("td", null, el("label", { class: "check" }, toggle)),
            el("td", { class: "actions" },
              btn({ icon: "edit", size: "sm", title: t("common.edit"), onClick: () => cameraEditor(cam) }),
              btn({ icon: "trash", kind: "danger", size: "sm", title: t("common.delete"), onClick: () => delCam(cam) })));
        })))));
    }
    async function delCam(cam) {
      if (!(await CVMS.confirm(t("common.delete"), t("cams.delete_confirm", { name: cam.name })))) return;
      try { await api("/api/cameras/" + cam.id, { method: "DELETE" }); await loadCameras(); render(); CVMS.toast(t("cams.deleted"), "ok"); } catch (e) { CVMS.err(e); }
    }
    let sig = JSON.stringify(S.cams.map((k) => [k.id, k.online, k.enabled]));
    S.refresh = () => { const n = JSON.stringify(S.cams.map((k) => [k.id, k.online, k.enabled])); if (n !== sig) { sig = n; render(); } };
    S.afterSave = render;
    render();
  }

  function camBody(cam, over) {
    return Object.assign({ name: cam.name, host: cam.host, onvif_port: cam.onvif_port, username: cam.username, password: null, manufacturer: cam.manufacturer, model: cam.model, main_url: cam.main.url, sub_url: cam.sub.url, enabled: cam.enabled }, over || {});
  }

  /* ================================================================ add / edit camera */
  /* Add page: one table of found devices (ONVIF discovery + IP range scan),
   * shared login/password, IP filter and bulk add via checkboxes. */
  const A = (S.add = S.add || { rows: new Map(), user: "admin", pass: "", q: "", range: "", port: 80, discovered: false });
  function ipKey(ip) { return ip.split(".").map((x) => x.padStart(3, "0")).join("."); }
  // rows are keyed by ip:port — one IP may host several devices (NVR, encoder)
  function rowKey(ip, port) { return ip + ":" + (port || 80); }
  function addRow(d) {
    const key = rowKey(d.ip, d.port);
    const cur = A.rows.get(key) || { ip: d.ip, port: 80, name: "", onvif: false, rtsp: false, added: false, status: "new", error: "", selected: false };
    Object.keys(d).forEach((k) => { if (d[k] !== undefined && d[k] !== "" && d[k] !== null) cur[k] = d[k]; });
    if (cur.added && cur.status === "new") cur.status = "added";
    A.rows.set(key, cur);
  }

  function addPage() {
    const c = page(t("nav.add_camera"), t("add.sub"));
    CVMS.shell.setActions(btn({ label: t("add.by_rtsp"), icon: "link", onClick: () => cameraEditor(null, { mode: "rtsp" }) }));
    const user = el("input", { class: "input", value: A.user, autocomplete: "off", placeholder: t("auth.username") });
    const pass = el("input", { class: "input", type: "password", value: A.pass, autocomplete: "new-password", placeholder: t("auth.password") });
    user.oninput = () => { A.user = user.value; };
    pass.oninput = () => { A.pass = pass.value; };
    const q = el("input", { class: "input", value: A.q, placeholder: t("add.filter_ph") });
    const range = el("input", { class: "input mono", value: A.range, placeholder: "192.168.1.10-50", title: t("add.range_hint"), style: { width: "210px" } });
    const port = el("input", { class: "input", type: "number", value: A.port, min: "1", max: "65535", style: { width: "84px" }, title: t("add.onvif_port") });
    range.oninput = () => { A.range = range.value; };
    port.oninput = () => { A.port = +port.value || 80; };
    q.oninput = () => { A.q = q.value.trim(); renderTable(); };
    const discoverBtn = btn({ label: t("add.scan"), icon: "radar", onClick: discover });
    const scanBtn = btn({ label: t("add.scan_range"), icon: "search", onClick: scanRange });
    range.addEventListener("keydown", (e) => { if (e.key === "Enter") scanRange(); });
    const statusLine = el("div", { class: "muted", style: { fontSize: "12px", minHeight: "18px" } });
    const tableBox = el("div");
    const addBtn = btn({ label: t("add.add_selected", { n: 0 }), icon: "plus", kind: "primary", onClick: bulkAdd });
    const selInfo = el("span", { class: "muted", style: { fontSize: "12px" } });

    c.append(
      el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("add.creds")), el("p", { class: "card-sub" }, t("add.creds_sub")))),
        el("div", { class: "card-body" }, el("div", { class: "form-row", style: { maxWidth: "640px" } }, CVMS.field(t("auth.username"), user), CVMS.field(t("auth.password"), pass)))),
      el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("add.devices")), el("p", { class: "card-sub" }, t("add.devices_sub")))),
        el("div", { class: "card-body", style: { paddingBottom: "8px" } },
          el("div", { class: "toolbar" },
            el("div", { class: "input-search", style: { width: "220px" } }, icon("search"), q),
            el("div", { class: "spacer" }),
            range, port, scanBtn, discoverBtn),
          statusLine),
        tableBox,
        el("div", { class: "card-body row", style: { justifyContent: "space-between", borderTop: "1px solid var(--border)" } }, selInfo, addBtn)));

    function selectable(r) { return r.onvif && !r.added && r.status !== "adding"; }
    function visibleRows() {
      const needle = A.q.toLowerCase();
      return [...A.rows.values()]
        .filter((r) => !needle || r.ip.includes(needle) || (r.name || "").toLowerCase().includes(needle) || (r.hardware || "").toLowerCase().includes(needle))
        .sort((a, b) => (ipKey(a.ip) === ipKey(b.ip) ? a.port - b.port : ipKey(a.ip) < ipKey(b.ip) ? -1 : 1));
    }
    function statusBadge(r) {
      switch (r.status) {
        case "adding": return el("span", { class: "row", style: { gap: "6px" } }, el("div", { class: "spinner", style: { width: "14px", height: "14px" } }), t("add.st_adding"));
        case "ok": return el("span", { class: "badge badge-ok badge-dot", title: r.warning || "" }, t("add.st_ok"));
        case "added": return el("span", { class: "badge badge-dot" }, t("add.added"));
        case "error": return el("span", { class: "badge badge-danger badge-dot", title: r.error }, r.code === "onvif_auth" ? t("add.st_auth") : t("add.failed"));
        default: return r.onvif ? el("span", { class: "badge badge-accent" }, t("add.st_new")) : el("span", { class: "badge badge-warn", title: t("add.rtsp_only_hint") }, t("add.st_rtsp_only"));
      }
    }
    function updateSel() {
      const n = [...A.rows.values()].filter((r) => r.selected && selectable(r)).length;
      addBtn.lastChild.textContent = t("add.add_selected", { n });
      addBtn.disabled = n === 0;
      selInfo.textContent = t("add.summary", { total: A.rows.size, added: [...A.rows.values()].filter((r) => r.added).length });
    }
    function renderTable() {
      CVMS.clear(tableBox);
      const rows = visibleRows();
      if (!rows.length) {
        tableBox.append(CVMS.emptyState("radar", A.rows.size ? t("add.no_match") : t("add.none_yet"), A.rows.size ? "" : t("add.none_yet_hint")));
        updateSel();
        return;
      }
      const sel = rows.filter(selectable);
      const all = el("input", { type: "checkbox", checked: sel.length > 0 && sel.every((r) => r.selected), disabled: !sel.length });
      all.onchange = () => { sel.forEach((r) => (r.selected = all.checked)); renderTable(); };
      tableBox.append(el("div", { class: "table-wrap" }, el("table", { class: "table" },
        el("thead", null, el("tr", null, el("th", { style: { width: "36px" } }, el("label", { class: "check" }, all)), el("th", null, "IP"), el("th", null, t("add.port")), el("th", null, t("add.device")), el("th", null, t("add.protocols")), el("th", null, t("col.status")), el("th"))),
        el("tbody", null, rows.map((r) => {
          const cb = el("input", { type: "checkbox", checked: r.selected && selectable(r), disabled: !selectable(r) });
          cb.onchange = () => { r.selected = cb.checked; renderTable(); };
          return el("tr", null,
            el("td", null, el("label", { class: "check" }, cb)),
            el("td", { class: "mono" }, r.ip),
            el("td", { class: "mono muted" }, String(r.port)),
            el("td", null, el("div", null, r.name || el("span", { class: "dim" }, "—")), r.hardware ? el("div", { class: "dim", style: { fontSize: "11px" } }, r.hardware) : null,
              r.status === "error" && r.error ? el("div", { style: { fontSize: "11px", color: "var(--danger)" } }, r.error) : null),
            el("td", null, el("div", { class: "row", style: { gap: "4px" } }, r.onvif ? el("span", { class: "badge badge-info" }, "ONVIF") : null, r.rtsp ? el("span", { class: "badge" }, "RTSP") : null)),
            el("td", null, statusBadge(r)),
            el("td", { class: "actions" }, r.added ? null : btn({
              icon: "edit", size: "sm", title: t("add.configure"),
              onClick: () => cameraEditor(null, r.onvif ? { mode: "onvif", host: r.ip, port: r.port, name: r.name, user: A.user, pass: A.pass } : { mode: "rtsp", host: r.ip, user: A.user, pass: A.pass }),
            })));
        })))));
      updateSel();
    }

    async function discover() {
      discoverBtn.disabled = true;
      statusLine.textContent = t("add.scanning");
      try {
        const devs = await api("/api/discover", { method: "POST" });
        devs.forEach((d) => addRow({ ip: d.ip, port: d.port, name: d.name, hardware: d.hardware, onvif: true, added: d.added }));
        statusLine.textContent = t("add.found", { n: devs.length });
        A.discovered = true;
      } catch (e) { statusLine.textContent = e.message; }
      finally { discoverBtn.disabled = false; renderTable(); }
    }

    async function scanRange() {
      if (!A.range.trim()) { range.focus(); return; }
      scanBtn.disabled = true;
      statusLine.textContent = t("add.scanning_range", { r: A.range.trim() });
      try {
        const hits = await api("/api/scan", { method: "POST", body: { range: A.range.trim(), port: A.port } });
        hits.forEach((h) => addRow({ ip: h.ip, port: h.port, onvif: h.onvif, rtsp: h.rtsp, added: h.added }));
        statusLine.textContent = t("add.found", { n: hits.length });
      } catch (e) { statusLine.textContent = e.message; }
      finally { scanBtn.disabled = false; renderTable(); }
    }

    async function bulkAdd() {
      const targets = [...A.rows.values()].filter((r) => r.selected && selectable(r));
      if (!targets.length) return;
      if (!A.user) { user.focus(); CVMS.toast(t("add.need_creds"), "warn"); return; }
      targets.forEach((r) => { r.status = "adding"; r.error = ""; });
      renderTable();
      addBtn.disabled = true;
      try {
        const res = await api("/api/cameras/bulk", { method: "POST", body: { username: A.user, password: A.pass, devices: targets.map((r) => ({ host: r.ip, port: r.port, name: r.name })) } });
        res.results.forEach((x) => {
          const r = A.rows.get(rowKey(x.host, x.port));
          if (!r) return;
          r.selected = false;
          if (x.ok) { r.status = "ok"; r.added = true; r.name = x.name || r.name; r.warning = x.warning; }
          else if (x.code === "camera_exists") { r.status = "added"; r.added = true; }
          else { r.status = "error"; r.error = x.error; r.code = x.code; }
        });
        CVMS.toast(t("add.bulk_done", { n: res.added, total: targets.length }), res.added === targets.length ? "ok" : "warn");
        await loadCameras();
      } catch (e) {
        targets.forEach((r) => { if (r.status === "adding") { r.status = "error"; r.error = e.message; } });
        CVMS.err(e);
      }
      renderTable();
    }

    S.afterSave = () => { loadCameras().then(() => { const keys = new Set(S.cams.filter((k) => k.onvif_port).map((k) => k.host + ":" + k.onvif_port)); A.rows.forEach((r) => { if (keys.has(r.ip + ":" + r.port)) { r.added = true; if (r.status !== "ok") r.status = "added"; } }); renderTable(); }); };
    renderTable();
    if (!A.discovered) discover();
  }

  /* cam: existing camera or null. opts: {mode: onvif|rtsp, host, port, name} */
  function cameraEditor(cam, opts) {
    opts = opts || {};
    let mode = cam ? "rtsp" : opts.mode || "onvif";
    const isNew = !cam;
    const f = {
      name: el("input", { class: "input", value: cam ? cam.name : opts.name || "", maxlength: "100" }),
      host: el("input", { class: "input", value: cam ? cam.host : opts.host || "", placeholder: "192.168.1.64" }),
      port: el("input", { class: "input", type: "number", value: cam ? cam.onvif_port || 80 : opts.port || 80, min: "1", max: "65535" }),
      user: el("input", { class: "input", value: cam ? cam.username : opts.user !== undefined ? opts.user : "admin", autocomplete: "off" }),
      pass: el("input", { class: "input", type: "password", value: cam ? "" : opts.pass || "", autocomplete: "new-password", placeholder: cam && cam.has_password ? t("add.pw_keep") : "" }),
      main: el("input", { class: "input mono", value: cam ? cam.main.url : "", placeholder: "rtsp://" + (opts.host || "192.168.1.64") + ":554/Streaming/Channels/101" }),
      sub: el("input", { class: "input mono", value: cam ? cam.sub.url : "", placeholder: "rtsp://192.168.1.64:554/Streaming/Channels/102" }),
    };
    const meta = { manufacturer: cam ? cam.manufacturer : "", model: cam ? cam.model : "" };
    const tabs = el("div", { class: "tabs" });
    const onvifBox = el("div");
    const rtspBox = el("div");
    const profileBox = el("div");
    const mainRes = el("div", { class: "probe-result" });
    const subRes = el("div", { class: "probe-result" });

    function renderTabs() {
      CVMS.clear(tabs).append(
        el("button", { class: "tab" + (mode === "onvif" ? " active" : ""), onclick: () => { mode = "onvif"; renderTabs(); } }, "ONVIF"),
        el("button", { class: "tab" + (mode === "rtsp" ? " active" : ""), onclick: () => { mode = "rtsp"; renderTabs(); } }, "RTSP"));
      onvifBox.style.display = mode === "onvif" ? "" : "none";
      rtspBox.style.display = mode === "rtsp" ? "" : "none";
    }

    const connectBtn = btn({ label: t("add.connect"), icon: "link", kind: "primary", onClick: connectONVIF });
    onvifBox.append(
      el("div", { class: "form-row" }, CVMS.field(t("add.ip"), f.host), CVMS.field(t("add.onvif_port"), f.port)),
      el("div", { class: "form-row" }, CVMS.field(t("auth.username"), f.user), CVMS.field(t("auth.password"), f.pass)),
      el("div", { style: { marginBottom: "16px" } }, connectBtn),
      profileBox);

    const testBtn = btn({ label: t("add.test"), icon: "activity", size: "sm", onClick: testRTSP });
    rtspBox.append(
      CVMS.field(t("add.main_url"), f.main), mainRes,
      CVMS.field(t("add.sub_url"), f.sub, t("add.sub_hint")), subRes,
      el("div", { class: "form-row" }, CVMS.field(t("auth.username"), f.user.cloneNode()), CVMS.field(t("auth.password"), f.pass.cloneNode())),
      testBtn);
    // keep credential inputs in both tabs in sync
    const [u2, p2] = rtspBox.querySelectorAll(".form-row input");
    u2.value = f.user.value;
    const sync = (a, b) => { a.addEventListener("input", () => { b.value = a.value; }); };
    sync(f.user, u2); sync(u2, f.user); sync(f.pass, p2); sync(p2, f.pass);

    async function connectONVIF() {
      connectBtn.disabled = true;
      CVMS.clear(profileBox).append(el("div", { class: "row muted", style: { marginBottom: "12px" } }, el("div", { class: "spinner" }), t("add.connecting")));
      try {
        const r = await api("/api/onvif/probe", { method: "POST", body: { host: f.host.value.trim(), port: +f.port.value || 80, username: f.user.value, password: f.pass.value, camera_id: cam ? cam.id : "" } });
        meta.manufacturer = r.info.manufacturer; meta.model = r.info.model;
        if (!f.name.value) f.name.value = [r.info.manufacturer, r.info.model].filter(Boolean).join(" ") || f.host.value;
        const opt = (p) => el("option", { value: p.uri }, (p.name || p.token) + " · " + (p.codec || "?").toUpperCase() + " " + fmt.res(p.width, p.height));
        const selMain = el("select", { class: "select" }, r.info.profiles.map(opt));
        const selSub = el("select", { class: "select" }, el("option", { value: "" }, t("add.no_sub")), r.info.profiles.map(opt));
        selMain.value = r.main.uri || "";
        selSub.value = (r.sub && r.sub.uri) || "";
        const snapOf = (uri) => { const p = r.info.profiles.find((x) => x.uri === uri); return p ? p.snapshot_uri || "" : ""; };
        const apply = () => { f.main.value = selMain.value; f.sub.value = selSub.value; meta.mainSnap = snapOf(selMain.value); meta.subSnap = snapOf(selSub.value); };
        selMain.onchange = apply; selSub.onchange = apply; apply();
        CVMS.clear(profileBox).append(el("div", { class: "profile-box" },
          el("div", { class: "row", style: { marginBottom: "10px" } }, el("span", { class: "badge badge-ok" }, icon("check"), t("add.connected")), el("b", null, [r.info.manufacturer, r.info.model].filter(Boolean).join(" ")), r.info.firmware ? el("span", { class: "dim", style: { fontSize: "11px" } }, "fw " + r.info.firmware) : null),
          CVMS.field(t("cam.main"), selMain),
          CVMS.field(t("cam.sub"), selSub, t("add.sub_hint"))));
        profileBox.querySelectorAll(".badge svg").forEach((s) => { s.style.width = "11px"; s.style.height = "11px"; });
      } catch (e) {
        CVMS.clear(profileBox).append(notice("danger", "alert", e.message + (e.code === "onvif_failed" ? " — " + t("add.try_rtsp") : "")));
      } finally { connectBtn.disabled = false; }
    }

    async function probeOne(url, out) {
      CVMS.clear(out).append(el("div", { class: "spinner", style: { width: "14px", height: "14px" } }), t("add.testing"));
      try {
        const r = await api("/api/rtsp/probe", { method: "POST", body: { url, username: f.user.value, password: f.pass.value, camera_id: cam ? cam.id : "" } });
        CVMS.clear(out).append(el("span", { class: "badge badge-ok" }, "OK"), CVMS.codecBadge(r.codec) || el("span", { class: "badge" }, r.codec), r.width ? el("span", { class: "badge" }, fmt.res(r.width, r.height)) : null);
      } catch (e) {
        CVMS.clear(out).append(el("span", { class: "badge badge-danger" }, t("add.failed")), el("span", { class: "muted" }, e.message));
      }
    }
    async function testRTSP() {
      testBtn.disabled = true;
      const jobs = [];
      if (f.main.value.trim()) jobs.push(probeOne(f.main.value.trim(), mainRes));
      if (f.sub.value.trim()) jobs.push(probeOne(f.sub.value.trim(), subRes)); else CVMS.clear(subRes);
      await Promise.all(jobs);
      testBtn.disabled = false;
    }

    renderTabs();
    CVMS.modal({
      title: isNew ? t("nav.add_camera") : t("cams.edit", { name: cam.name }),
      wide: true,
      body: el("div", null, CVMS.field(t("col.name"), f.name), tabs, onvifBox, rtspBox),
      actions: [{ label: t("common.cancel") }, {
        label: isNew ? t("common.add") : t("common.save"), kind: "primary", icon: "save",
        onClick: async () => {
          if (!f.main.value.trim()) {
            if (mode === "onvif") throw new Error(t("add.connect_first"));
            throw new Error(t("err.main_url_required"));
          }
          const body = {
            name: f.name.value.trim(), host: mode === "onvif" ? f.host.value.trim() : (cam ? cam.host : ""), onvif_port: +f.port.value || 0,
            username: f.user.value.trim(), password: f.pass.value === "" && cam && cam.has_password ? null : f.pass.value,
            manufacturer: meta.manufacturer, model: meta.model, main_url: f.main.value.trim(), sub_url: f.sub.value.trim(),
            main_snapshot: meta.mainSnap || "", sub_snapshot: meta.subSnap || "",
          };
          if (cam) body.enabled = cam.enabled;
          const r = await api(isNew ? "/api/cameras" : "/api/cameras/" + cam.id, { method: isNew ? "POST" : "PUT", body });
          if (r.probe_error) CVMS.toast(t("add.saved_probe_error", { err: r.probe_error }), "warn");
          else CVMS.toast(isNew ? t("add.added_ok") : t("cams.saved"), "ok");
          await loadCameras();
          if (S.afterSave) S.afterSave();
        },
      }],
    });
  }

  /* ================================================================ server */
  function serverPage() {
    const c = page(t("nav.server"), t("srv.sub"));
    const url = el("input", { class: "input mono", placeholder: "http://192.168.1.10:8080" });
    const statusBox = el("div", { class: "card-body" });
    let loaded = false;
    c.append(
      el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("srv.connection")), el("p", { class: "card-sub" }, t("srv.connection_sub")))),
        statusBox),
      el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("srv.url")), el("p", { class: "card-sub" }, t("srv.url_sub")))),
        el("div", { class: "card-body" },
          el("div", { class: "input-group", style: { maxWidth: "620px" } }, url, btn({ label: t("common.save"), icon: "save", kind: "primary", onClick: save })),
          el("p", { class: "muted", style: { fontSize: "12px", margin: "10px 0 0" } }, t("srv.url_hint")))));

    async function save() {
      try {
        const s = await api("/api/settings");
        s.server_url = url.value.trim();
        await api("/api/settings", { method: "PUT", body: s });
        CVMS.toast(t("srv.saved"), "ok");
        setTimeout(loadStatus, 800);
      } catch (e) { CVMS.err(e); }
    }

    function render() {
      const s = (S.status && S.status.server) || {};
      if (!loaded && S.status) { url.value = s.server_url || ""; loaded = true; }
      const st = serverState();
      CVMS.clear(statusBox).append(
        el("div", { class: "conn-card" },
          el("div", { class: "conn-icon " + st.cls }, icon(st.icon)),
          el("div", { class: "grow" },
            el("div", { class: "conn-title" }, t(st.key)),
            el("div", { class: "muted", style: { fontSize: "12px" } }, t(st.key + "_hint")),
            s.error && s.state !== "connected" ? el("div", { class: "mono", style: { fontSize: "11px", color: "var(--danger)", marginTop: "4px" } }, s.error) : null),
          s.state === "connected" ? el("span", { class: "muted", style: { fontSize: "12px" } }, t("srv.since", { t: fmt.ago(s.since) })) : null),
        el("dl", { class: "kv", style: { marginTop: "16px" } },
          el("dt", null, t("settings.client_name")), el("dd", null, (S.status && S.status.name) || "—"),
          el("dt", null, t("srv.client_id")), el("dd", { class: "mono" }, (S.status && S.status.client_uid) || "—"),
          el("dt", null, t("dash.streams")), el("dd", null, String(s.streams || 0))));
    }
    S.refresh = render;
    render();
    api("/api/settings").then((s) => { url.value = s.server_url || ""; loaded = true; }).catch(() => {});
  }

  /* ================================================================ settings */
  async function settingsPage() {
    const c = page(t("nav.settings"), t("settings.sub"));
    let s;
    try { s = await api("/api/settings"); } catch (e) { return CVMS.err(e); }
    const f = {
      name: el("input", { class: "input", value: s.name, maxlength: "100" }),
      snap: el("input", { class: "input", type: "number", min: "1", max: "3600", value: s.snapshot_interval }),
      status: el("input", { class: "input", type: "number", min: "5", max: "3600", value: s.status_interval }),
      hw: el("select", { class: "select" }, ["auto", "cuda", "qsv", "vaapi", "d3d11va", "none"].map((m) => el("option", { value: m, selected: s.hwaccel === m }, t("settings.hw_" + m)))),
      gpu: el("input", { class: "input mono", value: s.gpu_device || "", placeholder: t("settings.gpu_device_ph") }),
      conc: el("input", { class: "input", type: "number", min: "1", max: "64", value: s.snapshot_concurrency }),
      enc: el("select", { class: "select" }, ["auto", "libx264", "h264_nvenc", "h264_qsv", "h264_vaapi", "h264_amf"].map((m) => el("option", { value: m, selected: s.h264_encoder === m }, m === "auto" ? t("settings.encoder_auto") : m))),
      ffmpeg: el("input", { class: "input mono", value: s.ffmpeg_path, placeholder: t("settings.ffmpeg_ph") }),
      listen: el("input", { class: "input mono", value: s.ui_listen }),
      idle: el("input", { class: "input", type: "number", min: "5", max: "1440", value: s.session_idle_minutes }),
    };
    const lang = CVMS.shell.langSelect();
    lang.classList.remove("lang-select");
    lang.addEventListener("change", () => setTimeout(settingsPage, 50));
    c.append(
      el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("settings.general")), el("p", { class: "card-sub" }, t("settings.general_sub")))),
        el("div", { class: "card-body" },
          el("div", { class: "form-row" }, CVMS.field(t("settings.client_name"), f.name, t("settings.client_name_hint")), CVMS.field(t("common.language"), lang)),
          el("div", { class: "form-row" }, CVMS.field(t("settings.snapshot_interval"), f.snap, t("settings.seconds")), CVMS.field(t("settings.status_interval"), f.status, t("settings.seconds"))))),
      el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("settings.video")), el("p", { class: "card-sub" }, t("settings.video_sub")))),
        el("div", { class: "card-body" },
          S.status && S.status.pipeline ? notice(S.status.pipeline.hwaccel ? "info" : "", "monitor", t("settings.pipeline_now", { p: S.status.pipeline.name })) : null,
          el("div", { class: "form-row" }, CVMS.field(t("settings.hwaccel"), f.hw, t("settings.hwaccel_hint")), CVMS.field(t("settings.encoder"), f.enc)),
          el("div", { class: "form-row" }, CVMS.field(t("settings.gpu_device"), f.gpu, t("settings.gpu_device_hint")), CVMS.field(t("settings.snapshot_concurrency"), f.conc, t("settings.snapshot_concurrency_hint"))),
          CVMS.field(t("settings.ffmpeg"), f.ffmpeg, t("settings.ffmpeg_hint")))),
      el("div", { class: "card" },
        el("div", { class: "card-head" }, el("div", null, el("h3", { class: "card-title" }, t("settings.security")), el("p", { class: "card-sub" }, t("settings.security_sub")))),
        el("div", { class: "card-body" },
          el("div", { class: "form-row" }, CVMS.field(t("settings.ui_listen"), f.listen, t("settings.ui_listen_hint")), CVMS.field(t("settings.session_idle"), f.idle, t("settings.minutes"))),
          btn({ label: t("auth.change_password"), icon: "key", onClick: changePassword }))),
      el("div", { style: { marginTop: "16px" } }, btn({ label: t("common.save"), icon: "save", kind: "primary", size: "lg", onClick: save })),
      el("div", { style: { marginTop: "16px" } }, CVMS.autostartCard()));

    async function save() {
      try {
        const body = Object.assign({}, s, {
          name: f.name.value.trim(), snapshot_interval: +f.snap.value, status_interval: +f.status.value,
          hwaccel: f.hw.value, gpu_device: f.gpu.value.trim(), snapshot_concurrency: +f.conc.value, h264_encoder: f.enc.value, ffmpeg_path: f.ffmpeg.value.trim(),
          ui_listen: f.listen.value.trim(), session_idle_minutes: +f.idle.value,
        });
        const r = await api("/api/settings", { method: "PUT", body });
        CVMS.toast(r.restart_required ? t("settings.saved_restart") : t("settings.saved"), r.restart_required ? "warn" : "ok");
        s = body;
        loadStatus();
      } catch (e) { CVMS.err(e); }
    }
  }

  main();
})();
