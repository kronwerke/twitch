"use strict";
// The dashboard: one page, the state from /api/overview, live updates from /api/events.

const $ = (s, el = document) => el.querySelector(s);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
let csrf = "";
let state = null;
let editingCommand = null;
let editingTimer = null;

const PLACEHOLDERS = [
  ["{user}", "wer den Command geschrieben hat"],
  ["{channel}", "der Streamer, in dessen Chat es steht"],
  ["{start}", "Start der Season, ausgeschrieben"],
  ["{countdown}", "wie lange noch bis zum Start"],
  ["{discord}", "Discord-Einladung"],
  ["{website}", "Website"],
  ["{server}", "Serveradresse"],
  ["{live}", "die anderen Kronwerke-Streamer, die gerade live sind"],
  ["{args}", "was nach dem Command steht"],
];

async function api(method, path, body) {
  const r = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", "X-Csrf-Token": csrf },
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
  });
  if (r.status === 204) return null;
  const data = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(data.error || "Fehler " + r.status);
  return data;
}

function toast(msg, err) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "toast" + (err ? " err" : "");
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), err ? 6000 : 2500);
}

async function run(fn, ok) {
  try {
    await fn();
    if (ok) toast(ok);
  } catch (e) {
    toast(e.message, true);
  }
}

function since(t) {
  if (!t) return "";
  const s = Math.max(0, (Date.now() - Date.parse(t)) / 1000);
  if (s < 3600) return Math.round(s / 60) + " min";
  return Math.floor(s / 3600) + " h " + Math.round((s % 3600) / 60) + " min";
}

function time(t) {
  return new Date(t).toLocaleTimeString("de-DE", { hour: "2-digit", minute: "2-digit" });
}

// ---- start ----

async function boot() {
  const r = await fetch("/api/me", { credentials: "same-origin" });
  if (r.status === 401) {
    $("#login").hidden = false;
    return;
  }
  const me = await r.json();
  csrf = me.csrf;
  $("#me").textContent = me.login;
  $("#app").hidden = false;
  $("#placeholders").innerHTML = PLACEHOLDERS.map(([k, v]) => `<dt>${k}</dt><dd>${esc(v)}</dd>`).join("");
  wire();
  await refresh();
  const history = await api("GET", "/api/chat?n=300");
  history.forEach(addLine);
  stream();
  route();
}

async function refresh() {
  state = await api("GET", "/api/overview");
  render();
}

function stream() {
  const es = new EventSource("/api/events");
  es.onmessage = (e) => {
    const ev = JSON.parse(e.data);
    if (ev.kind === "message") addLine(ev.message);
    if (ev.kind === "state") {
      state = ev.state;
      render();
    }
  };
  es.onerror = () => setStatus();
}

function route() {
  const id = (location.hash || "#overview").slice(1).split("?")[0];
  const known = [...document.querySelectorAll(".view")].map((v) => v.id);
  const view = known.includes(id) ? id : "overview";
  document.querySelectorAll(".view").forEach((v) => v.classList.toggle("on", v.id === view));
  document.querySelectorAll(".tabs a").forEach((a) => a.classList.toggle("on", a.getAttribute("href") === "#" + view));
  if (view === "log") loadLog();
  if (view === "chat") scrollFeed(true);
}

// ---- render ----

function render() {
  setStatus();
  renderOverview();
  renderChannels();
  renderCommands();
  renderTimers();
  renderSettings();
  renderUsers();
  fillChannelSelects();
}

function setStatus() {
  if (!state) return;
  const c = state.chat;
  const live = state.channels.filter((x) => x.live).length;
  $("#status").innerHTML = `<span class="dot ${c.connected ? "on" : ""}"></span>${c.connected ? "Im Chat von " + c.joined.length + " Kanälen" : esc(c.detail || "Nicht verbunden")} · ${live} live · ${esc(state.version)}`;
}

