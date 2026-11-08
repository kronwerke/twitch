"use strict";
// The Kronwerke Twitch dashboard. One page: the state comes from /api/overview, every
// change and every chat line from /api/events. Lists are updated in place by key, so
// focus, selections and half typed input survive every live update.

(() => {
  // ---- small helpers ----

  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => [...el.querySelectorAll(s)];
  const NS = "http://www.w3.org/2000/svg";
  const reduced = matchMedia("(prefers-reduced-motion: reduce)");
  const phone = matchMedia("(max-width: 959px)");
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  const bytes = (s) => new TextEncoder().encode(s).length;
  const cap = (s) => (s ? String(s).charAt(0).toUpperCase() + String(s).slice(1) : "");

  function h(tag, props, ...kids) {
    const el = document.createElement(tag);
    if (props) {
      for (const [k, v] of Object.entries(props)) {
        if (v == null || v === false) continue;
        if (k === "class") el.className = v;
        else if (k === "text") el.textContent = v;
        else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
        else if (k === "style" && typeof v === "object") for (const [p, x] of Object.entries(v)) el.style.setProperty(p, x);
        else el.setAttribute(k, v === true ? "" : v);
      }
    }
    for (const c of kids.flat()) if (c != null && c !== false) el.append(c instanceof Node ? c : String(c));
    return el;
  }

  function icon(name, cls) {
    const s = document.createElementNS(NS, "svg");
    s.setAttribute("class", "i" + (cls ? " " + cls : ""));
    s.setAttribute("aria-hidden", "true");
    const u = document.createElementNS(NS, "use");
    u.setAttribute("href", "#i-" + name);
    s.append(u);
    return s;
  }

  function setText(el, v) {
    v = v == null ? "" : String(v);
    if (el.textContent !== v) el.textContent = v;
  }

  function setAttr(el, k, v) {
    if (v == null || v === false) {
      if (el.hasAttribute(k)) el.removeAttribute(k);
      return;
    }
    v = v === true ? "" : String(v);
    if (el.getAttribute(k) !== v) el.setAttribute(k, v);
  }

  function flag(el, cls, on) {
    if (el.classList.contains(cls) !== !!on) el.classList.toggle(cls, !!on);
  }

  // build replaces the children of el only when sig changed.
  function once(el, sig, build) {
    if (el._sig === sig) return false;
    el._sig = sig;
    el.replaceChildren(...[build()].flat().filter(Boolean));
    return true;
  }

  function restartAnim(el, cls) {
    el.classList.remove(cls);
    void el.offsetWidth;
    el.classList.add(cls);
  }

  // ---- keyed lists ----

  // keyed brings the children of box in line with items: existing elements are patched
  // in place, new ones animate in, removed ones animate out. Nothing that stays is
  // recreated or moved without need, so focus and input survive.
  function keyed(box, items, keyOf, make, patch, opts = {}) {
    const map = box._k || (box._k = new Map());
    const first = !box._ready;
    for (const c of [...box.children]) if (!c._key && !c._leaving && c !== box._empty) c.remove();
    const want = new Set();
    let prev = null;
    let n = 0;
    for (const it of items) {
      const k = keyOf(it);
      want.add(k);
      let el = map.get(k);
      if (!el) {
        el = make(it, k);
        el._key = k;
        map.set(k, el);
        if (!reduced.matches && (!first || opts.stagger)) {
          el.classList.add("entering");
          if (first) el.style.animationDelay = Math.min(n, 8) * 35 + "ms";
          const done = (e) => {
            if (e.target !== el) return;
            el.classList.remove("entering");
            el.style.animationDelay = "";
            el.removeEventListener("animationend", done);
          };
          el.addEventListener("animationend", done);
        }
        n++;
      }
      patch(el, it, k);
      let next = prev ? prev.nextElementSibling : box.firstElementChild;
      while (next && (next._leaving || next === box._empty)) next = next.nextElementSibling;
      if (next !== el) box.insertBefore(el, next);
      prev = el;
    }
    for (const [k, el] of map) {
      if (!want.has(k)) {
        map.delete(k);
        leave(el, opts.grid);
      }
    }
    box._ready = true;
  }

  function leave(el, grid) {
    el._leaving = true;
    el._key = null;
    if (reduced.matches || !el.isConnected || !el.offsetParent) return el.remove();
    if (grid) {
      el.classList.add("leaving-fade");
      setTimeout(() => el.remove(), 260);
      return;
    }
    el.style.height = el.offsetHeight + "px";
    void el.offsetHeight;
    el.classList.add("leaving");
    requestAnimationFrame(() => {
      el.style.height = "0px";
      el.style.marginTop = "0px";
      el.style.paddingTop = "0px";
      el.style.paddingBottom = "0px";
      el.style.borderWidth = "0px";
    });
    setTimeout(() => el.remove(), 340);
  }

  // list is keyed plus an empty state that is only rebuilt when its reason changes.
  function list(box, items, keyOf, make, patch, empty, opts) {
    keyed(box, items, keyOf, make, patch, opts);
    if (items.length) {
      if (box._empty) {
        box._empty.remove();
        box._empty = null;
      }
      return;
    }
    const [sig, build] = empty();
    if (box._empty && box._emptySig === sig) return;
    if (box._empty) box._empty.remove();
    box._empty = build();
    box._emptySig = sig;
    box.append(box._empty);
  }

  function emptyState(iconName, title, text, action) {
    return h("div", { class: "empty" }, h("span", { class: "empty-icon" }, icon(iconName)), h("p", { class: "empty-title", text: title }), text && h("p", { class: "empty-text", text }), action);
  }

  // ---- formatting ----

  const MONTHS = ["Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"];
  const WEEKDAYS = ["Sonntag", "Montag", "Dienstag", "Mittwoch", "Donnerstag", "Freitag", "Samstag"];
  const rtf = new Intl.RelativeTimeFormat("de", { numeric: "auto" });
  const nf = new Intl.NumberFormat("de-DE");
  const valid = (t) => !!t && !String(t).startsWith("0001");
  const ms = (t) => (valid(t) ? Date.parse(t) : NaN);

  function clock(t, secs) {
    return new Date(t).toLocaleTimeString("de-DE", { hour: "2-digit", minute: "2-digit", second: secs ? "2-digit" : undefined });
  }
  function dateLong(t) {
    const d = new Date(t);
    return d.getDate() + ". " + MONTHS[d.getMonth()] + " " + d.getFullYear();
  }
  function ago(t) {
    const s = (ms(t) - Date.now()) / 1000;
    if (isNaN(s)) return "";
    const a = Math.abs(s);
    if (a < 45) return "gerade eben";
    if (a < 3600) return rtf.format(Math.round(s / 60), "minute");
    if (a < 86400) return rtf.format(Math.round(s / 3600), "hour");
    return rtf.format(Math.round(s / 86400), "day");
  }
  function uptime(t) {
    const m = Math.max(0, Math.floor((Date.now() - ms(t)) / 60000));
    if (isNaN(m)) return "";
    const hrs = Math.floor(m / 60);
    return hrs ? hrs + " Std. " + (m % 60) + " Min." : m + " Min.";
  }
  const plural = (n, one, many) => nf.format(n) + " " + (n === 1 ? one : many);

  // The season start is local Berlin time, as "2027-01-16T18:00".
  const berlin = new Intl.DateTimeFormat("en-US", { timeZone: "Europe/Berlin", hourCycle: "h23", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
  function berlinOffset(t) {
    const p = Object.fromEntries(berlin.formatToParts(new Date(t)).map((x) => [x.type, x.value]));
    return Date.UTC(+p.year, p.month - 1, +p.day, +p.hour % 24, +p.minute, +p.second) - Math.floor(t / 1000) * 1000;
  }
  function parseStart(s) {
    const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})/.exec(s || "");
    if (!m) return null;
    const guess = Date.UTC(+m[1], m[2] - 1, +m[3], +m[4], +m[5]);
    let t = guess - berlinOffset(guess);
    t = guess - berlinOffset(t);
    const wd = new Date(Date.UTC(+m[1], m[2] - 1, +m[3])).getUTCDay();
    const words = WEEKDAYS[wd] + ", " + +m[3] + ". " + MONTHS[m[2] - 1] + " " + m[1] + ", um " + +m[4] + (m[5] !== "00" ? ":" + m[5] : "") + " Uhr";
    return { t, words };
  }

  function hue(s) {
    let x = 0;
    for (const c of String(s)) x = (x * 31 + c.charCodeAt(0)) % 360;
    return x;
  }
  const channelColor = (l) => `hsl(${hue(l)} 62% 72%)`;
  const avatarColor = (l) => `hsl(${hue(l)} 42% 64%)`;

  // userColor keeps Twitch name colours readable on the dark background.
  function userColor(hex, fallback) {
    const m = /^#?([0-9a-f]{6})$/i.exec(hex || "");
    if (!m) return channelColor(fallback || "");
    const n = parseInt(m[1], 16);
    const r = (n >> 16) / 255, g = ((n >> 8) & 255) / 255, b = (n & 255) / 255;
    const mx = Math.max(r, g, b), mn = Math.min(r, g, b);
    let hh = 0, ss = 0;
    const l = (mx + mn) / 2;
    if (mx !== mn) {
      const d = mx - mn;
      ss = l > 0.5 ? d / (2 - mx - mn) : d / (mx + mn);
      hh = mx === r ? (g - b) / d + (g < b ? 6 : 0) : mx === g ? (b - r) / d + 2 : (r - g) / d + 4;
      hh *= 60;
    }
    return `hsl(${Math.round(hh)} ${Math.round(Math.min(ss, 0.85) * 100)}% ${Math.round(Math.max(l, 0.64) * 100)}%)`;
  }

  function thumbURL(u) {
    u = String(u || "").replace("{width}", "640").replace("{height}", "360");
    if (u.startsWith("https://static-cdn.jtvnw.net/")) u += (u.includes("?") ? "&" : "?") + "t=" + Math.floor(Date.now() / 300000);
    return u;
  }

  function loginFrom(s) {
    s = String(s || "").trim().toLowerCase();
    const m = /(?:^|\/\/|\.)twitch\.tv\/(?:popout\/)?@?([a-z0-9_]+)/.exec(s);
    if (m) return m[1];
    return s.replace(/^@/, "").replace(/\/+$/, "");
  }

  // ---- state ----

  const S = {
    me: null,
    ov: null,
    version0: "",
    updateTo: "",
    view: "",
    lost: false,
    lastState: 0,
    lines: [],
    seen: new Set(),
    filter: "",
    paused: false,
    queue: [],
    atBottom: true,
    unseen: 0,
    unread: 0,
    flash: null,
  };

  function normalize(o) {
    o = o || {};
    for (const k of ["channels", "commands", "timers", "users"]) o[k] = Array.isArray(o[k]) ? o[k] : [];
    o.chat = o.chat || { connected: false, joined: [] };
    o.chat.joined = Array.isArray(o.chat.joined) ? o.chat.joined : [];
    o.settings = o.settings || {};
    for (const c of o.commands) {
      c.aliases = c.aliases || [];
      c.channels = c.channels || [];
    }
    for (const t of o.timers) t.channels = t.channels || [];
    return o;
  }

  const channel = (login) => S.ov?.channels.find((c) => c.login === login);
  const displayName = (login) => channel(login)?.name || login;
  const botName = () => S.ov?.bot?.name || S.ov?.bot?.login || "Bot";

  // ---- API ----

  class ApiError extends Error {
    constructor(msg, status) {
      super(msg);
      this.status = status;
    }
  }

  function statusText(st) {
    if (st >= 502 && st <= 504) return "Der Server ist gerade nicht erreichbar.";
    if (st === 413) return "Das ist zu lang.";
    return "Das hat nicht geklappt (Fehler " + st + ").";
  }

  async function api(method, path, body, opt = {}) {
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), opt.timeout || 15000);
    let r;
    try {
      r = await fetch(path, {
        method,
        headers: body === undefined ? { "X-Csrf-Token": S.me?.csrf || "" } : { "Content-Type": "application/json", "X-Csrf-Token": S.me?.csrf || "" },
        body: body === undefined ? undefined : JSON.stringify(body),
        credentials: "same-origin",
        cache: "no-store",
        signal: ctl.signal,
      });
    } catch {
      throw new ApiError(ctl.signal.aborted ? "Der Server antwortet nicht." : "Keine Verbindung zum Server.", 0);
    } finally {
      clearTimeout(timer);
    }
    if (r.status === 401) {
      sessionLost();
      throw new ApiError("Du bist nicht mehr angemeldet.", 401);
    }
    if (r.status === 204) return null;
    let data = null;
    try {
      data = await r.json();
    } catch {
      data = null;
    }
    if (r.status === 403 && data?.error?.includes("CSRF") && !opt.retried) {
      // the session got a new token, for example after signing in again in another tab
      const me = await api("GET", "/api/me");
      S.me = me;
      return api(method, path, body, { ...opt, retried: true });
    }
    if (!r.ok) throw new ApiError(cap(data?.error) || statusText(r.status), r.status);
    return data;
  }

  // after a change the server sends the new state on the stream; if that does not come
  // (stream down), fetch it.
  function expectState() {
    const t = Date.now();
    setTimeout(() => {
      if (S.lastState < t) loadOverview().catch(() => {});
    }, 900);
  }

  async function loadOverview() {
    applyOverview(await api("GET", "/api/overview"));
  }

  function busy(btn, on) {
    if (btn) flag(btn, "busy", on);
  }

  // ---- toasts ----

  function toast(text, kind = "ok") {
    const box = $("#toasts");
    for (const t of box.children) {
      if (t._text === text && !t.classList.contains("out")) {
        t._restart();
        return;
      }
    }
    let timer;
    const el = h(
      "div",
      { class: "toast " + kind, role: kind === "err" ? "alert" : "status" },
      h("span", { class: "toast-icon" }, icon(kind === "err" ? "alert" : kind === "info" ? "signal" : "check")),
      h("span", { class: "toast-text", text }),
      h("button", { class: "icon-btn", type: "button", "aria-label": "Schließen", onclick: () => close() }, icon("x"))
    );
    el._text = text;
    const life = kind === "err" ? 6500 : 3200;
    const close = () => {
      clearTimeout(timer);
      if (el.classList.contains("out")) return;
      if (reduced.matches) return el.remove();
      el.classList.add("out");
      setTimeout(() => el.remove(), 260);
    };
    el._restart = () => {
      clearTimeout(timer);
      timer = setTimeout(close, life);
      restartAnim(el, "toast");
    };
    el.addEventListener("mouseenter", () => clearTimeout(timer));
    el.addEventListener("mouseleave", () => (timer = setTimeout(close, 1800)));
    box.append(el);
    const live = [...box.children].filter((t) => !t.classList.contains("out"));
    if (live.length > 4) live[0].querySelector("button").click();
    timer = setTimeout(close, life);
  }

  // ---- confirm dialog ----

  function ask({ title, text, ok = "OK", danger = false }) {
    return new Promise((resolve) => {
      const d = $("#modal");
      const before = document.activeElement;
      flag(d, "danger", danger);
      setText($("#modal-title", d), title);
      setText($(".modal-text", d), text);
      const okb = $("[data-ok]", d);
      const cancel = $("[data-cancel]", d);
      okb.className = "btn " + (danger ? "danger" : "primary");
      setText(okb, ok);
      let done = false;
      const finish = (v) => {
        if (done) return;
        done = true;
        d.classList.add("closing");
        setTimeout(() => {
          d.classList.remove("closing");
          d.close();
          if (before && before.isConnected) before.focus({ preventScroll: true });
          resolve(v);
        }, reduced.matches ? 0 : 170);
      };
      okb.onclick = (e) => {
        e.preventDefault();
        finish(true);
      };
      cancel.onclick = (e) => {
        e.preventDefault();
        finish(false);
      };
      d.oncancel = (e) => {
        e.preventDefault();
        finish(false);
      };
      d.onclick = (e) => {
        if (e.target === d) finish(false);
      };
      d.showModal();
      (danger ? cancel : okb).focus();
    });
  }

  // ---- banner ----

  const banner = { el: null, kind: "", okUntil: 0, tick: 0 };

  function refreshBanner() {
    const el = $("#banner");
    const now = Date.now();
    let want = null;
    if (S.ov && Live.downAt && now - Live.downAt > 1500) {
      const s = Live.nextAt ? Math.max(1, Math.ceil((Live.nextAt - now) / 1000)) : 0;
      want = ["down", s ? "Verbindung zum Server verloren. Neuer Versuch in " + s + " s." : "Verbindung zum Server verloren. Verbinde neu.", "Jetzt versuchen", () => Live.now(), "offline"];
    } else if (banner.okUntil > now) {
      want = ["ok", "Wieder verbunden. Alles ist auf dem neuesten Stand.", "", null, "check"];
    } else if (S.updateTo) {
      want = ["info", "Eine neue Version läuft (" + S.updateTo + "). Lade neu, um das aktuelle Dashboard zu sehen.", "Neu laden", () => location.reload(), "refresh"];
    }
    if (!want) {
      if (!el.hidden && !el.classList.contains("out")) {
        el.classList.add("out");
        setTimeout(() => {
          el.hidden = true;
          el.classList.remove("out");
        }, reduced.matches ? 0 : 280);
      }
      banner.kind = "";
      return;
    }
    const [kind, text, btnText, fn, ic] = want;
    el.classList.remove("out");
    el.className = "banner" + (kind === "down" ? "" : " " + kind);
    if (el.hidden) el.hidden = false;
    setText($(".banner-text", el), text);
    $(".banner-icon use", el).setAttribute("href", "#i-" + ic);
    const btn = $(".banner-btn", el);
    btn.hidden = !btnText;
    setText(btn, btnText);
    btn.onclick = fn;
    banner.kind = kind;
  }
  setInterval(() => {
    if (Live.downAt || banner.kind === "ok" || banner.kind === "down") refreshBanner();
  }, 500);

  function setConn(st) {
    const text = { live: "Live verbunden", connecting: "Verbinde", down: "Getrennt" }[st];
    for (const el of $$("[data-conn]")) {
      setAttr(el, "data-conn", st);
      setText($(".conn-text", el), text);
      setAttr(el, "title", st === "live" ? "Live-Updates laufen" : st === "down" ? "Keine Verbindung zum Server" : "Verbinde mit dem Server");
    }
  }

  // ---- live stream ----

  const Live = {
    es: null,
    tries: 0,
    retryT: 0,
    dogT: 0,
    downAt: 0,
    nextAt: 0,

    start() {
      this.stop();
      if (S.lost) return;
      if (!this.downAt) setConn("connecting");
      let es;
      try {
        es = new EventSource("/api/events");
      } catch {
        return this.fail(true);
      }
      this.es = es;
      es.onopen = () => {
        const was = this.downAt;
        this.downAt = 0;
        this.nextAt = 0;
        this.tries = 0;
        setConn("live");
        this.kick();
        if (was && Date.now() - was > 1500) banner.okUntil = Date.now() + 2800;
        refreshBanner();
        resync();
      };
      es.onmessage = (e) => {
        this.kick();
        let ev;
        try {
          ev = JSON.parse(e.data);
        } catch {
          return;
        }
        onEvent(ev);
      };
      es.addEventListener("ping", () => this.kick());
      es.onerror = () => this.fail();
      this.kick();
    },

    // the server pings every 25 s; silence for much longer means the line is dead
    kick() {
      clearTimeout(this.dogT);
      this.dogT = setTimeout(() => this.fail(), 65000);
    },

    fail(force) {
      if (!this.es && !force) return;
      this.stop();
      if (!this.downAt) this.downAt = Date.now();
      setConn("down");
      this.retry();
    },

    retry() {
      clearTimeout(this.retryT);
      const delay = Math.min(15000, 700 * 2 ** this.tries) * (0.8 + Math.random() * 0.4);
      this.tries++;
      this.nextAt = Date.now() + delay;
      this.retryT = setTimeout(() => this.probe(), delay);
      refreshBanner();
    },

    // probe asks the server first, so an expired session shows the sign in instead of
    // reconnecting for ever
    async probe() {
      clearTimeout(this.retryT);
      this.nextAt = 0;
      refreshBanner();
      try {
        await api("GET", "/api/me", undefined, { timeout: 8000 });
      } catch (e) {
        if (e.status === 401) return;
        return this.retry();
      }
      this.start();
    },

    now() {
      this.tries = 0;
      if (this.downAt || !this.es) this.probe();
    },

    stop() {
      clearTimeout(this.dogT);
      clearTimeout(this.retryT);
      if (this.es) {
        this.es.onopen = this.es.onmessage = this.es.onerror = null;
        this.es.close();
        this.es = null;
      }
    },
  };

  async function resync() {
    try {
      const [ov, hist] = await Promise.all([api("GET", "/api/overview"), api("GET", "/api/chat?n=300")]);
      applyOverview(ov);
      addLines(hist || [], false);
      historyLoaded();
      if (S.view === "log") loadLog();
    } catch {
      // the stream is open; the next state on it brings everything up to date
    }
  }

  function onEvent(ev) {
    if (ev.kind === "message" && ev.message) addLines([ev.message], true);
    else if (ev.kind === "state" && ev.state) {
      S.lastState = Date.now();
      applyOverview(ev.state);
      if (S.view === "log") loadLogSoon();
    }
  }

  function applyOverview(ov) {
    S.ov = normalize(ov);
    if (!S.version0) S.version0 = S.ov.version;
    else if (S.ov.version && S.ov.version !== S.version0 && S.updateTo !== S.ov.version) {
      S.updateTo = S.ov.version;
      refreshBanner();
    }
    document.body.classList.remove("booting");
    hideFail();
    renderAll();
  }

  function sessionLost() {
    if (S.lost) return;
    S.lost = true;
    Live.stop();
    showLogin(S.ov ? "Deine Sitzung ist abgelaufen. Melde dich bitte neu an." : "");
  }

  function showLogin(note) {
    const n = $("#login-note");
    n.hidden = !note;
    setText(n, note);
    $("#login").hidden = false;
    $("#app").hidden = true;
    $("#more-sheet").hidden = true;
    document.title = "Anmelden · Kronwerke Twitch";
  }

  // ---- first load and failure ----

  let failWake = null;

  function hideFail() {
    if (!document.body.classList.contains("failed")) return;
    document.body.classList.remove("failed");
    $("#fail").hidden = true;
  }

  async function failWait(err, attempt) {
    document.body.classList.add("failed");
    const box = $("#fail");
    box.hidden = false;
    setText($(".fail-detail", box), err.status && (err.status < 502 || err.status > 504) ? err.message : "");
    const wait = Math.min(15000, 2000 * 2 ** attempt);
    const until = Date.now() + wait;
    const auto = $(".fail-auto", box);
    const btn = $("#fail-retry");
    busy(btn, false);
    await new Promise((resolve) => {
      failWake = resolve;
      const tick = () => {
        const s = Math.ceil((until - Date.now()) / 1000);
        if (s <= 0) return resolve();
        setText(auto, "Nächster Versuch in " + s + " s");
        box._t = setTimeout(tick, 250);
      };
      tick();
    });
    clearTimeout(box._t);
    failWake = null;
    setText(auto, "Versuche es");
    busy(btn, true);
  }

  // ---- router ----

  const VIEWS = { overview: "Übersicht", chat: "Live-Chat", channels: "Kanäle", commands: "Commands", timers: "Timer", settings: "Einstellungen", access: "Zugänge", log: "Protokoll" };
  const MORE = ["timers", "settings", "access", "log"];

  function route() {
    let id = location.hash.slice(1);
    if (id === "bot") {
      history.replaceState(null, "", "#overview");
      id = "overview";
      setTimeout(() => toast("Bot-Konto verbunden. Er geht gleich in die Chats."), 400);
    }
    if (!VIEWS[id]) id = "overview";
    if (id === S.view) return;
    S.view = id;
    for (const v of $$(".view")) flag(v, "on", v.dataset.view === id);
    for (const a of $$(".nav a, .tabbar a, .sheet-nav a")) {
      const on = a.dataset.view === id;
      flag(a, "on", on);
      setAttr(a, "aria-current", on ? "page" : null);
    }
    flag($("#more-btn"), "on", MORE.includes(id));
    const main = $("#main");
    flag(main, "fill", id === "chat");
    main.style.scrollBehavior = "auto";
    main.scrollTop = 0;
    main.style.scrollBehavior = "";
    moveIndicator();
    document.title = VIEWS[id] + " · Kronwerke Twitch";
    closeSheet();
    if (id === "chat") {
      S.unread = 0;
      renderBadges();
      S.atBottom = true;
      requestAnimationFrame(() => toBottom(false));
    }
    if (id === "log") loadLog();
  }

  function moveIndicator() {
    const ind = $(".nav-ind");
    const a = $(".nav a.on");
    if (!ind || !a) return;
    const first = !ind._placed;
    if (first) ind.style.transition = "none";
    ind.style.transform = `translateY(${a.offsetTop}px)`;
    ind.style.opacity = "1";
    if (first) {
      void ind.offsetWidth;
      ind.style.transition = "";
      ind._placed = true;
    }
  }

  function openSheet() {
    const s = $("#more-sheet");
    s.hidden = false;
    s.classList.remove("closing");
    $(".sheet-nav a", s).focus({ preventScroll: true });
  }
  function closeSheet() {
    const s = $("#more-sheet");
    if (s.hidden || s.classList.contains("closing")) return;
    if (reduced.matches) {
      s.hidden = true;
      return;
    }
    s.classList.add("closing");
    setTimeout(() => {
      s.hidden = true;
      s.classList.remove("closing");
    }, 240);
  }

  // ---- rendering ----

  function renderAll() {
    renderMe();
    renderBadges();
    renderOverview();
    renderChatChrome();
    renderChannels();
    renderCommands();
    CmdEd.sync();
    renderTimers();
    TmrEd.sync();
    renderSettings();
    renderSys();
    renderUsers();
  }

  function renderMe() {
    if (!S.me) return;
    for (const el of $$("[data-me-name]")) setText(el, S.me.login);
    for (const el of $$("[data-me-avatar]")) setAvatar(el, S.me.login, "");
    for (const el of $$("[data-version]")) setText(el, S.ov?.version || "");
  }

  function renderBadges() {
    const o = S.ov;
    const vals = {
      live: o ? o.channels.filter((c) => c.live).length : 0,
      chat: S.view === "chat" ? 0 : S.unread,
      channels: o ? o.channels.length : 0,
      commands: o ? o.commands.length : 0,
      timers: o ? o.timers.length : 0,
    };
    for (const el of $$("[data-badge]")) {
      const v = vals[el.dataset.badge] || 0;
      el.hidden = !v;
      setText(el, v > 99 ? "99+" : v);
      if (el.dataset.badge === "live") setAttr(el, "title", v + " live");
    }
  }

  function setAvatar(el, name, image) {
    name = name || "?";
    if (el._name !== name) {
      el._name = name;
      el.style.setProperty("--av", avatarColor(name.toLowerCase()));
      if (!el._letter) el._letter = el.appendChild(document.createTextNode(""));
      el._letter.data = name.trim().charAt(0).toUpperCase();
    }
    if (el._img !== (image || "")) {
      el._img = image || "";
      el.querySelector("img")?.remove();
      if (image) el.append(picture(image));
    }
  }

  function avatar(name, image, cls) {
    const el = h("span", { class: "avatar" + (cls ? " " + cls : ""), "aria-hidden": "true" });
    setAvatar(el, name, image);
    return el;
  }

  function picture(src, onfail) {
    const i = new Image();
    i.alt = "";
    i.decoding = "async";
    i.referrerPolicy = "no-referrer";
    i.onload = () => i.classList.add("ok");
    i.onerror = () => {
      i.remove();
      if (onfail) onfail();
    };
    i.src = src;
    return i;
  }

  // switches change at once and roll back when the server says no
  function switchEl(label, onChange, tick) {
    const input = h("input", { type: "checkbox", role: "switch", "aria-label": label });
    const sw = h("span", { class: "sw" }, input, h("i"));
    input.addEventListener("click", (e) => e.stopPropagation());
    input.addEventListener("change", async () => {
      const want = input.checked;
      input._pending = true;
      sw.classList.add("wait");
      try {
        await onChange(want);
        if (tick) {
          tick.classList.add("show");
          clearTimeout(tick._t);
          tick._t = setTimeout(() => tick.classList.remove("show"), 1400);
        }
        expectState();
      } catch (e) {
        input.checked = !want;
        restartAnim(sw, "nope");
        toast(e.message, "err");
      } finally {
        input._pending = false;
        sw.classList.remove("wait");
      }
    });
    sw._set = (v) => {
      if (!input._pending && input.checked !== !!v) input.checked = !!v;
    };
    return sw;
  }

  function tickEl() {
    return h("span", { class: "tick", "aria-hidden": "true" }, icon("check"));
  }

  // ---- overview ----

  function renderOverview() {
    const o = S.ov;
    const live = o.channels.filter((c) => c.live).sort((a, b) => b.live.viewer_count - a.live.viewer_count);
    const enabled = o.channels.filter((c) => c.enabled).length;

    let sub;
    if (!o.configured) sub = "Die Twitch-Anwendung ist noch nicht eingerichtet.";
    else if (!o.bot) sub = "Verbinde ein Bot-Konto, damit der Bot in die Chats kommt.";
    else if (!o.chat.connected) sub = "Der Bot ist gerade nicht mit dem Twitch-Chat verbunden.";
    else if (!o.channels.length) sub = "Der Bot ist verbunden. Füge Kanäle hinzu, damit er in die Chats geht.";
    else sub = "Der Bot sitzt in " + plural(o.chat.joined.length, "Chat", "Chats") + (live.length ? ", " + plural(live.length, "Kanal ist", "Kanäle sind") + " gerade live." : ", gerade ist niemand live.");
    setText($("#ov-sub"), sub);

    renderSeason();
    renderBot(enabled);

    once($("#ov-stats"), JSON.stringify([o.channels.length, o.channels.filter((c) => c.website).length, o.chat.joined.length, enabled, o.commands.length, o.commands.filter((c) => c.enabled).length, o.timers.length, o.timers.filter((t) => t.enabled).length]), () => [
      stat("#channels", "channels", "Kanäle", o.channels.length, o.channels.filter((c) => c.website).length + " auf der Website"),
      stat("#chat", "chat", "Im Chat", o.chat.joined.length, "von " + plural(enabled, "aktivem Kanal", "aktiven Kanälen")),
      stat("#commands", "commands", "Commands", o.commands.length, o.commands.filter((c) => c.enabled).length + " aktiv"),
      stat("#timers", "timers", "Timer", o.timers.length, o.timers.filter((t) => t.enabled).length + " aktiv"),
    ]);

    const pill = $("#ov-live-count");
    pill.hidden = !live.length;
    setText(pill, live.length);
    renderLiveMeta();

    list(
      $("#ov-streams"),
      live,
      (c) => c.login,
      makeStream,
      patchStream,
      () => {
        if (!o.channels.length) return ["none", () => emptyState("channels", "Noch keine Kanäle", "Füge die Kanäle der Kronwerke-Streamer hinzu. Wer live ist, erscheint dann hier.", h("a", { class: "btn primary sm", href: "#channels" }, icon("plus"), "Kanal hinzufügen"))];
        if (o.live_error) return ["err" + o.live_error, () => emptyState("alert", "Twitch antwortet gerade nicht", "Die Live-Abfrage ist fehlgeschlagen: " + o.live_error)];
        return ["quiet", () => emptyState("signal", "Gerade ist niemand live", "Sobald einer der Kanäle streamt, erscheint er hier mit Vorschaubild.")];
      },
      { grid: true, stagger: true }
    );
    const e = $("#ov-streams")._empty;
    if (e) e.classList.add("live-empty");
    flag($("#ov-streams"), "streams", true);
    $("#ov-streams").style.display = live.length ? "" : "block";

    renderMini();
  }

  function renderLiveMeta() {
    const o = S.ov;
    if (!o) return;
    setText($("#ov-live-meta"), valid(o.live_at) ? "Geprüft " + ago(o.live_at) : "");
  }

  function stat(href, ic, label, num, sub) {
    return h("a", { class: "stat", href }, h("span", { class: "stat-top" }, label, icon(ic)), h("span", { class: "stat-num", text: nf.format(num) }), h("span", { class: "stat-sub", text: sub }));
  }

  function renderSeason() {
    const el = $("#ov-season");
    const o = S.ov;
    const st = parseStart(o.settings.start);
    const words = st ? st.words : o.start;
    const running = st ? st.t <= Date.now() : o.countdown === "Season 2 läuft";
    once(el, JSON.stringify([words, running]), () => [
      h("div", { class: "season-top" }, h("p", { class: "eyebrow" }, "Season 2"), h("a", { class: "btn sm ghost", href: "#settings" }, icon("edit"), "Ändern")),
      h("p", { class: "season-when" }, words || "Noch kein Start eingetragen", h("small", { text: running ? "Gestartet, Uhrzeit in Berlin" : "Start, Uhrzeit in Berlin" })),
      running
        ? h("p", { class: "season-running" }, h("span", { class: "live-dot" }), "Season 2 läuft")
        : st
          ? h("div", { class: "cd", role: "timer", "aria-label": o.countdown }, ...["Tage", "Stunden", "Minuten", "Sekunden"].map((l) => h("div", { class: "cd-tile" }, h("span", { class: "cd-num" }, h("span", { text: "0" })), h("span", { class: "cd-lab", text: l }))))
          : h("p", { class: "season-running", text: o.countdown }),
    ]);
    el._t = st ? st.t : null;
    tickSeason();
  }

  function tickSeason() {
    const el = $("#ov-season");
    if (!el._t) return;
    const nums = $$(".cd-num", el);
    if (!nums.length) return;
    let s = Math.max(0, Math.floor((el._t - Date.now()) / 1000));
    if (s === 0 && S.ov) {
      el._sig = null;
      return renderSeason();
    }
    const parts = [Math.floor(s / 86400), Math.floor((s % 86400) / 3600), Math.floor((s % 3600) / 60), s % 60];
    parts.forEach((v, i) => {
      const txt = i === 0 ? String(v) : String(v).padStart(2, "0");
      const span = nums[i].firstElementChild;
      if (span.textContent === txt) return;
      const fresh = h("span", { class: span.textContent === "0" && !nums[i]._init ? "" : "roll", text: txt });
      nums[i]._init = true;
      span.replaceWith(fresh);
    });
  }
  setInterval(() => {
    if (S.view === "overview" && !document.hidden) tickSeason();
  }, 1000);

  function renderBot(enabled) {
    const o = S.ov;
    const b = o.bot;
    const el = $("#ov-bot");
    const sig = JSON.stringify([o.configured, o.callback, b, o.chat.connected, o.chat.detail, o.chat.joined.length, enabled]);
    once(el, sig, () => {
      const head = h("p", { class: "eyebrow", text: "Bot-Konto" });
      if (!o.configured) {
        return [
          head,
          h("div", { class: "bot-id" }, h("span", { class: "bot-avatar off" }, icon("alert")), h("div", null, h("p", { class: "bot-name", text: "Twitch-Anwendung fehlt" }), h("p", { class: "bot-state bad" }, h("span", { class: "conn-dot" }), "Nicht eingerichtet"))),
          h("p", { class: "bot-info" }, "Trage ", h("b", { text: "TTV_CLIENT_ID" }), " und ", h("b", { text: "TTV_CLIENT_SECRET" }), " in twitch.env ein und starte den Dienst neu. Die Redirect-URL der Anwendung:"),
          copyLine(o.callback),
        ];
      }
      if (!b) {
        return [
          head,
          h("div", { class: "bot-id" }, h("span", { class: "bot-avatar off" }, icon("bot")), h("div", null, h("p", { class: "bot-name", text: "Noch kein Bot-Konto" }), h("p", { class: "bot-state bad" }, h("span", { class: "conn-dot" }), "Nicht verbunden"))),
          h("p", { class: "bot-info", text: "Melde dich mit dem Twitch-Konto des Bots an, nicht mit deinem eigenen. Bist du bei Twitch gerade als du selbst angemeldet, melde dich dort zuerst ab oder nimm ein privates Fenster." }),
          h("div", { class: "bot-actions" }, h("a", { class: "btn twitch", href: "/auth/bot" }, icon("twitch"), "Bot mit Twitch verbinden")),
        ];
      }
      const st = o.chat.connected ? ["ok", "Verbunden"] : o.chat.detail ? ["bad", "Getrennt"] : ["wait", "Verbindet"];
      const info = o.chat.connected
        ? ["Sitzt in ", h("b", { text: o.chat.joined.length + " von " + enabled }), " Chats."]
        : [o.chat.detail ? cap(o.chat.detail) + "." : "Der Bot verbindet sich gerade mit dem Chat."];
      return [
        head,
        h("div", { class: "bot-id" }, h("span", { class: "bot-avatar" }, icon("bot")), h("div", { style: { "min-width": "0" } }, h("p", { class: "bot-name", text: b.name || b.login }), h("p", { class: "bot-state " + st[0] }, h("span", { class: "conn-dot" }), st[1]))),
        h("p", { class: "bot-info" }, ...info, h("br"), valid(b.linked) ? "Verbunden am " + dateLong(b.linked) + (b.by ? " von " + b.by + "." : ".") : ""),
        h(
          "div",
          { class: "bot-actions" },
          h("a", { class: "btn ghost sm", href: "/auth/bot" }, icon("refresh"), "Anderes Konto"),
          h("button", { class: "btn danger-ghost sm", type: "button", onclick: dropBot }, icon("x"), "Trennen")
        ),
      ];
    });
  }

  async function dropBot(e) {
    const btn = e.currentTarget;
    const ok = await ask({ title: "Bot-Konto trennen?", text: "Der Bot verlässt alle Chats und antwortet nicht mehr, bis du wieder ein Konto verbindest.", ok: "Trennen", danger: true });
    if (!ok) return;
    busy(btn, true);
    try {
      await api("DELETE", "/api/bot");
      toast("Bot-Konto getrennt.");
      expectState();
    } catch (err) {
      toast(err.message, "err");
    } finally {
      busy(btn, false);
    }
  }

  function copyLine(text) {
    const btn = h("button", { class: "icon-btn", type: "button", title: "Kopieren", "aria-label": "Kopieren" }, icon("copy"));
    btn.onclick = async () => {
      try {
        await navigator.clipboard.writeText(text);
        btn.replaceChildren(icon("check"));
        btn.classList.add("ok");
        toast("Kopiert.");
        setTimeout(() => {
          btn.replaceChildren(icon("copy"));
          btn.classList.remove("ok");
        }, 1600);
      } catch {
        toast("Kopieren ging nicht. Markiere den Text von Hand.", "err");
      }
    };
    return h("div", { class: "copy-line" }, h("span", { text, title: text }), btn);
  }

  function makeStream(c) {
    const a = h("a", { class: "stream", href: "https://www.twitch.tv/" + c.login, target: "_blank", rel: "noopener" });
    const thumb = h("div", { class: "thumb" });
    thumb.style.setProperty("--th", `hsl(${hue(c.login)} 35% 26%)`);
    const fallback = h("div", { class: "thumb-fallback" }, avatar(c.name || c.login, c.image));
    const viewers = h("span", { class: "badge-glass" }, icon("eye"), h("span"));
    const time = h("span", { class: "badge-glass" });
    thumb.append(fallback, h("div", { class: "thumb-badges" }, h("span", { class: "badge-live" }, h("span", { class: "live-dot" }), "LIVE"), viewers), h("div", { class: "thumb-time" }, time));
    const name = h("span");
    const title = h("p", { class: "stream-title" });
    const game = h("p", { class: "stream-game" });
    const av = avatar(c.name || c.login, c.image, "sm");
    a.append(thumb, h("div", { class: "stream-body" }, av, h("div", { class: "stream-text" }, h("p", { class: "stream-name" }, name), title, game)));
    a._p = { thumb, viewers: viewers.lastChild, time, name, title, game, av, fallback };
    return a;
  }

  function patchStream(a, c) {
    const p = a._p;
    const l = c.live;
    setText(p.viewers, nf.format(l.viewer_count || 0));
    setText(p.time, valid(l.started_at) ? "seit " + uptime(l.started_at) : "live");
    setText(p.name, l.user_name || c.name || c.login);
    setText(p.title, l.title || "Ohne Titel");
    setText(p.game, l.game_name || "");
    setAvatar(p.av, c.name || c.login, c.image);
    setAvatar(p.fallback.firstChild, c.name || c.login, c.image);
    const src = thumbURL(l.thumbnail_url);
    if (p.src !== src) {
      p.src = src;
      const old = p.img;
      if (src) {
        // the new picture fades in over the old one, which goes once it is covered
        const img = picture(src, () => {
          if (p.img === img) p.img = null;
          old?.remove();
        });
        img.className = "shot";
        img.onload = () => {
          img.classList.add("ok");
          if (old && old !== img) setTimeout(() => old.remove(), 450);
        };
        p.img = img;
        p.thumb.insertBefore(img, p.fallback.nextSibling);
      }
    }
  }

  // ---- chat ----

  const KINDS_SHOWN = new Set(["chat", "action", "notice", "event"]);
  const lineKey = (m) => (m.id || "") + "|" + m.time + "|" + m.channel + "|" + (m.user || m.login || "") + "|" + m.text;
  let pending = [];
  let flushQueued = false;

  function addLines(msgs, live) {
    const fresh = [];
    for (const m of msgs) {
      if (!m || !KINDS_SHOWN.has(m.kind || "chat")) continue;
      const k = lineKey(m);
      if (S.seen.has(k)) continue;
      S.seen.add(k);
      m._k = k;
      m._t = Date.parse(m.time) || Date.now();
      fresh.push(m);
    }
    if (!fresh.length) return;
    S.lines.push(...fresh);
    S.lines.sort((a, b) => a._t - b._t);
    if (S.lines.length > 700) {
      S.lines = S.lines.slice(-600);
      S.seen = new Set(S.lines.map((m) => m._k));
    }
    if (live && S.view !== "chat") {
      S.unread += fresh.filter((m) => m.kind !== "notice").length;
      renderBadges();
    }
    pending.push(...fresh);
    if (!flushQueued) {
      flushQueued = true;
      requestAnimationFrame(flush);
    }
  }

  function flush() {
    flushQueued = false;
    const batch = pending;
    pending = [];
    if (!batch.length) return;
    if (S.ov) renderMini();
    if (S.paused) {
      S.queue.push(...batch);
      renderPaused();
      return;
    }
    appendToFeed(batch);
  }

  // historyLoaded takes the placeholder lines away when the history brought nothing new
  function historyLoaded() {
    const feed = $("#feed");
    if (!feed._skeleton || pending.length) return;
    feed.replaceChildren();
    feed._skeleton = false;
    updateFeedEmpty();
  }

  function appendToFeed(batch) {
    const feed = $("#feed");
    if (feed._skeleton) {
      feed.replaceChildren();
      feed._skeleton = false;
    }
    const animate = batch.length <= 12 && !reduced.matches && S.view === "chat";
    let visible = 0;
    for (const m of batch) {
      const li = lineEl(m);
      if (animate) li.classList.add("new");
      const last = feed.lastElementChild;
      if (last && last._t > m._t) {
        let at = last;
        while (at.previousElementSibling && at.previousElementSibling._t > m._t) at = at.previousElementSibling;
        feed.insertBefore(li, at);
      } else feed.append(li);
      if (!li.hidden) visible++;
    }
    while (feed.children.length > 600) feed.firstElementChild.remove();
    updateFeedEmpty();
    if (S.atBottom) toBottom(false);
    else if (visible) {
      S.unseen += visible;
      renderPill();
    }
  }

  function lineEl(m) {
    const kind = m.kind || "chat";
    const li = h("li", { class: "msg " + kind + (m.self ? " self" : "") });
    li._t = m._t;
    li._ch = m.channel;
    li._k = m._k;
    const t = h("time", { text: clock(m._t), title: new Date(m._t).toLocaleString("de-DE") });
    const ch = h("button", { class: "ch", type: "button", title: "Nur " + displayName(m.channel) + " zeigen", text: displayName(m.channel) });
    ch.style.setProperty("--c", channelColor(m.channel));
    ch.onclick = () => setFilter(m.channel);
    const body = h("div", { class: "msg-body" });
    if (kind === "chat" || kind === "action") {
      if (m.self) body.append(h("span", { class: "b bot", text: "Bot" }));
      if (m.broadcaster) body.append(h("span", { class: "b streamer", text: "Streamer" }));
      else if (m.mod) body.append(h("span", { class: "b mod", text: "Mod" }));
      if (m.vip) body.append(h("span", { class: "b vip", text: "VIP" }));
      const col = m.self ? "var(--brass)" : userColor(m.color, m.login || m.user);
      const u = h("span", { class: "u", text: (m.self && S.ov?.bot?.name) || m.user || m.login || "?" });
      u.style.color = col;
      body.append(u);
      if (kind === "action") li.style.setProperty("--uc", col);
    }
    body.append(linkify(m.text || ""));
    li.append(t, ch, body);
    if (S.filter && m.channel !== S.filter) li.hidden = true;
    return li;
  }

  function linkify(text) {
    const span = h("span", { class: "msg-text" });
    const parts = String(text).split(/(https?:\/\/[^\s<>"]+)/g);
    parts.forEach((p, i) => {
      if (i % 2) span.append(h("a", { href: p, target: "_blank", rel: "noopener noreferrer", text: p }));
      else if (p) span.append(p);
    });
    return span;
  }

  function toBottom(smooth) {
    const feed = $("#feed");
    if (smooth && !reduced.matches) feed.scrollTo({ top: feed.scrollHeight, behavior: "smooth" });
    else feed.scrollTop = feed.scrollHeight;
    S.atBottom = true;
    S.unseen = 0;
    renderPill();
  }

  function renderPill() {
    const pill = $("#pill-new");
    const show = S.unseen > 0 && !S.atBottom && !S.paused;
    if (pill.hidden === !show && !show) return;
    pill.hidden = !show;
    if (show) setText($("span", pill), S.unseen === 1 ? "1 neue Nachricht" : nf.format(S.unseen) + " neue Nachrichten");
  }

  function renderPaused() {
    const bar = $("#paused-bar");
    bar.hidden = !S.paused;
    const btn = $("#chat-pause");
    setAttr(btn, "aria-pressed", S.paused ? "true" : "false");
    $("use", btn).setAttribute("href", S.paused ? "#i-play" : "#i-pause");
    setText($("span", btn), S.paused ? "Fortsetzen" : "Anhalten");
    if (S.paused) {
      const n = S.queue.filter((m) => !S.filter || m.channel === S.filter).length;
      setText($(".paused-text", bar), n ? "Angehalten, " + (n === 1 ? "1 neue Nachricht" : nf.format(n) + " neue Nachrichten") + " warten" : "Angehalten. Neue Nachrichten warten, bis du fortsetzt.");
    }
    renderPill();
  }

  function setPaused(on) {
    S.paused = on;
    if (!on && S.queue.length) {
      const q = S.queue;
      S.queue = [];
      S.atBottom = true;
      appendToFeed(q);
    }
    renderPaused();
    if (!on) toBottom(true);
  }

  function setFilter(login) {
    S.filter = login || "";
    const feed = $("#feed");
    flag(feed, "filtered", !!S.filter);
    for (const li of feed.children) li.hidden = !!S.filter && li._ch !== S.filter;
    renderChatChrome();
    updateFeedEmpty();
    renderPaused();
    if (S.filter && S.ov?.chat.joined.includes(S.filter)) {
      $("#say-channel").value = S.filter;
      renderComposerHint();
    }
    requestAnimationFrame(() => toBottom(false));
  }

  function updateFeedEmpty() {
    const feed = $("#feed");
    const any = S.filter ? [...feed.children].some((li) => !li.hidden) : feed.children.length > 0;
    const box = $("#feed-empty");
    if (!S.ov) {
      box.hidden = true;
      return;
    }
    box.hidden = any;
    if (!any) {
      setText($(".empty-title", box), S.filter ? "In " + displayName(S.filter) + " ist es noch still" : "Noch ist es still");
      setText($(".empty-text", box), S.filter ? "Hier erscheinen die Nachrichten aus diesem Chat, sobald jemand schreibt." : S.ov.chat.joined.length ? "Sobald in einem der Chats etwas geschrieben wird, steht es hier." : "Der Bot sitzt gerade in keinem Chat. Sobald er drin ist, siehst du hier alles, was geschrieben wird.");
    }
  }

  function renderChatChrome() {
    const o = S.ov;
    if (!o) return;
    const live = new Set(o.channels.filter((c) => c.live).map((c) => c.login));
    const chans = [{ login: "", name: "Alle Kanäle" }, ...o.channels.map((c) => ({ login: c.login, name: c.name || c.login }))];
    if (S.filter && !chans.some((c) => c.login === S.filter)) chans.push({ login: S.filter, name: S.filter });
    keyed(
      $("#chat-filter"),
      chans,
      (c) => c.login || "*",
      (c) => {
        const b = h("button", { class: "chip", type: "button" });
        if (c.login) {
          const d = h("span", { class: "dot" });
          d.style.setProperty("--c", channelColor(c.login));
          b.append(d);
        }
        b._n = h("span");
        b._live = h("span", { class: "live-dot", title: "live" });
        b.append(b._n, b._live);
        b.onclick = () => setFilter(c.login === S.filter ? "" : c.login);
        return b;
      },
      (b, c) => {
        setText(b._n, c.name);
        b._live.hidden = !live.has(c.login);
        setAttr(b, "aria-pressed", (c.login || "") === S.filter ? "true" : "false");
      }
    );
    const n = o.chat.joined.length;
    setText($("#chat-sub"), o.chat.connected ? "Alle Chats in einem Feed. Der Bot sitzt in " + plural(n, "Chat", "Chats") + "." : "Alle Chats in einem Feed. Der Bot ist gerade nicht mit dem Chat verbunden.");

    const sel = $("#say-channel");
    const joined = [...o.chat.joined].sort();
    const sig = joined.join(",") + "|" + joined.map(displayName).join(",");
    if (sel._sig !== sig) {
      sel._sig = sig;
      const cur = sel.value;
      sel.replaceChildren(...(joined.length ? joined.map((l) => h("option", { value: l, text: displayName(l) })) : [h("option", { value: "", text: "Kein Kanal" })]));
      if (joined.includes(cur)) sel.value = cur;
      else if (joined.includes(S.filter)) sel.value = S.filter;
    }
    renderComposerHint();
    updateFeedEmpty();
  }

  function renderComposerHint() {
    const o = S.ov;
    const sel = $("#say-channel");
    const input = $("#say-text");
    const form = $("#say");
    let hint = "";
    if (!o.bot) hint = "Zuerst ein Bot-Konto verbinden";
    else if (!o.chat.connected) hint = "Der Bot ist nicht mit dem Chat verbunden";
    else if (!o.chat.joined.length) hint = "Der Bot sitzt in keinem Chat";
    const off = !!hint;
    flag(form, "off", off);
    sel.disabled = off;
    input.disabled = off;
    $("button[type=submit]", form).disabled = off;
    setAttr(input, "placeholder", hint || "Als " + botName() + " in " + displayName(sel.value) + " schreiben");
  }

  function renderMini() {
    const msgs = S.lines.filter((m) => m.kind === "chat" || m.kind === "action").slice(-6);
    list(
      $("#ov-feed"),
      msgs,
      (m) => m._k,
      (m) => {
        const u = h("span", { class: "u", text: ((m.self && S.ov?.bot?.name) || m.user || m.login || "?") + ": " });
        u.style.color = m.self ? "var(--brass)" : userColor(m.color, m.login || m.user);
        return h("li", null, avatar(displayName(m.channel), channel(m.channel)?.image, "xs"), h("div", { class: "mini-text" }, h("span", { class: "mini-ch", text: displayName(m.channel) + " · " + clock(m._t) }), u, m.text));
      },
      () => {},
      () => ["e", () => h("li", { class: "mini-empty", text: "Noch keine Nachrichten. Sobald in einem der Chats etwas geschrieben wird, steht es hier." })]
    );
  }

  async function sendChat(e) {
    e.preventDefault();
    const input = $("#say-text");
    const btn = $("#say button[type=submit]");
    const text = input.value.trim();
    if (!text || btn.classList.contains("busy")) return;
    if (bytes(text) > 480) return toast("Die Nachricht ist zu lang, höchstens 480 Zeichen.", "err");
    busy(btn, true);
    try {
      await api("POST", "/api/chat", { channel: $("#say-channel").value, text });
      S.lastSent = text;
      input.value = "";
      renderSayCount();
      S.atBottom = true;
      toBottom(true);
    } catch (err) {
      toast(err.message, "err");
      restartAnim($("#say"), "shake-it");
    } finally {
      busy(btn, false);
      input.focus();
    }
  }

  function renderSayCount() {
    const n = bytes($("#say-text").value);
    const c = $("#say-count");
    c.hidden = n < 380;
    setText(c, n + "/480");
    flag(c, "warn", n > 460);
  }

  // ---- channels ----

  function renderChannels() {
    const o = S.ov;
    list(
      $("#channel-list"),
      o.channels,
      (c) => c.login,
      makeChannel,
      patchChannel,
      () => ["e", () => emptyState("channels", "Noch keine Kanäle", "Trag oben den Twitch-Namen oder den Link eines Streamers ein. Der Bot geht dann sofort in den Chat.")],
      { stagger: true }
    );
  }

  function makeChannel(c) {
    const row = h("div", { class: "row ch-row" });
    const av = avatar(c.name || c.login, c.image);
    const name = h("span", { class: "name" });
    const tags = h("span", { class: "row-tags", style: { display: "contents" } });
    const sub = h("p", { class: "row-sub" });
    const noteBtn = h("button", { class: "icon-btn", type: "button", title: "Notiz bearbeiten", "aria-label": "Notiz bearbeiten" }, icon("edit"));
    const main = h("div", { class: "row-main" }, h("p", { class: "row-title" }, name, tags), sub);
    const t1 = tickEl();
    const t2 = tickEl();
    const botSw = switchEl("Bot im Chat", (v) => api("PATCH", "/api/channels/" + encodeURIComponent(c.login), { enabled: v }), t1);
    const webSw = switchEl("Auf der Website", (v) => api("PATCH", "/api/channels/" + encodeURIComponent(c.login), { website: v }), t2);
    const del = h("button", { class: "icon-btn danger", type: "button", title: "Entfernen", "aria-label": "Kanal entfernen" }, icon("trash"));
    const side = h(
      "div",
      { class: "row-side" },
      h("div", { class: "ch-toggles" }, h("label", { class: "toggle-lab" }, botSw, "Bot im Chat", t1), h("label", { class: "toggle-lab" }, webSw, "Auf der Website", t2)),
      h("div", { class: "ch-actions" }, noteBtn, del)
    );
    row.append(av, main, side);
    noteBtn.onclick = () => editNote(row, c.login);
    del.onclick = async () => {
      const n = displayName(c.login);
      const ok = await ask({ title: n + " entfernen?", text: "Der Bot verlässt den Chat von " + n + ", und " + n + " erscheint nicht mehr auf der Website. Commands und Timer bleiben, wie sie sind.", ok: "Entfernen", danger: true });
      if (!ok) return;
      busy(del, true);
      try {
        await api("DELETE", "/api/channels/" + encodeURIComponent(c.login));
        toast(n + " entfernt.");
        expectState();
      } catch (e) {
        toast(e.message, "err");
        busy(del, false);
      }
    };
    row._p = { av, name, tags, sub, botSw, webSw, main };
    if (S.flash === c.login) {
      S.flash = null;
      setTimeout(() => restartAnim(row, "flash"), 50);
    }
    return row;
  }

  function patchChannel(row, c) {
    const p = row._p;
    setAvatar(p.av, c.name || c.login, c.image);
    flag(p.av, "live", !!c.live);
    setText(p.name, c.name || c.login);
    const tagSig = JSON.stringify([!!c.live, c.live?.viewer_count, c.enabled, c.joined, S.ov.chat.connected]);
    once(p.tags, tagSig, () => {
      const t = [];
      if (c.live) t.push(h("span", { class: "tag red" }, h("span", { class: "live-dot" }), "live · " + nf.format(c.live.viewer_count || 0)));
      if (c.enabled && c.joined) t.push(h("span", { class: "tag green", text: "im Chat" }));
      else if (c.enabled && S.ov.chat.connected) t.push(h("span", { class: "tag brass", text: "geht in den Chat" }));
      else if (!c.enabled) t.push(h("span", { class: "tag off", text: "Bot aus" }));
      return t;
    });
    if (!row._editing) {
      const sub = c.live ? [c.live.game_name, c.live.title].filter(Boolean).join(" · ") : c.note || "twitch.tv/" + c.login;
      setText(p.sub, sub);
      setAttr(p.sub, "title", sub);
    }
    p.botSw._set(c.enabled);
    p.webSw._set(c.website);
  }

  function editNote(row, login) {
    if (row._editing) return;
    const c = channel(login);
    row._editing = true;
    const p = row._p;
    const input = h("input", { value: c?.note || "", placeholder: "Notiz, zum Beispiel wofür oder von wem", "aria-label": "Notiz", maxlength: "200" });
    const save = h("button", { class: "btn primary sm", type: "submit" }, "Speichern");
    const form = h("form", { class: "ch-note-edit" }, input, save);
    p.sub.hidden = true;
    p.main.append(form);
    input.focus();
    input.select();
    const done = () => {
      row._editing = false;
      form.remove();
      p.sub.hidden = false;
      const cc = channel(login);
      if (cc) patchChannel(row, cc);
    };
    input.onkeydown = (e) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        done();
      }
    };
    form.onsubmit = async (e) => {
      e.preventDefault();
      busy(save, true);
      try {
        await api("PATCH", "/api/channels/" + encodeURIComponent(login), { note: input.value.trim() });
        if (c) c.note = input.value.trim();
        toast("Notiz gespeichert.");
        done();
        expectState();
      } catch (err) {
        toast(err.message, "err");
        busy(save, false);
      }
    };
  }

  async function addChannel(e) {
    e.preventDefault();
    const li = $("#channel-login");
    const note = $("#channel-note");
    const btn = $("#add-channel button[type=submit]");
    const login = loginFrom(li.value);
    if (!login) {
      li.focus();
      restartAnim(li, "bad");
      return toast("Trag einen Twitch-Namen oder Link ein.", "err");
    }
    if (!/^[a-z0-9_]{3,25}$/.test(login)) {
      restartAnim(li, "bad");
      return toast("„" + login + "“ ist kein Twitch-Name.", "err");
    }
    if (btn.classList.contains("busy")) return;
    busy(btn, true);
    li.classList.remove("bad");
    try {
      await api("POST", "/api/channels", { login, note: note.value.trim() });
      S.flash = login;
      li.value = note.value = "";
      toast(displayName(login) === login ? login + " hinzugefügt. Der Bot geht gleich in den Chat." : displayName(login) + " hinzugefügt.");
      expectState();
      li.focus();
    } catch (err) {
      li.classList.add("bad");
      toast(err.message, "err");
    } finally {
      busy(btn, false);
    }
  }

  // ---- editors ----

  const PLACEHOLDERS = [
    ["{user}", "wer den Command geschrieben hat"],
    ["{channel}", "der Streamer, in dessen Chat es steht"],
    ["{start}", "Start der Season, ausgeschrieben"],
    ["{countdown}", "wie lange es noch bis zum Start ist"],
    ["{discord}", "die Discord-Einladung"],
    ["{website}", "die Website"],
    ["{server}", "die Serveradresse"],
    ["{live}", "die anderen Kronwerke-Streamer, die gerade live sind"],
    ["{args}", "was nach dem Command steht"],
  ];

  function chipInput(box) {
    const input = $("input", box);
    let vals = [];
    const norm = (s) => s.trim().toLowerCase().replace(/^!+/, "").replace(/\s+/g, "");
    const changed = () => box.dispatchEvent(new Event("input", { bubbles: true }));
    const render = () =>
      keyed(
        box,
        vals,
        (v) => v,
        (v) =>
          h(
            "span",
            { class: "tag-chip" },
            "!" + v,
            h(
              "button",
              {
                type: "button",
                "aria-label": "!" + v + " entfernen",
                onclick: (e) => {
                  e.stopPropagation();
                  vals = vals.filter((x) => x !== v);
                  render();
                  changed();
                  input.focus();
                },
              },
              icon("x")
            )
          ),
        () => {},
        { grid: true }
      );
    // keyed keeps only keyed children: put the input back at the end
    const keepInput = () => {
      if (input.parentNode !== box || box.lastElementChild !== input) box.append(input);
    };
    input._key = "__input";
    const add = (s) => {
      for (const part of s.split(/[,\s]+/)) {
        const v = norm(part);
        if (v && !vals.includes(v)) vals.push(v);
      }
      render();
      keepInput();
      changed();
    };
    input.addEventListener("keydown", (e) => {
      if ((e.key === "Enter" || e.key === "," || e.key === " ") && input.value.trim()) {
        e.preventDefault();
        add(input.value);
        input.value = "";
      } else if (e.key === "Backspace" && !input.value && vals.length) {
        vals.pop();
        render();
        keepInput();
        changed();
      }
    });
    input.addEventListener("blur", () => {
      if (input.value.trim()) {
        add(input.value);
        input.value = "";
      }
    });
    box.addEventListener("click", (e) => {
      if (e.target === box) input.focus();
    });
    return {
      get: () => {
        const extra = norm(input.value);
        return extra && !vals.includes(extra) ? [...vals, extra] : [...vals];
      },
      set(v) {
        vals = [...(v || [])];
        input.value = "";
        render();
        keepInput();
      },
    };
  }

  function chanPicker(box) {
    let sel = new Set();
    let opts = [];
    const render = () => {
      const all = [...new Set([...opts, ...sel])];
      keyed(
        box,
        [{ login: "" }, ...all.map((l) => ({ login: l }))],
        (c) => c.login || "*",
        (c) => {
          const b = h("button", { class: "chip", type: "button" });
          if (c.login) {
            const d = h("span", { class: "dot" });
            d.style.setProperty("--c", channelColor(c.login));
            b.append(d);
          }
          b._n = h("span");
          b.append(b._n);
          b.onclick = () => {
            if (!c.login) sel.clear();
            else if (sel.has(c.login)) sel.delete(c.login);
            else sel.add(c.login);
            render();
            box.dispatchEvent(new Event("input", { bubbles: true }));
          };
          return b;
        },
        (b, c) => {
          setText(b._n, c.login ? displayName(c.login) : "Alle Kanäle");
          setAttr(b, "aria-pressed", (c.login ? sel.has(c.login) : sel.size === 0) ? "true" : "false");
        },
        { grid: true }
      );
    };
    return {
      get: () => [...sel].sort(),
      set(v) {
        sel = new Set(v || []);
        render();
      },
      options(list) {
        const sig = list.join(",") + "|" + list.map(displayName).join(",");
        if (box._osig === sig) return;
        box._osig = sig;
        opts = list;
        render();
      },
    };
  }

  function makeEditor(cfg) {
    const f = cfg.form;
    const E = { key: null, snap: "", open: false };
    const chipsBox = $("[data-chips]", f);
    const chips = chipsBox ? chipInput(chipsBox) : null;
    const pick = chanPicker($("[data-chan-pick]", f));
    const ta = $("textarea", f);
    const count = $("[data-count]", f);
    const bubble = $(".bubble", f);
    const pvText = $("[data-preview-text]", f);
    const pvWhere = $("[data-preview-where]", f);
    const ctx = { chips, pick };

    for (const box of $$("[data-ph-for]", f)) {
      for (const [tok, what] of PLACEHOLDERS) {
        if (cfg.noArgs && (tok === "{user}" || tok === "{args}")) continue;
        box.append(
          h("button", {
            class: "ph-chip",
            type: "button",
            title: what,
            text: tok,
            onclick: () => {
              ta.focus();
              ta.setRangeText(tok, ta.selectionStart, ta.selectionEnd, "end");
              ta.dispatchEvent(new Event("input", { bubbles: true }));
            },
          })
        );
      }
    }

    const read = () => cfg.read(f, ctx);
    const isDirty = () => E.open && JSON.stringify(read()) !== E.snap;

    function updCount() {
      const n = bytes(ta.value);
      setText(count, n + "/480");
      flag(count, "warn", n > 440);
    }

    let pvT = 0;
    let pvSeq = 0;
    function preview(now) {
      clearTimeout(pvT);
      const text = ta.value;
      const ch = pick.get()[0] || S.ov?.channels[0]?.login || "";
      for (const el of $$("[data-bot-name]", f)) setText(el, botName());
      if (!text.trim()) {
        pvSeq++;
        bubble.classList.add("empty");
        bubble.classList.remove("wait");
        setText(pvText, "Hier siehst du, was der Bot schreibt.");
        setText(pvWhere, "");
        return;
      }
      bubble.classList.remove("empty");
      bubble.classList.add("wait");
      pvT = setTimeout(
        async () => {
          const my = ++pvSeq;
          try {
            const r = await api("POST", "/api/preview", { text, channel: ch });
            if (my !== pvSeq) return;
            setText(pvText, r.text);
            setText(pvWhere, ch ? "in " + displayName(ch) + (cfg.noArgs ? "" : ", für Zuschauer") : "");
          } catch (e) {
            if (my !== pvSeq) return;
            setText(pvText, e.message);
          }
          bubble.classList.remove("wait");
        },
        now ? 0 : 260
      );
    }

    function fill(item) {
      pick.options(S.ov.channels.map((c) => c.login));
      cfg.fill(f, item, ctx);
      updCount();
      preview(true);
    }

    function errors(errs) {
      for (const el of $$("[data-err]", f)) {
        const msg = errs[el.dataset.err] || "";
        setText(el, msg);
        const input = f.elements[el.dataset.err];
        const target = input?.closest(".affix") || input;
        if (target) flag(target, "bad", !!msg);
      }
      const fe = $("[data-form-err]", f);
      fe.hidden = !errs._form;
      setText(fe, errs._form || "");
      if (errs._form) restartAnim(fe, "form-err");
    }

    function refreshDirty() {
      $("[data-dirty]", f).hidden = !isDirty();
    }

    function titles() {
      const [eyebrow, title] = cfg.titles(E.key ? cfg.find(E.key) || read() : null);
      setText($("[data-eyebrow]", f), eyebrow);
      setText($("[data-title]", f), title);
      $("[data-delete]", f).hidden = !E.key;
    }

    const discard = () => ask({ title: "Änderungen verwerfen?", text: cfg.discardText, ok: "Verwerfen", danger: true });

    E.openItem = async (key) => {
      if (E.open && key != null && key === E.key) return;
      if (isDirty() && !(await discard())) return;
      const item = key != null ? cfg.find(key) : null;
      E.key = item ? key : null;
      errors({});
      $("[data-gone]", f).hidden = true;
      fill(cfg.norm(item || cfg.blank()));
      E.snap = JSON.stringify(read());
      E.open = true;
      titles();
      refreshDirty();
      cfg.empty.hidden = true;
      f.hidden = false;
      if (!reduced.matches) restartAnim(f, "editor");
      $(".ed-body", f).scrollTop = 0;
      cfg.changed();
      if (!E.key && !phone.matches) f.elements.name.focus();
      else if (!phone.matches) $("[data-close]", f).focus({ preventScroll: true });
    };

    E.close = async (force) => {
      if (!E.open) return;
      if (!force && isDirty() && !(await discard())) return;
      E.open = false;
      E.key = null;
      if (phone.matches && !reduced.matches) {
        f.classList.add("closing");
        await sleep(250);
        f.classList.remove("closing");
      }
      f.hidden = true;
      cfg.empty.hidden = false;
      cfg.changed();
    };

    E.sync = () => {
      if (!E.open || !S.ov) return;
      pick.options(S.ov.channels.map((c) => c.login));
      const item = E.key != null ? cfg.find(E.key) : null;
      $("[data-gone]", f).hidden = !(E.key != null && !item);
      if (item && !isDirty() && !f.contains(document.activeElement)) {
        const fresh = JSON.stringify(cfg.norm(item));
        if (fresh !== E.snap) {
          fill(cfg.norm(item));
          E.snap = JSON.stringify(read());
        }
      }
      titles();
    };

    E.dirty = isDirty;

    f.addEventListener("input", () => {
      updCount();
      refreshDirty();
      preview(false);
      if (!$("[data-form-err]", f).hidden) $("[data-form-err]", f).hidden = true;
    });
    f.addEventListener("input", (e) => {
      const n = e.target.name;
      if (n && $(`[data-err="${n}"]`, f)?.textContent) {
        const errs = cfg.validate(read());
        setText($(`[data-err="${n}"]`, f), errs[n] || "");
        const t = f.elements[n]?.closest(".affix") || f.elements[n];
        if (t) flag(t, "bad", !!errs[n]);
      }
    });
    $("[data-chan-pick]", f).addEventListener("input", () => preview(true));
    f.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        f.requestSubmit();
      }
    });
    for (const b of $$("[data-close]", f)) b.addEventListener("click", () => E.close());

    f.addEventListener("submit", async (e) => {
      e.preventDefault();
      const btn = $("[data-save]", f);
      if (btn.classList.contains("busy")) return;
      const v = read();
      const errs = cfg.validate(v);
      errors(errs);
      if (Object.keys(errs).length) {
        const first = Object.keys(errs).find((k) => f.elements[k]);
        if (first) f.elements[first].focus();
        return;
      }
      busy(btn, true);
      try {
        const saved = await cfg.save(E.key, v);
        const wasNew = !E.key;
        E.key = cfg.keyOf(saved);
        S.flash = wasNew ? E.key : null;
        fill(cfg.norm(saved));
        E.snap = JSON.stringify(read());
        $("[data-gone]", f).hidden = true;
        refreshDirty();
        titles();
        toast(cfg.savedText(saved, wasNew));
        expectState();
        cfg.changed();
      } catch (err) {
        errors({ _form: err.message });
      } finally {
        busy(btn, false);
      }
    });

    $("[data-delete]", f).addEventListener("click", async (e) => {
      if (!E.key) return;
      const btn = e.currentTarget;
      const item = cfg.find(E.key) || read();
      const [title, text] = cfg.deleteText(item);
      if (!(await ask({ title, text, ok: "Löschen", danger: true }))) return;
      busy(btn, true);
      try {
        await cfg.remove(E.key);
        toast(cfg.deletedText(item));
        await E.close(true);
        expectState();
      } catch (err) {
        toast(err.message, "err");
      } finally {
        busy(btn, false);
      }
    });

    return E;
  }

  const CmdEd = makeEditor({
    form: $("#cmd-form"),
    get empty() {
      return $("#cmd-empty");
    },
    blank: () => ({ name: "", aliases: [], response: "", cooldown: 30, enabled: true, mod_only: false, channels: [] }),
    find: (k) => S.ov?.commands.find((c) => c.name === k),
    keyOf: (c) => c.name,
    norm: (c) => ({ name: c.name || "", aliases: [...(c.aliases || [])], response: c.response || "", cooldown: +c.cooldown || 0, enabled: !!c.enabled, mod_only: !!c.mod_only, channels: [...(c.channels || [])].sort() }),
    fill(f, c, { chips, pick }) {
      f.elements.name.value = c.name;
      chips.set(c.aliases);
      f.elements.response.value = c.response;
      f.elements.cooldown.value = c.cooldown;
      f.elements.enabled.checked = c.enabled;
      f.elements.mod_only.checked = c.mod_only;
      pick.set(c.channels);
    },
    read: (f, { chips, pick }) => ({
      name: f.elements.name.value.trim().toLowerCase().replace(/^!+/, ""),
      aliases: chips.get(),
      response: f.elements.response.value,
      cooldown: Math.max(0, Math.min(3600, parseInt(f.elements.cooldown.value, 10) || 0)),
      enabled: f.elements.enabled.checked,
      mod_only: f.elements.mod_only.checked,
      channels: pick.get(),
    }),
    validate(v) {
      const e = {};
      if (!v.name) e.name = "Wie heißt der Command?";
      else if (!/^[a-z0-9_äöü]{1,30}$/.test(v.name)) e.name = "Nur Buchstaben, Zahlen und Unterstrich, ohne Leerzeichen.";
      else if (CmdEd.key !== v.name && S.ov.commands.some((c) => c.name !== CmdEd.key && (c.name === v.name || c.aliases.includes(v.name)))) e.name = "!" + v.name + " gibt es schon.";
      if (!v.response.trim()) e.response = "Was soll der Bot antworten?";
      else if (bytes(v.response.trim()) > 480) e.response = "Zu lang: höchstens 480 Zeichen, Umlaute zählen doppelt.";
      return e;
    },
    save: (key, v) => api("PUT", "/api/commands/" + encodeURIComponent(key || v.name), { ...v, response: v.response.trim() }),
    remove: (key) => api("DELETE", "/api/commands/" + encodeURIComponent(key)),
    titles: (c) => (c ? ["Command bearbeiten", "!" + c.name] : ["Neu", "Neuer Command"]),
    savedText: (c, isNew) => "!" + c.name + (isNew ? " angelegt." : " gespeichert.") + " Gilt ab sofort.",
    deleteText: (c) => ["!" + c.name + " löschen?", "Der Bot reagiert danach nicht mehr auf !" + c.name + (c.aliases?.length ? " und seine weiteren Namen" : "") + "."],
    deletedText: (c) => "!" + c.name + " gelöscht.",
    discardText: "Deine Änderungen an diesem Command sind noch nicht gespeichert.",
    changed: () => renderCommands(),
  });

  const TmrEd = makeEditor({
    form: $("#tmr-form"),
    noArgs: true,
    get empty() {
      return $("#tmr-empty");
    },
    blank: () => ({ name: "", text: "", minutes: 30, min_lines: 15, enabled: true, live_only: true, channels: [] }),
    find: (k) => S.ov?.timers.find((t) => t.id === k),
    keyOf: (t) => t.id,
    norm: (t) => ({ name: t.name || "", text: t.text || "", minutes: +t.minutes || 0, min_lines: +t.min_lines || 0, enabled: !!t.enabled, live_only: !!t.live_only, channels: [...(t.channels || [])].sort() }),
    fill(f, t, { pick }) {
      f.elements.name.value = t.name;
      f.elements.text.value = t.text;
      f.elements.minutes.value = t.minutes;
      f.elements.min_lines.value = t.min_lines;
      f.elements.enabled.checked = t.enabled;
      f.elements.live_only.checked = t.live_only;
      pick.set(t.channels);
    },
    read: (f, { pick }) => ({
      name: f.elements.name.value.trim(),
      text: f.elements.text.value,
      minutes: parseInt(f.elements.minutes.value, 10) || 0,
      min_lines: Math.max(0, parseInt(f.elements.min_lines.value, 10) || 0),
      enabled: f.elements.enabled.checked,
      live_only: f.elements.live_only.checked,
      channels: pick.get(),
    }),
    validate(v) {
      const e = {};
      if (!v.name) e.name = "Gib dem Timer einen Namen, damit du ihn wiederfindest.";
      if (!v.text.trim()) e.text = "Was soll der Bot schreiben?";
      else if (bytes(v.text.trim()) > 480) e.text = "Zu lang: höchstens 480 Zeichen, Umlaute zählen doppelt.";
      if (v.minutes < 5) e.minutes = "Mindestens 5 Minuten, öfter schreibt ein Timer nicht.";
      return e;
    },
    save: (key, v) => api("PUT", "/api/timers/" + encodeURIComponent(key || "new"), { ...v, text: v.text.trim() }),
    remove: (key) => api("DELETE", "/api/timers/" + encodeURIComponent(key)),
    titles: (t) => (t ? ["Timer bearbeiten", t.name || "Timer"] : ["Neu", "Neuer Timer"]),
    savedText: (t, isNew) => "Timer „" + (t.name || "Timer") + "“ " + (isNew ? "angelegt." : "gespeichert.") + " Gilt ab sofort.",
    deleteText: (t) => ["Timer löschen?", "„" + (t.name || "Timer") + "“ wird nicht mehr in die Chats geschrieben."],
    deletedText: (t) => "Timer „" + (t.name || "Timer") + "“ gelöscht.",
    discardText: "Deine Änderungen an diesem Timer sind noch nicht gespeichert.",
    changed: () => renderTimers(),
  });

  function chanLabel(chs) {
    if (!chs?.length) return null;
    const names = chs.map(displayName);
    return names.length > 2 ? "nur in " + names.slice(0, 2).join(", ") + " und " + (names.length - 2) + " weiteren" : "nur in " + names.join(" und ");
  }

  function renderCommands() {
    if (!S.ov) return;
    const q = ($("#cmd-search").value || "").trim().toLowerCase().replace(/^!/, "");
    const items = S.ov.commands.filter((c) => !q || c.name.includes(q) || c.aliases.some((a) => a.includes(q)) || c.response.toLowerCase().includes(q));
    list(
      $("#cmd-list"),
      items,
      (c) => c.name,
      (c) => {
        const row = h("div", { class: "row click", tabindex: "0", role: "button" });
        const title = h("p", { class: "row-title" });
        const text = h("p", { class: "row-text" });
        const meta = h("div", { class: "row-meta" });
        const sw = switchEl("Aktiv", (v) => {
          const cur = S.ov.commands.find((x) => x.name === c.name) || c;
          return api("PUT", "/api/commands/" + encodeURIComponent(c.name), { ...cur, enabled: v });
        });
        row.append(h("div", { class: "row-main" }, title, text, meta), h("div", { class: "row-side" }, sw));
        sw.addEventListener("click", (e) => e.stopPropagation());
        row.onclick = () => CmdEd.openItem(c.name);
        row.onkeydown = (e) => {
          if ((e.key === "Enter" || e.key === " ") && e.target === row) {
            e.preventDefault();
            CmdEd.openItem(c.name);
          }
        };
        row._p = { title, text, meta, sw };
        if (S.flash === c.name) {
          S.flash = null;
          setTimeout(() => restartAnim(row, "flash"), 50);
        }
        return row;
      },
      (row, c) => {
        const p = row._p;
        flag(row, "sel", CmdEd.open && CmdEd.key === c.name);
        flag(row, "off", !c.enabled);
        once(p.title, JSON.stringify([c.name, c.aliases]), () => [h("span", { class: "name", text: "!" + c.name }), ...c.aliases.map((a) => h("span", { class: "tag mono", text: "!" + a }))]);
        setText(p.text, c.response);
        once(p.meta, JSON.stringify([c.cooldown, c.mod_only, c.enabled, c.channels.map(displayName), c.uses]), () => [
          !c.enabled && h("span", { class: "tag off", text: "aus" }),
          c.mod_only && h("span", { class: "tag teal" }, icon("shield"), "nur Mods"),
          h("span", { class: "tag", text: c.cooldown ? c.cooldown + " s Abklingzeit" : "ohne Abklingzeit" }),
          c.channels.length && h("span", { class: "tag brass", text: chanLabel(c.channels) }),
          c.uses > 0 && h("span", { class: "uses", text: plural(c.uses, "Aufruf", "Aufrufe") }),
        ]);
        setAttr(row, "aria-label", "!" + c.name + " bearbeiten");
        p.sw._set(c.enabled);
      },
      () =>
        q
          ? ["q" + q, () => emptyState("search", "Nichts gefunden", "Kein Command passt zu „" + q + "“.")]
          : ["e", () => emptyState("commands", "Noch keine Commands", "Ein Command ist eine feste Antwort auf !name im Chat, zum Beispiel !discord.", h("button", { class: "btn primary sm", type: "button", onclick: () => CmdEd.openItem(null) }, icon("plus"), "Ersten Command anlegen"))],
      { stagger: true }
    );
  }

  function renderTimers() {
    if (!S.ov) return;
    list(
      $("#tmr-list"),
      S.ov.timers,
      (t) => t.id,
      (t) => {
        const row = h("div", { class: "row click", tabindex: "0", role: "button" });
        const title = h("p", { class: "row-title" });
        const text = h("p", { class: "row-text" });
        const meta = h("div", { class: "row-meta" });
        const sw = switchEl("Aktiv", (v) => {
          const cur = S.ov.timers.find((x) => x.id === t.id) || t;
          return api("PUT", "/api/timers/" + encodeURIComponent(t.id), { ...cur, enabled: v });
        });
        row.append(h("div", { class: "row-main" }, title, text, meta), h("div", { class: "row-side" }, sw));
        sw.addEventListener("click", (e) => e.stopPropagation());
        row.onclick = () => TmrEd.openItem(t.id);
        row.onkeydown = (e) => {
          if ((e.key === "Enter" || e.key === " ") && e.target === row) {
            e.preventDefault();
            TmrEd.openItem(t.id);
          }
        };
        row._p = { title, text, meta, sw };
        if (S.flash === t.id) {
          S.flash = null;
          setTimeout(() => restartAnim(row, "flash"), 50);
        }
        return row;
      },
      (row, t) => {
        const p = row._p;
        flag(row, "sel", TmrEd.open && TmrEd.key === t.id);
        flag(row, "off", !t.enabled);
        once(p.title, t.name, () => h("span", { class: "name", text: t.name || "Timer" }));
        setText(p.text, t.text);
        once(p.meta, JSON.stringify([t.minutes, t.min_lines, t.live_only, t.enabled, t.channels.map(displayName)]), () => [
          !t.enabled && h("span", { class: "tag off", text: "aus" }),
          h("span", { class: "tag" }, icon("timers"), "alle " + t.minutes + " Min."),
          t.min_lines > 0 && h("span", { class: "tag", text: "ab " + plural(t.min_lines, "Zeile", "Zeilen") }),
          t.live_only && h("span", { class: "tag red", text: "nur live" }),
          t.channels.length && h("span", { class: "tag brass", text: chanLabel(t.channels) }),
        ]);
        setAttr(row, "aria-label", (t.name || "Timer") + " bearbeiten");
        p.sw._set(t.enabled);
      },
      () => ["e", () => emptyState("timers", "Noch keine Timer", "Ein Timer schreibt regelmäßig etwas in die Chats, zum Beispiel den Start der Season.", h("button", { class: "btn primary sm", type: "button", onclick: () => TmrEd.openItem(null) }, icon("plus"), "Ersten Timer anlegen"))],
      { stagger: true }
    );
  }

  // ---- settings ----

  const SETTING_KEYS = ["start", "discord", "website", "server"];
  let settingsSnap = "";

  const readSettings = () => Object.fromEntries(SETTING_KEYS.map((k) => [k, $("#settings-form").elements[k].value.trim()]));
  const settingsDirty = () => !!settingsSnap && JSON.stringify(readSettings()) !== settingsSnap;

  function fillSettings() {
    const f = $("#settings-form");
    for (const k of SETTING_KEYS) f.elements[k].value = S.ov.settings[k] || "";
    settingsSnap = JSON.stringify(readSettings());
    settingsUI();
  }

  function renderSettings() {
    const f = $("#settings-form");
    for (const el of f.elements) el.disabled = false;
    if (!settingsDirty() && !f.contains(document.activeElement)) fillSettings();
    else settingsUI();
  }

  function settingsUI() {
    const f = $("#settings-form");
    $("#settings-bar").hidden = !settingsDirty();
    const st = parseStart(f.elements.start.value);
    setText($("#set-start-hint"), st ? "Im Chat: " + st.words + ". Uhrzeit in Berlin." : "Uhrzeit in Berlin.");
  }

  async function saveSettings(e) {
    e.preventDefault();
    const f = $("#settings-form");
    const btn = $("#settings-bar .btn.primary");
    const v = readSettings();
    const err = !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(v.start) ? "Bitte Datum und Uhrzeit eintragen." : "";
    setText($('[data-err="start"]', f), err);
    flag(f.elements.start, "bad", !!err);
    if (err) return f.elements.start.focus();
    busy(btn, true);
    try {
      await api("PATCH", "/api/settings", v);
      settingsSnap = JSON.stringify(v);
      Object.assign(S.ov.settings, v);
      settingsUI();
      renderSeason();
      toast("Gespeichert. Die Platzhalter setzen ab sofort die neuen Werte ein.");
      expectState();
    } catch (err2) {
      toast(err2.message, "err");
    } finally {
      busy(btn, false);
    }
  }

  function renderSys() {
    const o = S.ov;
    const bucket = Math.floor(Date.now() / 30000);
    once($("#sys"), JSON.stringify([o.version, o.started, o.configured, o.callback, o.live_at, o.live_error, o.chat.connected, o.chat.detail, o.chat.joined.length, bucket]), () => [
      h("h2", { text: "System" }),
      h(
        "dl",
        { class: "kv" },
        h("dt", { text: "Version" }),
        h("dd", { text: o.version || "unbekannt" }),
        h("dt", { text: "Läuft seit" }),
        h("dd", { text: valid(o.started) ? dateLong(o.started) + ", " + clock(o.started) + " Uhr (" + ago(o.started) + ")" : "unbekannt" }),
        h("dt", { text: "Twitch-Anwendung" }),
        h("dd", null, o.configured ? h("span", { class: "tag green", text: "eingerichtet" }) : h("span", { class: "tag red", text: "fehlt" })),
        h("dt", { text: "Redirect-URL" }),
        h("dd", null, copyLine(o.callback || "")),
        h("dt", { text: "Chat" }),
        h("dd", { text: o.chat.connected ? "verbunden, in " + plural(o.chat.joined.length, "Kanal", "Kanälen") : cap(o.chat.detail || "nicht verbunden") }),
        h("dt", { text: "Live-Abfrage" }),
        h("dd", { text: o.live_error ? "Fehler: " + o.live_error : valid(o.live_at) ? ago(o.live_at) : "noch nicht" })
      ),
    ]);
  }

  // ---- access ----

  function renderUsers() {
    const me = S.me?.login;
    list(
      $("#user-list"),
      S.ov.users,
      (u) => u.login,
      (u) => {
        const row = h("div", { class: "row user-row" });
        const av = avatar(u.login, "");
        const title = h("p", { class: "row-title" });
        const sub = h("p", { class: "row-sub" });
        const del = h("button", { class: "btn danger-ghost sm", type: "button", "aria-label": u.login + " den Zugang entziehen", title: "Zugang entziehen" }, icon("x"), h("span", { text: "Zugang entziehen" }));
        del.onclick = async () => {
          const ok = await ask({ title: u.login + " den Zugang entziehen?", text: u.login + " kann sich danach nicht mehr anmelden. Offene Sitzungen enden sofort.", ok: "Entziehen", danger: true });
          if (!ok) return;
          busy(del, true);
          try {
            await api("DELETE", "/api/users/" + encodeURIComponent(u.login));
            toast(u.login + " hat keinen Zugang mehr.");
            expectState();
          } catch (e) {
            toast(e.message, "err");
            busy(del, false);
          }
        };
        row.append(av, h("div", { class: "row-main" }, title, sub), h("div", { class: "row-side" }, del));
        row._p = { title, sub, del };
        if (S.flash === u.login) {
          S.flash = null;
          setTimeout(() => restartAnim(row, "flash"), 50);
        }
        return row;
      },
      (row, u) => {
        const p = row._p;
        once(p.title, JSON.stringify([u.login, u.owner, !!u.id, u.login === me]), () => [
          h("span", { class: "name", text: u.login }),
          u.owner && h("span", { class: "tag brass", text: "Besitzer" }),
          u.login === me && h("span", { class: "tag teal", text: "du" }),
          !u.id && h("span", { class: "tag off", text: "noch nie angemeldet" }),
        ]);
        setText(p.sub, valid(u.last_seen) ? "Zuletzt angemeldet " + ago(u.last_seen) : valid(u.added) ? "Zugang seit " + dateLong(u.added) : "");
        p.del.hidden = !!u.owner || u.login === me;
      },
      () => ["e", () => emptyState("access", "Niemand auf der Liste", "")],
      { stagger: true }
    );
  }

  async function addUser(e) {
    e.preventDefault();
    const input = $("#user-login");
    const btn = $("#add-user button[type=submit]");
    const login = loginFrom(input.value);
    if (!/^[a-z0-9_]{3,25}$/.test(login)) {
      restartAnim(input, "bad");
      input.focus();
      return toast(login ? "„" + login + "“ ist kein Twitch-Name." : "Trag einen Twitch-Namen ein.", "err");
    }
    if (btn.classList.contains("busy")) return;
    busy(btn, true);
    try {
      await api("POST", "/api/users", { login });
      S.flash = login;
      input.value = "";
      input.classList.remove("bad");
      toast(login + " hat jetzt Zugang. Die erste Anmeldung bindet ihn an das Twitch-Konto.");
      expectState();
    } catch (err) {
      toast(err.message, "err");
    } finally {
      busy(btn, false);
    }
  }

  // ---- log ----

  let logLines = null;
  let logT = 0;
  let logBusy = false;

  async function loadLog() {
    if (logBusy) return;
    logBusy = true;
    const box = $("#log-list");
    if (!logLines && !box.children.length) box.replaceChildren(...Array.from({ length: 8 }, (_, i) => h("li", null, h("span", { class: "sk sk-text w60" }), h("span", { class: "sk sk-text " + ["w80", "w60", "w40"][i % 3] }))));
    try {
      logLines = await api("GET", "/api/log");
      renderLog();
    } catch (e) {
      if (!logLines) box.replaceChildren(h("li", { class: "log-empty" }, e.message + " ", h("button", { class: "btn ghost sm", type: "button", onclick: loadLog }, "Nochmal")));
    } finally {
      logBusy = false;
    }
  }

  function loadLogSoon() {
    clearTimeout(logT);
    logT = setTimeout(loadLog, 600);
  }
  setInterval(() => {
    if (S.view === "log" && !document.hidden && S.ov) loadLog();
  }, 20000);

  function parseLog(line, year) {
    const m = /^(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2}) (.*)$/.exec(line);
    if (!m) return { t: null, text: line };
    const now = new Date();
    let y = year;
    if (+m[1] - 1 > now.getUTCMonth() + 1) y--;
    return { t: Date.UTC(y, m[1] - 1, +m[2], +m[3], +m[4], +m[5]), text: m[6] };
  }

  function renderLog() {
    const box = $("#log-list");
    const q = $("#log-search").value.trim().toLowerCase();
    const year = new Date().getUTCFullYear();
    const counts = new Map();
    const entries = [];
    for (const line of logLines || []) {
      const n = (counts.get(line) || 0) + 1;
      counts.set(line, n);
      entries.push({ key: line + "#" + n, ...parseLog(line, year) });
    }
    entries.reverse();
    const shown = q ? entries.filter((e) => e.text.toLowerCase().includes(q)) : entries;
    list(
      box,
      shown,
      (e) => e.key,
      (e) => {
        const li = h("li");
        if (/panic|error|\berr\b|fail|refused|abgemeldet|abgelaufen|timeout|denied|invalid|\bkein\b/i.test(e.text)) li.classList.add("warn");
        else if (/^(?!chat:|live:|update|installing)[a-z0-9_]+: |signed in|connected by/.test(e.text)) li.classList.add("who");
        li.append(h("time", { text: e.t ? new Date(e.t).toLocaleString("de-DE", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" }).replace(",", "") : "" }), h("span", { class: "lt", text: e.text }));
        return li;
      },
      () => {},
      () => (q ? ["q" + q, () => h("li", { class: "log-empty", text: "Nichts gefunden für „" + q + "“." })] : ["e", () => h("li", { class: "log-empty", text: "Noch nichts passiert." })]),
      { grid: true }
    );
  }

  // ---- skeletons ----

  function skeletons() {
    const sk = (cls) => h("span", { class: "sk " + cls });
    $("#ov-season").replaceChildren(sk("sk-text w20"), sk("sk-title"), h("div", { class: "cd" }, ...Array.from({ length: 4 }, () => h("div", { class: "cd-tile" }, sk("sk-title w60"), sk("sk-text w60")))));
    $("#ov-bot").replaceChildren(sk("sk-text w30"), h("div", { class: "bot-id" }, sk("sk-box"), h("div", { style: { flex: "1" } }, sk("sk-title w80"), sk("sk-text w40"))), sk("sk-text w80"), sk("sk-text w60"));
    $("#ov-bot .sk-box").style.cssText = "width:48px;height:48px;min-height:0;border-radius:14px;flex:none";
    $("#ov-stats").replaceChildren(...Array.from({ length: 4 }, () => h("div", { class: "stat" }, sk("sk-text w40"), sk("sk-title w30"), sk("sk-text w60"))));
    $("#ov-streams").replaceChildren(...Array.from({ length: 2 }, () => h("div", { class: "stream" }, h("div", { class: "thumb sk", style: { "border-radius": "0" } }), h("div", { class: "stream-body", style: { display: "block" } }, sk("sk-text w60"), sk("sk-text w80")))));
    $("#ov-feed").replaceChildren(...Array.from({ length: 5 }, () => h("li", { style: { display: "block" } }, sk("sk-text w30"), sk("sk-text w80"))));
    const rows = (n) => Array.from({ length: n }, () => h("div", { class: "row" }, h("div", { class: "row-main" }, sk("sk-text w30"), sk("sk-text w60"))));
    $("#channel-list").replaceChildren(...rows(3));
    $("#cmd-list").replaceChildren(...rows(4));
    $("#tmr-list").replaceChildren(...rows(2));
    $("#user-list").replaceChildren(...rows(2));
    $("#feed").replaceChildren(...Array.from({ length: 9 }, (_, i) => h("li", { class: "msg", style: { opacity: String(1 - i * 0.08) } }, sk("sk-text"), sk("sk-text"), sk("sk-text " + ["w80", "w60", "w40"][i % 3]))));
    $("#feed")._skeleton = true;
    $("#sys").replaceChildren(sk("sk-title w30"), sk("sk-text w80"), sk("sk-text w60"), sk("sk-text w80"));
    for (const el of $("#settings-form").elements) el.disabled = true;
  }

  // ---- wiring ----

  function wire() {
    addEventListener("hashchange", route);
    addEventListener("resize", () => requestAnimationFrame(moveIndicator));
    document.fonts?.ready.then(moveIndicator);

    for (const b of $$("[data-logout]")) {
      b.addEventListener("click", async () => {
        try {
          await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
        } catch {
          // signed out locally either way
        }
        S.lost = true;
        Live.stop();
        showLogin("Du bist abgemeldet.");
      });
    }
    $("#more-btn").addEventListener("click", openSheet);
    for (const el of $$("[data-sheet-close]")) el.addEventListener("click", closeSheet);
    for (const a of $$(".sheet-nav a")) a.addEventListener("click", closeSheet);

    $("#fail-retry").addEventListener("click", () => failWake && failWake());
    addEventListener("online", () => {
      if (failWake) failWake();
      if (S.me && !S.lost) Live.now();
    });
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden && S.me && !S.lost) {
        if (!Live.es) Live.now();
        else Live.kick();
        renderLiveMeta();
        tickSeason();
      }
    });

    const feed = $("#feed");
    feed.addEventListener(
      "scroll",
      () => {
        const at = feed.scrollTop + feed.clientHeight >= feed.scrollHeight - 48;
        if (at !== S.atBottom) S.atBottom = at;
        if (at && S.unseen) {
          S.unseen = 0;
          renderPill();
        }
      },
      { passive: true }
    );
    $("#pill-new").addEventListener("click", () => toBottom(true));
    $("#chat-pause").addEventListener("click", () => setPaused(!S.paused));
    $("#paused-resume").addEventListener("click", () => setPaused(false));
    $("#say").addEventListener("submit", sendChat);
    $("#say-channel").addEventListener("change", renderComposerHint);
    $("#say-text").addEventListener("input", renderSayCount);
    $("#say-text").addEventListener("keydown", (e) => {
      if (e.key === "ArrowUp" && !e.target.value && S.lastSent) {
        e.preventDefault();
        e.target.value = S.lastSent;
        renderSayCount();
      }
    });

    $("#add-channel").addEventListener("submit", addChannel);
    $("#channel-login").addEventListener("input", (e) => e.target.classList.remove("bad"));
    $("#add-user").addEventListener("submit", addUser);
    $("#user-login").addEventListener("input", (e) => e.target.classList.remove("bad"));

    $("#cmd-new").addEventListener("click", () => CmdEd.openItem(null));
    $("#tmr-new").addEventListener("click", () => TmrEd.openItem(null));
    $("#cmd-search").addEventListener("input", renderCommands);
    for (const dl of $$("[data-ph-list]")) dl.append(...PLACEHOLDERS.flatMap(([k, v]) => [h("dt", { text: k }), h("dd", { text: v })]));

    const sf = $("#settings-form");
    sf.addEventListener("input", settingsUI);
    sf.addEventListener("submit", saveSettings);
    $("#settings-reset").addEventListener("click", fillSettings);

    $("#log-refresh").addEventListener("click", async (e) => {
      const btn = e.currentTarget;
      busy(btn, true);
      await loadLog();
      busy(btn, false);
    });
    $("#log-search").addEventListener("input", () => logLines && renderLog());

    document.addEventListener("keydown", (e) => {
      if (e.key !== "Escape" || e.defaultPrevented || $("#modal").open) return;
      const ed = S.view === "commands" && CmdEd.open ? CmdEd : S.view === "timers" && TmrEd.open ? TmrEd : null;
      if (!$("#more-sheet").hidden) return closeSheet();
      if (!ed) return;
      // a dialog opened while this key is still being handled would be closed by it at once
      e.preventDefault();
      setTimeout(() => ed.close(), 0);
    });

    addEventListener("beforeunload", (e) => {
      if (S.lost) return;
      if (CmdEd.dirty() || TmrEd.dirty() || settingsDirty()) {
        e.preventDefault();
        e.returnValue = "";
      }
    });

    // relative times ("vor 2 Minuten") stay fresh
    setInterval(() => {
      if (!S.ov || document.hidden) return;
      renderLiveMeta();
      renderSys();
      renderUsers();
      for (const a of $$("#ov-streams .stream")) {
        const c = channel(a._key);
        if (c?.live) patchStream(a, c);
      }
    }, 30000);
  }

  // ---- start ----

  async function boot() {
    wire();
    skeletons();
    route();
    let attempt = 0;
    for (;;) {
      try {
        S.me = await api("GET", "/api/me");
        break;
      } catch (e) {
        if (e.status === 401) return;
        await failWait(e, attempt++);
      }
    }
    renderMe();
    Live.start();
    for (;;) {
      try {
        const [ov, hist] = await Promise.all([api("GET", "/api/overview"), api("GET", "/api/chat?n=300")]);
        applyOverview(ov);
        addLines(hist || [], false);
        historyLoaded();
        break;
      } catch (e) {
        if (e.status === 401) return;
        await failWait(e, attempt++);
      }
    }
  }

  boot();
})();
