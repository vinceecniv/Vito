// Vito in the browser, without the helper: this file stands in for the daemon.
//
// The interface talks to the daemon through api("/api/...") and a WebSocket of
// events. When the page is opened from the website (vito.talk/app) there is no
// daemon, so index.html routes both here instead: the same routes, the same
// JSON shapes, answered from localStorage, with Whistle running as WebAssembly
// in a worker (whistle-worker.js) for the speech recognition.
//
// What needs the helper — a global hotkey, typing into other apps, the better
// local models, cloud speech services, AI cleanup — answers as unsupported,
// and index.html hides those settings.
//
// The figures (stats, streaks, achievements) are ports of internal/history;
// keep them in step when those change.
"use strict";
(function () {
  const BASE = new URL(".", document.currentScript ? document.currentScript.src : location.href).href;
  const LS_CONFIG = "vito-web-config", LS_HISTORY = "vito-web-history", LS_DAYS = "vito-web-days",
        LS_UNLOCKED = "vito-web-achievements";
  const MAX_HISTORY = 2000; // localStorage holds ~5 MB; this keeps text well under it
  const RATE = 16000;

  // ---- storage ----
  const read = (k, d) => { try { const v = localStorage.getItem(k); return v ? JSON.parse(v) : d; } catch { return d; } };
  const write = (k, v) => { try { localStorage.setItem(k, JSON.stringify(v)); return true; } catch { return false; } };

  let DEFAULTS = null, ACH = [], cfg = null;
  let history = read(LS_HISTORY, []);   // newest first
  let days = read(LS_DAYS, {});         // "yyyy-mm-dd" -> {words, sentences, activations, duration_ms}
  let unlocked = read(LS_UNLOCKED, {}); // id -> unix ms

  const ready = (async () => {
    const [d, a] = await Promise.all([
      fetch(BASE + "defaults.json").then((r) => r.json()),
      fetch(BASE + "achievements.json").then((r) => r.json()).catch(() => ({})),
    ]);
    DEFAULTS = d; ACH = a.list || []; ACH_IMAGES = a.images || []; ACH_ANIMATED = a.animated || [];
    cfg = merge(structuredClone(DEFAULTS), read(LS_CONFIG, {}));
    // What the browser can do, whatever an older saved config says.
    cfg.stt.provider = "whistle"; cfg.stt.model = "whistle";
    cfg.cleanup.enabled = false; cfg.history.store_audio = false;
    if (!WHISTLE_LANGS.includes(cfg.stt.language)) cfg.stt.language = guessLang();
  })();

  const WHISTLE_LANGS = ["nl", "en", "de", "fr", "es", "it", "pl"];
  function guessLang() {
    for (const l of navigator.languages || [navigator.language || "en"]) {
      const c = String(l).slice(0, 2).toLowerCase();
      if (WHISTLE_LANGS.includes(c)) return c;
    }
    return "en";
  }

  function merge(base, over) {
    if (!over || typeof over !== "object" || Array.isArray(over)) return over === undefined ? base : over;
    for (const k of Object.keys(over)) {
      base[k] = base[k] && typeof base[k] === "object" && !Array.isArray(base[k]) ? merge(base[k], over[k]) : over[k];
    }
    return base;
  }
  const saveConfig = () => write(LS_CONFIG, cfg);

  // ---- events (what the daemon sends over its WebSocket) ----
  let onEvent = () => {};
  const emit = (e) => { try { onEvent(e); } catch (err) { console.error(err); } };

  // ---- dates ----
  const pad = (n) => String(n).padStart(2, "0");
  const dayKey = (d) => d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate());
  const midnight = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate());
  const parseDay = (s) => { const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s || ""); return m ? new Date(+m[1], m[2] - 1, +m[3]) : null; };
  const addDays = (d, n) => new Date(d.getFullYear(), d.getMonth(), d.getDate() + n);
  const daysBetween = (a, b) => Math.round((b - a) / 86400000);
  const typingWPM = () => ({ slow: 25, fast: 65 }[cfg.stats && cfg.stats.typing_speed] || 40);

  // ---- text ----
  const countWords = (t) => (t.trim() ? t.trim().split(/\s+/).length : 0);
  function countSentences(t) {
    let n = 0, inTerm = false;
    for (const ch of t) {
      if (ch === "." || ch === "!" || ch === "?") { if (!inTerm) { n++; inTerm = true; } } else inTerm = false;
    }
    return n === 0 && t.trim() ? 1 : n;
  }
  // internal/cleanup/plain.go
  const plain = (t) => t.replace(/[‐‑]/g, "-").replace(/[   ]/g, " ").replace(/[­​‌⁠﻿]/g, "");
  // internal/dictionary: corrections, case-insensitive on word boundaries.
  function applyDictionary(text) {
    for (const c of (cfg.dictionary && cfg.dictionary.corrections) || []) {
      const wrong = (c.wrong || "").trim();
      if (!wrong || !c.right) continue;
      const re = new RegExp("(?<![\\p{L}\\p{N}_])" + wrong.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "(?![\\p{L}\\p{N}_])", "giu");
      text = text.replace(re, () => c.right);
    }
    return text;
  }
  // The browser's cleanup: rules, not a model. Hesitations out, a word said
  // twice in a row once, spacing around punctuation, a capital to start with.
  const FILLERS = /(^|[\s,])(?:e+h+m*|u+h+m*|u+m+|e+r+m+|euh+|hmm+)(?=[\s,.!?]|$)/giu;
  function ruleCleanup(t) {
    t = t.replace(FILLERS, "$1");
    t = t.replace(/\b([\p{L}']+)(\s+\1\b)+/giu, "$1");
    t = t.replace(/\s+([,.!?;:])/g, "$1").replace(/,\s*([.!?])/g, "$1").replace(/^[\s,]+/, "").replace(/\s{2,}/g, " ").trim();
    return t ? t[0].toLocaleUpperCase() + t.slice(1) : t;
  }
  const keyterms = () => {
    const seen = new Set(), out = [];
    for (let t of (cfg.dictionary && cfg.dictionary.keyterms) || []) {
      t = String(t).trim();
      if (!t || seen.has(t.toLowerCase())) continue;
      seen.add(t.toLowerCase()); out.push(t);
      if (out.length === 100) break;
    }
    return out;
  };

  // ---- Whistle (the worker) ----
  let worker = null, whistle = { phase: "absent" }, workerReady = null;
  const setWhistle = (s) => { whistle = s; emit({ type: "whistle", whistle: s }); };
  const MODEL_URL = "https://huggingface.co/Cactus-Compute/whistle/resolve/b358ddadd89b7a713b5aa131f23032d3cca1b251/whistle.cact";

  function startWorker() {
    if (workerReady) return workerReady;
    worker = new Worker(BASE + "whistle-worker.js");
    workerReady = new Promise((resolve, reject) => {
      worker.onmessage = (ev) => {
        const m = ev.data;
        if (m.type === "progress") setWhistle({ phase: "downloading", done: m.done, total: m.total });
        else if (m.type === "ready") { setWhistle({ phase: "ready" }); resolve(); }
        else if (m.type === "error" && m.fatal) {
          setWhistle({ phase: "error", error: m.error });
          workerReady = null; worker.terminate(); worker = null;
          reject(new Error(m.error));
        } else onWorker(m);
      };
    });
    workerReady.catch(() => {});
    setWhistle({ phase: "downloading", done: 0, total: 16919407 });
    worker.postMessage({ type: "load" });
    return workerReady;
  }
  // Already downloaded on an earlier visit: load it straight away, so the
  // first dictation doesn't wait. Not yet: fetch it — this page is where
  // people come to try Vito, and it does nothing without the model.
  async function warmUp() {
    try {
      const c = await caches.open("vito-whistle-model");
      if (await c.match(MODEL_URL)) whistle = { phase: "ready" };
    } catch {}
    startWorker();
  }

  // ---- recording ----
  let state = "idle", rec = null, lastTimings = {};
  const setState = (s) => { state = s; emit({ type: "state", state: s }); };

  const WORKLET = `class P extends AudioWorkletProcessor{constructor(){super();this.r=sampleRate/${RATE};this.a=0;this.s=0;this.n=0;this.b=new Float32Array(1600);this.l=0;this.p=0}
process(i){const c=i[0]&&i[0][0];if(!c)return true;for(let k=0;k<c.length;k++){const v=c[k];this.s+=v;this.n++;this.a+=1;const m=v<0?-v:v;if(m>this.p)this.p=m;if(this.a>=this.r){this.a-=this.r;this.b[this.l++]=this.s/this.n;this.s=0;this.n=0;if(this.l===this.b.length){this.port.postMessage({pcm:this.b,peak:this.p},[this.b.buffer]);this.b=new Float32Array(1600);this.l=0;this.p=0}}}return true}}
registerProcessor("vito-pcm",P)`;

  async function start() {
    if (state !== "idle") return;
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) throw new Error("This browser cannot record audio.");
    setState("recording");
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: {
        deviceId: cfg.audio.input_device ? { ideal: cfg.audio.input_device } : undefined,
        echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
      const ctx = new AudioContext();
      const url = URL.createObjectURL(new Blob([WORKLET], { type: "text/javascript" }));
      await ctx.audioWorklet.addModule(url);
      URL.revokeObjectURL(url);
      const src = ctx.createMediaStreamSource(stream);
      const node = new AudioWorkletNode(ctx, "vito-pcm");
      const r = { stream, ctx, node, started: performance.now(), samples: 0, lastLoud: performance.now(), heard: false, buffered: [] };
      rec = r;
      node.port.onmessage = (ev) => {
        if (rec !== r) return;
        const { pcm, peak } = ev.data;
        r.samples += pcm.length;
        const db = peak > 0 ? 20 * Math.log10(peak) : -100;
        emit({ type: "level", level: Math.max(0, Math.min(100, Math.round((db + 60) / 60 * 100))), clip: peak >= 0.99 });
        if (db > -40) { r.lastLoud = performance.now(); r.heard = true; }
        if (worker && whistle.phase === "ready") {
          for (const b of r.buffered.splice(0)) worker.postMessage({ type: "audio", pcm: b }, [b.buffer]);
          worker.postMessage({ type: "audio", pcm }, [pcm.buffer]);
        } else r.buffered.push(pcm); // still downloading: keep it for when the model is there
        // Auto-stop after a pause, when switched on, as the daemon does.
        if (cfg.audio.auto_stop && r.heard && performance.now() - r.lastLoud > (cfg.audio.auto_stop_silence_ms || 1200)) stop();
        else if (performance.now() - r.lastLoud > (cfg.audio.silence_timeout_sec || 15) * 1000 && !r.heard) cancel();
      };
      src.connect(node);
      // Nothing to hear from the node; connecting it keeps the graph running.
      node.connect(ctx.destination);
      startWorker();
      worker.postMessage({ type: "start", lang: cfg.stt.language || "", keywords: cfg.stt.keyterms_enabled === false ? [] : keyterms() });
    } catch (e) {
      rec = null;
      setState("idle");
      const msg = e && e.name === "NotAllowedError" ? "Microphone access was denied." : (e && e.message) || String(e);
      emit({ type: "error", error: msg });
      throw new Error(msg);
    }
  }

  function release(r) {
    try { r.node.port.onmessage = null; r.node.disconnect(); } catch {}
    try { r.stream.getTracks().forEach((t) => t.stop()); } catch {}
    try { r.ctx.close(); } catch {}
  }

  async function stop() {
    const r = rec;
    if (!r || state !== "recording") return;
    rec = null;
    release(r);
    r.stopped = performance.now();
    setState("processing");
    try { await workerReady; } catch (e) { emit({ type: "error", error: e.message }); setState("idle"); return; }
    for (const b of r.buffered.splice(0)) worker.postMessage({ type: "audio", pcm: b }, [b.buffer]);
    pendingStop = r;
    worker.postMessage({ type: "stop" });
  }

  function cancel() {
    const r = rec;
    rec = null;
    if (r) release(r);
    pendingStop = null;
    if (worker) worker.postMessage({ type: "abort" });
    if (state !== "idle") setState("idle");
  }

  let pendingStop = null;
  function onWorker(m) {
    if (m.type === "partial" && state === "recording") emit({ type: "partial", text: m.text });
    else if (m.type === "error") console.warn("whistle:", m.error);
    else if (m.type === "final" && pendingStop) {
      const r = pendingStop;
      pendingStop = null;
      finish(r, m.text || "", m.language || cfg.stt.language);
    }
  }

  async function finish(r, text, language) {
    const sttMS = performance.now() - r.stopped;
    let raw = ruleCleanup(plain(applyDictionary(text)));
    if (!raw) {
      setState("idle");
      emit({ type: "error", error: "No speech recognised." });
      return;
    }
    let copied = false;
    try { await navigator.clipboard.writeText(raw); copied = true; } catch {}
    const injectedMS = performance.now() - r.stopped;
    const durMS = Math.round(r.samples / RATE * 1000);
    const ms = (v) => Math.round(v) * 1e6; // Go's time.Duration is nanoseconds
    lastTimings = { recording_ms: ms(durMS), stt_final_ms: ms(sttMS), cleanup_ms: 0, injected_ms: ms(injectedMS) };
    const entry = record({ raw, language, duration_ms: durMS, stt_ms: Math.round(sttMS), injected_ms: Math.round(injectedMS) });
    emit({ type: "final", raw, cleaned: "", text: raw, timings: lastTimings, entry_id: entry ? entry.id : "" });
    setState("idle");
    if (copied) emit({ type: "copied" });
  }

  // ---- history and the permanent day sums ----
  const newID = () => [...crypto.getRandomValues(new Uint8Array(8))].map((b) => b.toString(16).padStart(2, "0")).join("");
  function record(e) {
    const now = new Date();
    const words = countWords(e.raw), sentences = countSentences(e.raw);
    const d = days[dayKey(now)] || (days[dayKey(now)] = { words: 0, sentences: 0, activations: 0, duration_ms: 0 });
    d.words += words; d.sentences += sentences; d.activations++; d.duration_ms += e.duration_ms;
    write(LS_DAYS, days);
    if (cfg.history && cfg.history.enabled === false) return null;
    const entry = { id: newID(), timestamp: now.toISOString(), duration_ms: e.duration_ms, language: e.language || "",
      source: "stream", raw: e.raw, cleanup_used: false, stt_ms: e.stt_ms, cleanup_ms: 0, injected_ms: e.injected_ms,
      words, sentences, favorite: false };
    history.unshift(entry);
    pruneHistory();
    return entry;
  }
  function pruneHistory() {
    const max = Math.min(MAX_HISTORY, (cfg.history && cfg.history.max_entries) || MAX_HISTORY);
    if (history.length > max) {
      // Favorites are kept, as in the daemon.
      let over = history.length - max;
      for (let i = history.length - 1; i >= 0 && over > 0; i--) if (!history[i].favorite) { history.splice(i, 1); over--; }
    }
    while (!write(LS_HISTORY, history) && history.length > 10) history.splice(-Math.ceil(history.length / 10));
  }

  // ---- stats: internal/history/stats.go, insights.go, achievements.go ----
  const DAY_LABELS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];
  const MONTH_LABELS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

  function dayTotals(from, to) {
    let words = 0, sentences = 0, activations = 0, dur = 0, first = "";
    for (const [k, v] of Object.entries(days)) {
      if ((from && k < from) || k > to) continue;
      words += v.words; sentences += v.sentences; activations += v.activations; dur += v.duration_ms;
      if (!first || k < first) first = k;
    }
    return { words, sentences, activations, dur, first };
  }
  const firstDataDay = () => Object.keys(days).sort()[0] || "";

  function spanBuckets(start, end, clip) {
    const span = daysBetween(start, end) + 1, out = [];
    const b = (from, to, label) => ({ from: dayKey(clip && from < start ? start : from), to: dayKey(to > end ? end : to), label });
    if (span <= 62) {
      for (let d = start; d <= end; d = addDays(d, 1)) {
        const wd = (d.getDay() + 6) % 7;
        out.push({ from: dayKey(d), to: dayKey(d), label: span > 10 ? String(d.getDate()) : DAY_LABELS[wd], weekend: wd >= 5 });
      }
      return [out, "day"];
    }
    if (span <= 371) {
      for (let w = addDays(start, -((start.getDay() + 6) % 7)); w <= end; w = addDays(w, 7)) out.push(b(w, addDays(w, 6), w.getDate() + "/" + (w.getMonth() + 1)));
      return [out, "week"];
    }
    for (let m = new Date(start.getFullYear(), start.getMonth(), 1); m <= end; m = new Date(m.getFullYear(), m.getMonth() + 1, 1)) {
      out.push(b(m, new Date(m.getFullYear(), m.getMonth() + 1, 0), MONTH_LABELS[m.getMonth()]));
    }
    return [out, "month"];
  }

  function percentile(v, p) {
    if (!v.length) return 0;
    v.sort((a, b) => a - b);
    return v[Math.max(0, Math.floor((p * v.length + 99) / 100) - 1)];
  }
  let STOP = null;
  function insights(from, to) {
    const end = addDays(to, 1).getTime(), start = from ? from.getTime() : 0;
    const out = { dictations: 0, latency_median_ms: 0, latency_p95_ms: 0, stt_median_ms: 0, cleanup_median_ms: 0,
      cleanup_runs: 0, cleanup_changed: 0, cleanup_failed: 0, languages: [], top_words: [] };
    const lat = [], stt = [], langs = {}, counts = {};
    STOP = STOP || new Set(STOPWORDS.split(/\s+/));
    for (const e of history) {
      const ts = Date.parse(e.timestamp);
      if (ts < start || ts >= end || e.source === "upload") continue;
      out.dictations++;
      if (e.injected_ms > 0) lat.push(e.injected_ms);
      if (e.stt_ms > 0) stt.push(e.stt_ms);
      const l = (e.language || "").toLowerCase().trim();
      if (l && l !== "auto") langs[l] = (langs[l] || 0) + 1;
      for (let w of (e.cleaned || e.raw || "").toLowerCase().split(/[^\p{L}\p{N}'’]+/u)) {
        w = w.replace(/^['’]+|['’]+$/g, "");
        if ([...w].length <= 3 || STOP.has(w) || !/\p{L}/u.test(w)) continue;
        counts[w] = (counts[w] || 0) + 1;
      }
    }
    out.latency_median_ms = percentile(lat, 50); out.latency_p95_ms = percentile(lat, 95); out.stt_median_ms = percentile(stt, 50);
    out.languages = Object.entries(langs).map(([code, count]) => ({ code, count }))
      .sort((a, b) => b.count - a.count || (a.code < b.code ? -1 : 1));
    out.top_words = Object.entries(counts).filter(([, c]) => c > 1).map(([word, count]) => ({ word, count }))
      .sort((a, b) => b.count - a.count || (a.word < b.word ? -1 : 1)).slice(0, 40);
    return out;
  }

  function calendar(now) {
    const today = midnight(now);
    const start = addDays(today, -((today.getDay() + 6) % 7) - 7 * 25);
    const words = new Array(daysBetween(start, today) + 1).fill(0);
    for (const [k, v] of Object.entries(days)) {
      const d = parseDay(k);
      const i = d ? daysBetween(start, d) : -1;
      if (i >= 0 && i < words.length) words[i] = v.words;
    }
    return { start: dayKey(start), words };
  }

  // Streaks allow two missed days in any seven (streakMissesPerWeek).
  function streakWalk(on, onEnd) {
    let start = 0, longest = 0;
    const missed = (a, b) => { let n = 0; for (let i = a; i <= b; i++) if (!on[i]) n++; return n; };
    for (let t = 0; t < on.length; t++) {
      while (start <= t && !on[start]) start++;
      while (start <= t && missed(Math.max(start, t - 6), t) > 2) { start++; while (start <= t && !on[start]) start++; }
      if (on[t] && start <= t) longest = Math.max(longest, t - start + 1);
    }
    return onEnd ? start : longest;
  }
  function activeDays() {
    return Object.keys(days).filter((k) => days[k].words > 0).sort().map(parseDay).filter(Boolean);
  }
  function longestStreak(active) {
    if (!active.length) return 0;
    const on = new Array(daysBetween(active[0], active[active.length - 1]) + 1).fill(false);
    for (const d of active) on[daysBetween(active[0], d)] = true;
    return streakWalk(on, false);
  }
  function currentStreak(active, today) {
    if (!active.length) return 0;
    const n = daysBetween(active[0], today) + 1;
    if (n < 1) return 0;
    const on = new Array(n).fill(false);
    for (const d of active) { const i = daysBetween(active[0], d); if (i >= 0 && i < n) on[i] = true; }
    on[n - 1] = true;
    const start = streakWalk(on, true);
    const last = Math.min(daysBetween(active[0], active[active.length - 1]), n - 1);
    return start > last ? 0 : last - start + 1;
  }
  function streaks(now) {
    const active = activeDays(), today = midnight(now);
    const current = currentStreak(active, today);
    let previous;
    if (current > 0) {
      const begin = addDays(active[active.length - 1], -(current - 1));
      previous = longestStreak(active.filter((d) => d < begin));
    } else previous = longestStreak(active);
    return { current, longest: longestStreak(active), previous };
  }

  function stats(q) {
    const wpm = typingWPM(), now = new Date(), today = midnight(now);
    let from = null, anchor = today, n = q.has("days") ? parseInt(q.get("days"), 10) : 28, bucketsFn;
    const qf = parseDay(q.get("from")), qt = parseDay(q.get("to"));
    if (qf && qt) {
      let a = qf, b = qt;
      if (b < a) [a, b] = [b, a];
      if (b > today) b = today;
      if (a > b) a = b;
      from = a; anchor = b; n = daysBetween(a, b) + 1;
      bucketsFn = () => spanBuckets(a, b, true);
    } else {
      if (isNaN(n) || n < -1) n = 28;
      if (n === -1) { anchor = addDays(today, -1); n = 1; }
      if (n > 0) from = addDays(anchor, -(n - 1));
      bucketsFn = (first) => {
        let span = n === 1 ? 7 : n, start = today;
        if (span > 0) start = addDays(today, -(span - 1));
        else if (parseDay(first)) start = parseDay(first);
        return spanBuckets(start, today, false);
      };
    }
    const toDay = dayKey(anchor), fromDay = from ? dayKey(from) : "";
    const tot = dayTotals(fromDay, toDay);
    let divisor = 1, span = n > 0 ? n : 0;
    if (tot.first) {
      const d = Math.max(1, daysBetween(parseDay(tot.first), anchor) + 1);
      divisor = d;
      if (n <= 0) span = d; else if (divisor > n) divisor = n;
    }
    span = Math.max(1, span);
    const saved = Math.max(0, tot.words / wpm - tot.dur / 60000);
    const st = { period_days: span, words: tot.words, sentences: tot.sentences, activations: tot.activations, commands: 0,
      activations_per_day: tot.activations / divisor, saved_minutes: Math.round(saved), spoken_seconds: Math.floor(tot.dur / 1000),
      typing_wpm: wpm, first_day: firstDataDay(), week: [], week_peak_index: -1, series_unit: "", currency: "eur",
      spoken_wpm: tot.dur > 0 ? Math.round(tot.words / (tot.dur / 60000)) : 0, avg_words: tot.activations ? tot.words / tot.activations : 0,
      insights: insights(from, anchor), calendar: calendar(now) };
    const s = streaks(now);
    st.current_streak = s.current; st.longest_streak = s.longest; st.previous_streak = s.previous;
    const bar = (label, date, end, t, extra) => Object.assign({ label, date, end_date: end || undefined, words: t.words, sentences: t.sentences,
      activations: t.activations, spoken_seconds: Math.floor(t.dur / 1000), saved_minutes: Math.round(Math.max(0, t.words / wpm - t.dur / 60000)),
      weekend: false, cost: 0 }, extra || {});
    let peak = -1;
    if (n === 1) {
      st.series_unit = "hour";
      const hours = Array.from({ length: 24 }, () => ({ words: 0, sentences: 0, activations: 0, dur: 0 }));
      for (const e of history) {
        const t = new Date(e.timestamp);
        if (dayKey(t) !== toDay) continue;
        const h = hours[t.getHours()];
        h.words += e.words; h.sentences += e.sentences; h.activations++; h.dur += e.duration_ms;
      }
      hours.forEach((h, i) => { st.week.push(bar(String(i), toDay, "", h, { hour: i })); if (h.words > peak) { peak = h.words; st.week_peak_index = i; } });
    } else {
      const [bars, unit] = bucketsFn(tot.first);
      st.series_unit = unit;
      bars.forEach((b, i) => {
        const t = dayTotals(b.from, b.to);
        st.week.push(bar(b.label, b.from, b.to, t, { weekend: !!b.weekend }));
        if (t.words > peak) { peak = t.words; st.week_peak_index = i; }
      });
    }
    if (peak <= 0) st.week_peak_index = -1;
    return st;
  }

  function achievementInputs() {
    const st = { words: 0, sentences: 0, activations: 0, spoken: 0, saved: 0, day: 0, week: 0, streak: 0, languages: 0,
      night: false, early: false, comeback: false };
    let dur = 0;
    for (const v of Object.values(days)) {
      st.words += v.words; st.sentences += v.sentences; st.activations += v.activations; dur += v.duration_ms;
      st.day = Math.max(st.day, v.words);
    }
    st.spoken = Math.floor(dur / 1000);
    st.saved = Math.max(0, Math.round(st.words / typingWPM() - dur / 60000));
    const active = Object.keys(days).filter((k) => days[k].words > 0).sort();
    let win = [], prev = null;
    for (const k of active) {
      const d = parseDay(k);
      if (prev && daysBetween(prev, d) >= 30) st.comeback = true;
      win.push([d, days[k].words]);
      while (win.length && (d - win[0][0]) / 3600000 > 6 * 24) win.shift();
      st.week = Math.max(st.week, win.reduce((a, x) => a + x[1], 0));
      prev = d;
    }
    st.streak = longestStreak(active.map(parseDay));
    const langs = new Set();
    for (const e of history) {
      if (e.language && e.language !== "auto") langs.add(e.language);
      const h = new Date(e.timestamp).getHours();
      if (h < 5) st.night = true; else if (h < 7) st.early = true;
    }
    st.languages = langs.size;
    return st;
  }
  function achievements() {
    const st = achievementInputs();
    const value = (g) => ({ words: st.words, spoken: st.spoken, saved: st.saved, streak: st.streak, day: st.day, week: st.week,
      activations: st.activations, commands: 0, money: 0 }[g] || 0);
    const earned = (d) => {
      if (d.manual) return false;
      if (d.group === "special") return { first: st.activations > 0, night: st.night, early: st.early, comeback: st.comeback, polyglot: st.languages >= 3 }[d.flag] || false;
      return value(d.group) >= d.threshold;
    };
    let changed = false;
    for (const d of ACH) if (earned(d) && !unlocked[d.id]) { unlocked[d.id] = Date.now(); changed = true; }
    if (changed) write(LS_UNLOCKED, unlocked);
    return { currency: "eur", savings: 0, months: 0, sub_monthly: 0, images: ACH_IMAGES, animated: ACH_ANIMATED,
      achievements: ACH.map((d) => {
        const item = { id: d.id, group: d.group, icon: d.icon, name: d.name, desc: d.desc, threshold: d.threshold || 0,
          value: value(d.group), earned: d.manual ? !!unlocked[d.id] : earned(d), secret: !!d.secret, manual: !!d.manual };
        if (unlocked[d.id]) item.unlocked_at = unlocked[d.id];
        return item;
      }) };
  }
  let ACH_IMAGES = [], ACH_ANIMATED = [];

  // ---- the routes ----
  const ok = (extra) => Object.assign({ ok: true }, extra || {});
  class Unsupported extends Error {}
  const helperOnly = () => { throw new Unsupported("This needs the Vito app on your computer."); };

  async function handle(method, path, body) {
    await ready;
    const u = new URL(path, location.href), q = u.searchParams, p = u.pathname.replace(/^.*?\/api\//, "/api/");
    const m = (re) => re.exec(p);
    let r;
    if (p === "/api/status") return { state, last_timings: lastTimings, boot: "web", web: true };
    if (p === "/api/config") {
      if (method === "GET") return cfg;
      const keep = cfg.ui && cfg.ui.dashboard;
      cfg = merge(structuredClone(DEFAULTS), body || {});
      cfg.ui = cfg.ui || {}; if (keep !== undefined) cfg.ui.dashboard = keep;
      cfg.stt.provider = "whistle"; cfg.stt.model = "whistle"; cfg.cleanup.enabled = false;
      saveConfig(); pruneHistory();
      return ok();
    }
    if (p === "/api/ui/dashboard") { cfg.ui.dashboard = body; saveConfig(); return ok(); }
    if (p === "/api/welcome-done") { cfg.ui.welcome_done = true; saveConfig(); return ok(); }
    if (p === "/api/toggle") { if (state === "idle") await start(); else if (state === "recording") await stop(); return ok({ state }); }
    if (p === "/api/start") { await start(); return ok(); }
    if (p === "/api/stop") { await stop(); return ok(); }
    if (p === "/api/cancel") { cancel(); return ok(); }
    if (p === "/api/about") return { name: "Vito", tagline: "Voice In, Text Out", version: "web", license: "MIT", api: 0, ui: { source: "web", api: 0 }, web: true };
    if (p === "/api/update") return { current: "web", checking: false, can_apply: false, web: true };
    if (p === "/api/stats") return stats(q);
    if (p === "/api/costs") return { currency: "eur", fx_rate: 1, month_stt: 0, month_cleanup: 0, month_command: 0, month_total: 0, period_total: 0, projected_monthly: 0 };
    if (p === "/api/achievements") return achievements();
    if ((r = m(/^\/api\/achievements\/([^/]+)$/))) {
      const d = ACH.find((x) => x.id === decodeURIComponent(r[1]) && x.manual);
      if (!d) throw new Error("not a self-checkable achievement");
      if (body && body.earned) unlocked[d.id] = Date.now(); else delete unlocked[d.id];
      write(LS_UNLOCKED, unlocked);
      return ok();
    }
    if (p === "/api/history") {
      if (method === "DELETE") { history = []; pruneHistory(); return ok(); }
      const needle = (q.get("q") || "").toLowerCase(), fav = q.get("fav") === "1";
      let items = history.filter((e) => (!fav || e.favorite) && (!needle || (e.raw + " " + (e.cleaned || "")).toLowerCase().includes(needle)));
      if (needle) items = items.filter((e) => e.favorite).concat(items.filter((e) => !e.favorite));
      const limit = Math.max(1, parseInt(q.get("limit"), 10) || 100), offset = Math.max(0, parseInt(q.get("offset"), 10) || 0);
      return { total: items.length, items: items.slice(offset, offset + limit) };
    }
    if ((r = m(/^\/api\/history\/([^/]+)$/))) { history = history.filter((e) => e.id !== r[1]); pruneHistory(); return ok(); }
    if ((r = m(/^\/api\/history\/([^/]+)\/favorite$/))) {
      const e = history.find((x) => x.id === r[1]);
      if (e) { e.favorite = !!(body && body.favorite); pruneHistory(); }
      return ok();
    }
    if ((r = m(/^\/api\/history\/([^/]+)\/inject$/))) {
      const e = history.find((x) => x.id === r[1]);
      if (!e) throw new Error("entry not found");
      await navigator.clipboard.writeText(e.cleaned || e.raw);
      emit({ type: "copied" });
      return ok();
    }
    if (p === "/api/whistle") return whistle;
    if (p === "/api/whistle/install") { startWorker(); return ok(); }
    if (p === "/api/whistle/remove") {
      cancel();
      if (worker) { worker.terminate(); worker = null; workerReady = null; }
      try { await caches.delete("vito-whistle-model"); } catch {}
      setWhistle({ phase: "absent" });
      return ok();
    }
    if (p === "/api/local-stt") return { phase: "unsupported", installed: false };
    if (p === "/api/privacy") return { enabled: false, until_ms: 0 };
    if (p === "/api/input-level") return { supported: false };
    if (p === "/api/autostart") return { supported: false, enabled: false };
    if (p === "/api/hotkey") return { os: "web", supported: false, toggle: {}, cancel: {}, configurable: false, accessibility: true };
    if (p === "/api/devices") {
      let input = [];
      try {
        input = (await navigator.mediaDevices.enumerateDevices()).filter((d) => d.kind === "audioinput" && d.deviceId && d.deviceId !== "default")
          .map((d) => ({ id: d.deviceId, name: d.label || "Microphone", is_default: false }));
      } catch {}
      return { input, output: [] };
    }
    if (p === "/api/backups") return { backups: [] };
    if (p === "/api/play-sound" || p === "/api/credit/dismiss") return ok();
    if (p === "/api/ui") return { source: "web", api: 0 };
    if (p === "/api/linux-tools") return {};
    return helperOnly();
  }

  // ---- what index.html calls ----
  window.VitoStandalone = {
    ready,
    // api answers like the daemon's HTTP API.
    async api(path, opts = {}) {
      let body;
      if (opts.body && typeof opts.body === "string") { try { body = JSON.parse(opts.body); } catch {} }
      return handle((opts.method || "GET").toUpperCase(), path, body);
    },
    // connect stands in for the WebSocket: fn gets every event.
    connect(fn) { onEvent = fn; ready.then(warmUp); },
    // export is the whole browser state, for handing over to the app.
    export() {
      // Names this browser, so the app can tell a repeat hand-over from a new one.
      let source = read("vito-web-id", "");
      if (!source) { source = newID(); write("vito-web-id", source); }
      return { source, config: cfg, history, days, achievements: unlocked };
    },
    get state() { return state; },
  };

  // internal/history/stopwords.go
  const STOPWORDS = `aan aangezien achter alle alleen allemaal alles also altijd anders
    bent beide best betreft bijna binnen boven daar daarbij daarin daarna daarom daarop daarvan dan dat deze dezelfde dicht dient doen doet
    door dus echt eens eerst eigen eigenlijk elke enige enkel erg even gaan gaat geen geeft geweest gewoon goed graag heb hebben hebt heeft
    hele hier hierbij hierin hierna hiervoor hoe hoewel hun iemand iets ieder jullie jouw kan kijk kijken komen komt kon konden kunnen kunt
    laat laten liever maak maakt maar maken mag meer mijn minder misschien moet moeten mogen naar nadat natuurlijk niet niets noch nodig
    nog nogal omdat onder ongeveer onze ook over overal paar precies sinds steeds terwijl toch toen tot tussen uit vaak van vanaf vanuit
    veel verder vond voor vooral voordat waar waarbij waardoor waarin waarom wanneer want waren was wat welk welke werd werden wie wij
    wil wilde willen word worden wordt zal zeer zelf zich zichzelf zien ziet zij zijn zit zitten zo'n zoals zodat zonder zou zouden zowel
    zullen staat staan stond geval manier ander andere anderen eerste tweede beetje weer heel helemaal keer zeggen zegt gezegd moment echter
    about above after again against all also although always another anything around back because been before being below between both
    cannot could couldn't didn't does doesn't doing don't done down during each else even ever every from further going gonna have
    haven't having here hers herself himself into isn't it's itself just know like make many might more most much must myself need never
    only other ought ours ourselves over really same shall she'll should shouldn't since some something still such than that that's their
    theirs them themselves then there there's these they they're thing things think this those though through very want wasn't well were
    weren't what what's when where which while will with without won't would wouldn't your yours yourself yourselves yeah okay`;
})();