function renderOverview() {
  const b = state.bot;
  let bot = `<p class="mark">Bot-Konto</p>`;
  if (!state.configured) {
    bot += `<p class="big">Keine Twitch-Anwendung</p><p class="mute">TTV_CLIENT_ID und TTV_CLIENT_SECRET in twitch.env eintragen. Redirect-URL der Anwendung: <code>${esc(state.callback)}</code></p>`;
  } else if (!b) {
    bot += `<p class="big">Nicht verbunden</p><p class="mute">Mit dem Twitch-Konto des Bots anmelden, nicht mit deinem eigenen. Twitch fragt nach, welches Konto; wenn du gerade als du selbst eingeloggt bist, dort zuerst abmelden oder ein privates Fenster nehmen.</p><p class="actions"><a class="btn twitch" href="/auth/bot">Bot mit Twitch verbinden</a></p>`;
  } else {
    bot += `<p class="big"><span class="dot ${state.chat.connected ? "on" : ""}"></span>${esc(b.login)}</p>
      <p class="mute">${state.chat.connected ? "Verbunden, sitzt in " + state.chat.joined.length + " Chats." : esc(state.chat.detail || "Verbindet ...")}<br>Verbunden am ${new Date(b.linked).toLocaleDateString("de-DE")} von ${esc(b.by)}.</p>
      <p class="actions"><a class="ghost small" href="/auth/bot">Anderes Konto verbinden</a> <button class="danger small" id="drop-bot" type="button">Trennen</button></p>`;
  }
  $("#bot-panel").innerHTML = bot;
  const drop = $("#drop-bot");
  if (drop) drop.onclick = () => confirm("Bot-Konto trennen? Der Bot verlässt alle Chats.") && run(() => api("DELETE", "/api/bot"), "Getrennt");

  $("#start-panel").innerHTML = `<p class="mark">Season 2</p><p class="big">${esc(state.countdown)}</p><p class="mute">Start ${esc(state.start)}</p>`;

  const live = state.channels.filter((c) => c.live).sort((a, b) => b.live.viewer_count - a.live.viewer_count);
  $("#live-panel").innerHTML = `<p class="mark">Gerade live</p>` + (live.length
    ? `<div class="live-list">${live.map((c) => `<a class="stream" href="https://www.twitch.tv/${esc(c.login)}" target="_blank" rel="noopener"><img src="${esc(thumb(c.live.thumbnail_url))}" alt="" loading="lazy"><p><strong>${esc(c.live.user_name)}</strong> <span class="tag live">${c.live.viewer_count} Zuschauer</span></p><p>${esc(c.live.title)}<br>${esc(c.live.game_name)}${c.live.started_at && !c.live.started_at.startsWith("0001") ? " · seit " + since(c.live.started_at) : ""}</p></a>`).join("")}</div>`
    : `<p class="mute">Gerade ist niemand live${state.live_error ? " (Twitch: " + esc(state.live_error) + ")" : ""}.</p>`);
}

function thumb(t) {
  return String(t || "").replace("{width}", "640").replace("{height}", "360");
}

function renderChannels() {
  const list = $("#channel-list");
  if (!state.channels.length) {
    list.innerHTML = `<p class="mute">Noch keine Kanäle. Twitch-Namen der Streamer oben eintragen.</p>`;
    return;
  }
  list.innerHTML = state.channels.map((c) => `
    <div class="item" data-login="${esc(c.login)}">
      ${c.image ? `<img src="${esc(c.image)}" alt="">` : `<span class="avatar">${esc((c.name || c.login)[0].toUpperCase())}</span>`}
      <div class="grow">
        <p><strong>${esc(c.name || c.login)}</strong>${c.live ? ' <span class="tag live">live</span>' : ""}${c.joined ? ' <span class="tag ok">im Chat</span>' : ""}</p>
        <p class="sub">${c.live ? esc(c.live.game_name) + " · " + c.live.viewer_count + " Zuschauer" : esc(c.note || "twitch.tv/" + c.login)}</p>
      </div>
      <label class="toggle"><input type="checkbox" data-k="enabled" ${c.enabled ? "checked" : ""}> Bot im Chat</label>
      <label class="toggle"><input type="checkbox" data-k="website" ${c.website ? "checked" : ""}> Auf der Website</label>
      <button class="danger small" data-del type="button">Entfernen</button>
    </div>`).join("");
  list.querySelectorAll(".item").forEach((el) => {
    const login = el.dataset.login;
    el.querySelectorAll("input[data-k]").forEach((cb) => (cb.onchange = () => run(() => api("PATCH", "/api/channels/" + login, { [cb.dataset.k]: cb.checked }), "Gespeichert")));
    el.querySelector("[data-del]").onclick = () => confirm(login + " entfernen?") && run(() => api("DELETE", "/api/channels/" + login), "Entfernt");
  });
}

