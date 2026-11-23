/* CVMS live player: WebRTC first, fragmented-MP4-over-WebSocket (MSE) as
 * fallback when WebRTC cannot connect (e.g. UDP blocked). */
(function () {
  "use strict";
  const t = CVMS.t;
  const el = CVMS.el;

  const PREF_KEY = "cvms.transport";
  function preferMSE() { try { return sessionStorage.getItem(PREF_KEY) === "mse"; } catch (e) { return false; } }
  function setPreferMSE(v) { try { if (v) sessionStorage.setItem(PREF_KEY, "mse"); else sessionStorage.removeItem(PREF_KEY); } catch (e) { /* ignore */ } }
  const MS = window.MediaSource || window.ManagedMediaSource;

  class Player {
    /* box: element (tile or viewer body). opts: {cameraId, profile, onState(state, info)} */
    constructor(box, opts) {
      this.box = box;
      this.opts = opts;
      this.video = el("video", { muted: true, autoplay: true, playsinline: true });
      this.video.muted = true;
      this.state = el("div", { class: "tile-state" });
      this.snap = null;
      this.stopped = true;
      this.retryTimer = null;
      this.gen = 0;
    }

    start() {
      if (!this.stopped) return;
      this.stopped = false;
      if (!this.video.isConnected) this.box.prepend(this.video);
      if (!this.state.isConnected) this.box.appendChild(this.state);
      this.video.style.visibility = "hidden";
      this.showSnapshot();
      this.connect(preferMSE() ? "mse" : "webrtc");
    }

    stop() {
      this.stopped = true;
      this.gen++;
      clearTimeout(this.retryTimer);
      this.teardown();
      this.video.removeAttribute("src");
      this.video.srcObject = null;
      try { this.video.load(); } catch (e) { /* ignore */ }
    }

    destroy() {
      this.stop();
      this.video.remove();
      this.state.remove();
      if (this.snap) this.snap.remove();
    }

    setState(kind, text) {
      CVMS.clear(this.state);
      if (kind === "loading") this.state.append(el("div", null, el("div", { class: "spinner" }), el("div", null, text || t("player.connecting"))));
      else if (kind === "error") this.state.append(el("div", null, text));
      else if (kind === "paused") this.state.append(el("div", null, text));
      this.state.style.display = kind === "playing" ? "none" : "grid";
      if (this.opts.onState) this.opts.onState(kind, this.info || {});
    }

    showSnapshot() {
      if (!this.snap) {
        this.snap = el("img", { alt: "" });
        this.snap.onerror = () => { this.snap.style.visibility = "hidden"; };
        this.snap.onload = () => { this.snap.style.visibility = "visible"; };
        this.box.prepend(this.snap);
      }
      this.snap.src = "/api/cameras/" + this.opts.cameraId + "/snapshot?profile=sub&_=" + Date.now();
      this.setState("loading");
    }

    teardown() {
      if (this.pc) { try { this.pc.close(); } catch (e) { /* ignore */ } this.pc = null; }
      if (this.ws) { try { this.ws.close(); } catch (e) { /* ignore */ } this.ws = null; }
      if (this.liveTimer) { clearInterval(this.liveTimer); this.liveTimer = null; }
      if (this.mseURL) { URL.revokeObjectURL(this.mseURL); this.mseURL = null; }
      clearTimeout(this.frameTimer);
      this.ms = null; this.sb = null; this.queue = [];
    }

    retry(delay, transport) {
      if (this.stopped) return;
      this.teardown();
      clearTimeout(this.retryTimer);
      this.retryTimer = setTimeout(() => { if (!this.stopped) this.connect(transport || (preferMSE() ? "mse" : "webrtc")); }, delay);
    }

    onPlaying() {
      clearTimeout(this.frameTimer);
      this.video.style.visibility = "visible";
      this.setState("playing");
    }

    watchFrames(gen) {
      clearTimeout(this.frameTimer);
      this.frameTimer = setTimeout(() => {
        if (gen !== this.gen || this.stopped) return;
        this.setState("error", t("player.no_video"));
        this.retry(4000);
      }, 25000);
    }

    async connect(transport) {
      const gen = ++this.gen;
      this.setState("loading");
      this.video.onplaying = () => { if (gen === this.gen) this.onPlaying(); };
      if (transport === "mse") return this.connectMSE(gen);
      return this.connectWebRTC(gen);
    }

    async connectWebRTC(gen) {
      if (!window.RTCPeerConnection) { setPreferMSE(true); return this.connectMSE(gen); }
      const pc = new RTCPeerConnection({ iceServers: [] });
      this.pc = pc;
      pc.addTransceiver("video", { direction: "recvonly" });
      let connected = false;
      pc.ontrack = (ev) => {
        if (gen !== this.gen) return;
        this.video.srcObject = ev.streams[0] || new MediaStream([ev.track]);
        this.video.play().catch(() => {});
      };
      pc.onconnectionstatechange = () => {
        if (gen !== this.gen) return;
        const s = pc.connectionState;
        if (s === "connected") { connected = true; setPreferMSE(false); this.watchFrames(gen); }
        if (s === "failed" || s === "disconnected" || s === "closed") {
          if (!connected) { setPreferMSE(true); this.retry(200, "mse"); }
          else { this.setState("loading", t("player.reconnecting")); this.retry(2000); }
        }
      };
      try {
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        const res = await CVMS.api("/api/cameras/" + this.opts.cameraId + "/webrtc?profile=" + this.opts.profile, { method: "POST", body: { sdp: offer.sdp } });
        if (gen !== this.gen) return;
        this.info = { codec: res.codec, transcoded: res.transcoded };
        await pc.setRemoteDescription({ type: "answer", sdp: res.sdp });
      } catch (e) {
        if (gen !== this.gen) return;
        if (e.status === 401 || e.status === 403 || e.status === 404) { this.setState("error", e.message); return; }
        this.setState("error", e.message);
        this.retry(5000);
        return;
      }
      setTimeout(() => {
        if (gen !== this.gen || connected) return;
        setPreferMSE(true);
        this.retry(100, "mse");
      }, 7000);
    }

    connectMSE(gen) {
      if (!MS) { this.setState("error", t("player.unsupported")); return; }
      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      const ws = new WebSocket(proto + "//" + location.host + "/api/cameras/" + this.opts.cameraId + "/mse?profile=" + this.opts.profile);
      ws.binaryType = "arraybuffer";
      this.ws = ws;
      this.queue = [];
      this.info = { transport: "mse" };
      this.watchFrames(gen);
      ws.onmessage = (ev) => {
        if (gen !== this.gen) return;
        if (typeof ev.data === "string") {
          const meta = JSON.parse(ev.data);
          const mime = 'video/mp4; codecs="' + meta.codec + '"';
          if (!MS.isTypeSupported(mime)) { this.setState("error", t("player.unsupported")); ws.close(); return; }
          const ms = new MS();
          this.ms = ms;
          this.mseURL = URL.createObjectURL(ms);
          this.video.disableRemotePlayback = true;
          this.video.src = this.mseURL;
          ms.addEventListener("sourceopen", () => {
            if (gen !== this.gen) return;
            try {
              this.sb = ms.addSourceBuffer(mime);
              this.sb.mode = "segments";
              this.sb.addEventListener("updateend", () => this.pump());
              this.pump();
            } catch (e) { this.setState("error", e.message); }
          });
          this.video.play().catch(() => {});
          this.liveTimer = setInterval(() => this.keepLive(), 1000);
          return;
        }
        this.queue.push(ev.data);
        if (this.queue.length > 300) this.queue.splice(0, this.queue.length - 300);
        this.pump();
      };
      ws.onclose = () => {
        if (gen !== this.gen || this.stopped) return;
        this.setState("loading", t("player.reconnecting"));
        this.retry(3000, "mse");
      };
    }

    pump() {
      const sb = this.sb;
      if (!sb || sb.updating || !this.queue.length) return;
      try { sb.appendBuffer(this.queue.shift()); }
      catch (e) {
        if (e.name === "QuotaExceededError") { this.trim(true); }
        else { this.retry(2000, "mse"); }
      }
    }

    trim(force) {
      const sb = this.sb, v = this.video;
      if (!sb || sb.updating || !sb.buffered.length) return;
      const start = sb.buffered.start(0);
      if (force || v.currentTime - start > 20) {
        try { sb.remove(start, Math.max(start, v.currentTime - 5)); } catch (e) { /* ignore */ }
      }
    }

    keepLive() {
      const sb = this.sb, v = this.video;
      if (!sb || !sb.buffered.length) return;
      const end = sb.buffered.end(sb.buffered.length - 1);
      if (end - v.currentTime > 1.5) v.currentTime = Math.max(0, end - 0.3);
      if (v.paused) v.play().catch(() => {});
      this.trim(false);
    }
  }

  CVMS.Player = Player;
})();