function renderCommands() {
  const list = $("#command-list");
  list.innerHTML = state.commands.map((c) => `
    <div class="item click ${editingCommand === c.name ? "sel" : ""}" data-name="${esc(c.name)}">
      <div class="grow ${c.enabled ? "" : "off"}">
        <p><strong>!${esc(c.name)}</strong>${(c.aliases || []).map((a) => ` <span class="tag">!${esc(a)}</span>`).join("")}${c.mod_only ? ' <span class="tag">Mods</span>' : ""}${c.enabled ? "" : ' <span class="tag">aus</span>'}</p>
        <p class="sub">${esc(c.response)}</p>
      </div>
      <span class="sub">${c.uses}×</span>
    </div>`).join("") || `<p class="mute">Noch keine Commands.</p>`;
  list.querySelectorAll(".item").forEach((el) => (el.onclick = () => openCommand(el.dataset.name)));
}

function renderTimers() {
  const list = $("#timer-list");
  list.innerHTML = state.timers.map((t) => `
    <div class="item click ${editingTimer === t.id ? "sel" : ""}" data-id="${esc(t.id)}">
      <div class="grow ${t.enabled ? "" : "off"}">
        <p><strong>${esc(t.name || "Timer")}</strong> <span class="tag">alle ${t.minutes} min</span>${t.live_only ? ' <span class="tag">nur live</span>' : ""}${t.enabled ? "" : ' <span class="tag">aus</span>'}</p>
        <p class="sub">${esc(t.text)}</p>
      </div>
    </div>`).join("") || `<p class="mute">Noch keine Timer.</p>`;
  list.querySelectorAll(".item").forEach((el) => (el.onclick = () => openTimer(el.dataset.id)));
}

function renderSettings() {
  const f = $("#settings-form");
  if (f.contains(document.activeElement)) return;
  for (const k of ["start", "discord", "website", "server"]) f.elements[k].value = state.settings[k] || "";
}

function renderUsers() {
  $("#user-list").innerHTML = state.users.map((u) => `
    <div class="item" data-login="${esc(u.login)}">
      <div class="grow"><p><strong>${esc(u.login)}</strong>${u.owner ? ' <span class="tag ok">Besitzer</span>' : ""}${u.id ? "" : ' <span class="tag">noch nie angemeldet</span>'}</p>
      <p class="sub">${u.last_seen ? "zuletzt " + new Date(u.last_seen).toLocaleString("de-DE") : "Zugang seit " + new Date(u.added).toLocaleDateString("de-DE")}</p></div>
      ${u.owner ? "" : '<button class="danger small" type="button">Zugang entziehen</button>'}
    </div>`).join("");
  $("#user-list").querySelectorAll(".item button").forEach((b) => {
    const login = b.closest(".item").dataset.login;
    b.onclick = () => confirm(login + " den Zugang entziehen?") && run(() => api("DELETE", "/api/users/" + login), "Entzogen");
  });
}

function fillChannelSelects() {
  const names = state.channels.map((c) => c.login);
  const joined = state.chat.joined;
  const fill = (sel, list, first) => {
    const keep = [...sel.selectedOptions].map((o) => o.value);
    sel.innerHTML = (first ? `<option value="">${first}</option>` : "") + list.map((n) => `<option value="${esc(n)}">${esc(n)}</option>`).join("");
    [...sel.options].forEach((o) => (o.selected = keep.includes(o.value)));
  };
  fill($("#chat-filter"), names, "Alle Kanäle");
  const say = $("#say-channel");
  const cur = say.value;
  fill(say, joined);
  if (joined.includes(cur)) say.value = cur;
  for (const f of [$("#command-form"), $("#timer-form")]) {
    if (!f.contains(document.activeElement)) fill(f.elements.channels, names);
  }
}

// ---- chat ----

const colors = {};
function channelColor(ch) {
  if (!colors[ch]) {
    let h = 0;
    for (const c of ch) h = (h * 31 + c.charCodeAt(0)) % 360;
    colors[ch] = `hsl(${h} 55% 68%)`;
  }
  return colors[ch];
}

function addLine(m) {
  const feed = $("#feed");
  const filter = $("#chat-filter").value;
  const li = document.createElement("li");
  li.dataset.ch = m.channel;
  li.className = (m.kind || "chat") + (m.self ? " self" : "");
  if (filter && m.channel !== filter) li.hidden = true;
  const badges = (m.broadcaster ? "Streamer " : m.mod ? "Mod " : "") + (m.vip ? "VIP " : "");
  const user = m.kind === "notice" ? "" : `<span class="u" style="color:${esc(m.color || "#c9c1b6")}">${esc(m.user || m.login)}</span>`;
  li.innerHTML = `<span class="t">${time(m.time)}</span><span class="ch" style="color:${channelColor(m.channel)}">${esc(m.channel)}</span>${badges ? `<span class="badge">${esc(badges.trim())}</span>` : ""}${user}<span class="x">${esc(m.text)}</span>`;
  li.querySelector(".ch").onclick = () => {
    $("#chat-filter").value = m.channel;
    applyFilter();
    $("#say-channel").value = m.channel;
  };
  const stick = feed.scrollTop + feed.clientHeight >= feed.scrollHeight - 40;
  feed.append(li);
  while (feed.children.length > 1500) feed.firstElementChild.remove();
  if (stick && !$("#chat-pause").checked) scrollFeed();
}

function scrollFeed(force) {
  const feed = $("#feed");
  if (force || !$("#chat-pause").checked) feed.scrollTop = feed.scrollHeight;
}

function applyFilter() {
  const f = $("#chat-filter").value;
  $("#feed").querySelectorAll("li").forEach((li) => (li.hidden = !!f && li.dataset.ch !== f));
  scrollFeed(true);
}

// ---- editors ----

function selected(sel) {
  return [...sel.selectedOptions].map((o) => o.value);
}

function select(sel, values) {
  [...sel.options].forEach((o) => (o.selected = (values || []).includes(o.value)));
}

function openCommand(name) {
  const f = $("#command-form");
  const c = state.commands.find((x) => x.name === name) || { name: "", aliases: [], response: "", cooldown: 30, enabled: true, mod_only: false, channels: [] };
  editingCommand = name;
  f.hidden = false;
  $("#command-title").textContent = name ? "!" + name : "Neuer Command";
  f.elements.name.value = c.name;
  f.elements.aliases.value = (c.aliases || []).join(", ");
  f.elements.response.value = c.response;
  f.elements.cooldown.value = c.cooldown;
  f.elements.enabled.checked = c.enabled;
  f.elements.mod_only.checked = c.mod_only;
  select(f.elements.channels, c.channels);
  $("#command-delete").hidden = !name;
  preview(f.elements.response, $("#command-preview"));
  renderCommands();
  f.elements[name ? "response" : "name"].focus();
}

function openTimer(id) {
  const f = $("#timer-form");
  const t = state.timers.find((x) => x.id === id) || { name: "", text: "", minutes: 30, min_lines: 15, enabled: true, live_only: true, channels: [] };
  editingTimer = id;
  f.hidden = false;
  f.elements.name.value = t.name;
  f.elements.text.value = t.text;
  f.elements.minutes.value = t.minutes;
  f.elements.min_lines.value = t.min_lines;
  f.elements.enabled.checked = t.enabled;
  f.elements.live_only.checked = t.live_only;
  select(f.elements.channels, t.channels);
  $("#timer-delete").hidden = !id;
  preview(f.elements.text, $("#timer-preview"));
  renderTimers();
}

let previewTimer;
function preview(input, out) {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(async () => {
    if (!input.value.trim()) {
      out.textContent = "Vorschau";
      return;
    }
    try {
      const r = await api("POST", "/api/preview", { text: input.value, channel: state.channels[0]?.login || "" });
      out.textContent = r.text;
    } catch (e) {
      out.textContent = e.message;
    }
  }, 250);
}

// ---- wiring ----

function wire() {
  window.addEventListener("hashchange", route);
  $("#logout").onclick = async () => {
    await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
    location.reload();
  };
  $("#chat-filter").onchange = applyFilter;
  $("#say").onsubmit = (e) => {
    e.preventDefault();
    const text = $("#say-text");
    run(async () => {
      await api("POST", "/api/chat", { channel: $("#say-channel").value, text: text.value });
      text.value = "";
    });
  };
  $("#add-channel").onsubmit = (e) => {
    e.preventDefault();
    run(async () => {
      await api("POST", "/api/channels", { login: $("#channel-login").value, note: $("#channel-note").value });
      $("#channel-login").value = $("#channel-note").value = "";
    }, "Kanal hinzugefügt, der Bot joint gleich");
  };
  $("#add-user").onsubmit = (e) => {
    e.preventDefault();
    run(async () => {
      await api("POST", "/api/users", { login: $("#user-login").value });
      $("#user-login").value = "";
    }, "Zugang gegeben");
  };

  const cf = $("#command-form");
  $("#new-command").onclick = () => openCommand(null);
  $("#command-cancel").onclick = () => { cf.hidden = true; editingCommand = null; renderCommands(); };
  cf.elements.response.oninput = () => preview(cf.elements.response, $("#command-preview"));
  cf.onsubmit = (e) => {
    e.preventDefault();
    const body = {
      name: cf.elements.name.value.trim().toLowerCase(),
      aliases: cf.elements.aliases.value.split(",").map((s) => s.trim()).filter(Boolean),
      response: cf.elements.response.value,
      cooldown: +cf.elements.cooldown.value || 0,
      enabled: cf.elements.enabled.checked,
      mod_only: cf.elements.mod_only.checked,
      channels: selected(cf.elements.channels),
    };
    run(async () => {
      const saved = await api("PUT", "/api/commands/" + encodeURIComponent(editingCommand || body.name), body);
      editingCommand = saved.name;
      $("#command-title").textContent = "!" + saved.name;
      $("#command-delete").hidden = false;
    }, "Gespeichert, gilt ab sofort");
  };
  $("#command-delete").onclick = () => editingCommand && confirm("!" + editingCommand + " löschen?") && run(async () => {
    await api("DELETE", "/api/commands/" + encodeURIComponent(editingCommand));
    cf.hidden = true;
    editingCommand = null;
  }, "Gelöscht");

  const tf = $("#timer-form");
  $("#new-timer").onclick = () => openTimer(null);
  $("#timer-cancel").onclick = () => { tf.hidden = true; editingTimer = null; renderTimers(); };
  tf.elements.text.oninput = () => preview(tf.elements.text, $("#timer-preview"));
  tf.onsubmit = (e) => {
    e.preventDefault();
    const body = {
      name: tf.elements.name.value.trim(),
      text: tf.elements.text.value,
      minutes: +tf.elements.minutes.value,
      min_lines: +tf.elements.min_lines.value,
      enabled: tf.elements.enabled.checked,
      live_only: tf.elements.live_only.checked,
      channels: selected(tf.elements.channels),
    };
    run(async () => {
      const saved = await api("PUT", "/api/timers/" + (editingTimer || "new"), body);
      editingTimer = saved.id;
      $("#timer-delete").hidden = false;
    }, "Gespeichert, gilt ab sofort");
  };
  $("#timer-delete").onclick = () => editingTimer && confirm("Timer löschen?") && run(async () => {
    await api("DELETE", "/api/timers/" + editingTimer);
    tf.hidden = true;
    editingTimer = null;
  }, "Gelöscht");

  $("#settings-form").onsubmit = (e) => {
    e.preventDefault();
    const f = e.target;
    run(() => api("PATCH", "/api/settings", { start: f.elements.start.value, discord: f.elements.discord.value, website: f.elements.website.value, server: f.elements.server.value }), "Gespeichert");
  };
}

async function loadLog() {
  const lines = await api("GET", "/api/log");
  $("#log-list").innerHTML = lines.reverse().map((l) => `<li>${esc(l)}</li>`).join("");
}

boot();
