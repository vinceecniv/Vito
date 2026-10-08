// Vito in the browser, without the helper: this file stands in for the daemon.
//
// The interface talks to the daemon through api("/api/...") and a WebSocket of
// events. When the page is opened from the website (vito.talk/app) there is no
// daemon, so index.html routes both here instead: the same routes, the same
// JSON shapes, answered from localStorage, with Whistle running as WebAssembly
// in a worker (whistle-worker.js) for the speech recognition.
//
// What needs the helper — a global hotkey, typing into other apps, the better
// local models, cloud speech services, sync — answers as unsupported, and
// index.html hides those settings. AI cleanup and Vito Assist do work: the
// providers accept calls straight from a browser with the user's own key.
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

  let DEFAULTS = null, ACH = [], cfg = null, CLEANUP = {};
  let privacyUntil = read("vito-web-privacy", 0); // ms; -1 = until switched off
  let history = read(LS_HISTORY, []);   // newest first
  let days = read(LS_DAYS, {});         // "yyyy-mm-dd" -> {words, sentences, activations, duration_ms}
  let unlocked = read(LS_UNLOCKED, {}); // id -> unix ms

  const ready = (async () => {
    const [d, a, c] = await Promise.all([
      fetch(BASE + "defaults.json").then((r) => r.json()),
      fetch(BASE + "achievements.json").then((r) => r.json()).catch(() => ({})),
      fetch(BASE + "cleanup.json").then((r) => r.json()).catch(() => ({})),
    ]);
    DEFAULTS = d; ACH = a.list || []; ACH_IMAGES = a.images || []; ACH_ANIMATED = a.animated || [];
    CLEANUP = c;
    const saved = read(LS_CONFIG, {});
    cfg = merge(structuredClone(DEFAULTS), saved);
    // What the browser can do, whatever a saved config says — decided before
    // anything is saved below. A first visit starts on Whistle: it needs no
    // account (the app's own default, Soniox, needs a key). A service without
    // its key or address falls back to Whistle too: an early version of this
    // page saved Soniox for visitors who never chose it.
    const st = cfg.stt;
    if (!(saved.stt && saved.stt.provider) || !BROWSER_STT.includes(st.provider)
        || (st.provider === "soniox" && !(st.soniox_api_key || "").trim())
        || (st.provider === "openai" && !(st.openai_base_url || "").trim())) { st.provider = "whistle"; st.model = "whistle"; }
    // A first visit speaks the browser's preferred language when Whistle knows
    // it, and English otherwise — saying so, since the user may not expect it.
    if (!(saved.stt && saved.stt.language)) {
      // Of the browser's languages that Whistle knows, one other than English
      // goes first: listing both means English is the second language.
      const prefs = [...new Set((navigator.languages || [navigator.language || "en"]).map((l) => String(l).slice(0, 2).toLowerCase()))];
      const known = prefs.filter((c) => WHISTLE_LANGS.includes(c));
      cfg.stt.language = known.find((c) => c !== "en") || known[0] || "en";
      if (prefs[0] && !WHISTLE_LANGS.includes(prefs[0])) unsupportedLang = prefs[0];
      saveConfig();
    }
    cfg.history.store_audio = false;
    if (!WHISTLE_LANGS.includes(cfg.stt.language)) cfg.stt.language = "en";
  })();

  const WHISTLE_LANGS = ["nl", "en", "de", "fr", "es", "it", "pl"];
  let unsupportedLang = "";

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
      if (await c.match(MODEL_URL)) { whistle = { phase: "ready" }; startWorker(); return; }
    } catch {}
    // Not here yet: fetch it when Whistle is the engine in use. Someone on
    // Soniox doesn't need 18 MB they will never run.
    if (cfg.stt.provider === "whistle") startWorker();
  }

  // ---- recording ----
  let state = "idle", rec = null, lastTimings = {}, gestureWindow = null;
  const setState = (s) => { state = s; emit({ type: "state", state: s }); };

  const WORKLET = `class P extends AudioWorkletProcessor{constructor(){super();this.r=sampleRate/${RATE};this.a=0;this.s=0;this.n=0;this.b=new Float32Array(1600);this.l=0;this.p=0}
process(i){const c=i[0]&&i[0][0];if(!c)return true;for(let k=0;k<c.length;k++){const v=c[k];this.s+=v;this.n++;this.a+=1;const m=v<0?-v:v;if(m>this.p)this.p=m;if(this.a>=this.r){this.a-=this.r;this.b[this.l++]=this.s/this.n;this.s=0;this.n=0;if(this.l===this.b.length){this.port.postMessage({pcm:this.b,peak:this.p},[this.b.buffer]);this.b=new Float32Array(1600);this.l=0;this.p=0}}}return true}}
registerProcessor("vito-pcm",P)`;

  // ---- speech engines ----
  // Each takes 16 kHz float audio while you speak and gives the text when you
  // stop: { audio(pcm), stop() -> Promise<{text, language}>, abort() }.
  // Live text goes out as "partial" events on the way.
  const BROWSER_STT = ["whistle", "soniox", "openai"];
  const partial = (text) => { if (state === "recording" && text) emit({ type: "partial", text }); };
  const toS16 = (pcm) => {
    const out = new Int16Array(pcm.length);
    for (let i = 0; i < pcm.length; i++) { const v = Math.max(-1, Math.min(1, pcm[i])); out[i] = v < 0 ? v * 32768 : v * 32767; }
    return out;
  };

  // Whistle: the model in the worker (whistle-worker.js). Audio that arrives
  // while the model is still downloading is kept until it is there.
  function whistleEngine() {
    startWorker();
    const buffered = [];
    let done = null;
    worker.postMessage({ type: "start", lang: cfg.stt.language || "", keywords: cfg.stt.keyterms_enabled === false ? [] : keyterms() });
    const send = (pcm) => worker.postMessage({ type: "audio", pcm }, [pcm.buffer]);
    const eng = {
      audio(pcm) {
        if (whistle.phase === "ready") { buffered.splice(0).forEach(send); send(pcm); } else buffered.push(pcm);
      },
      async stop() {
        await workerReady;
        buffered.splice(0).forEach(send);
        return new Promise((resolve) => { done = resolve; worker.postMessage({ type: "stop" }); });
      },
      abort() { worker.postMessage({ type: "abort" }); },
      onWorker(m) {
        if (m.type === "partial") partial(m.text);
        else if (m.type === "final" && done) { const d = done; done = null; d({ text: m.text || "", language: m.language || cfg.stt.language }); }
      },
    };
    return eng;
  }

  // Soniox: its realtime WebSocket, as internal/stt/soniox_stream.go. The key
  // travels in the first message, which is what makes it usable from a page.
  function sonioxEngine() {
    const ws = new WebSocket("wss://stt-rt.soniox.com/transcribe-websocket");
    ws.binaryType = "arraybuffer";
    let final = "", tail = "", queue = [], failed = null, finished = null;
    const langs = {};
    const start = { api_key: (cfg.stt.soniox_api_key || "").trim(), model: "stt-rt-v5", audio_format: "pcm_s16le",
      sample_rate: RATE, num_channels: 1, enable_language_identification: true };
    if (cfg.stt.language && cfg.stt.language !== "auto") start.language_hints = [cfg.stt.language];
    const end = () => { if (finished) { const f = finished; finished = null; f(); } };
    ws.onopen = () => { ws.send(JSON.stringify(start)); queue.forEach((b) => ws.send(b)); queue = null; };
    ws.onmessage = (ev) => {
      let m; try { m = JSON.parse(ev.data); } catch { return; }
      if (m.error_message) {
        failed = looksLikeCredit(m.error_code === 402 ? 402 : 400, m.error_message) ? new CreditError("Soniox", m.error_message) : new Error("Soniox: " + m.error_message);
        end(); return;
      }
      let t = "";
      for (const tk of m.tokens || []) {
        if (tk.is_final) { final += tk.text; if (tk.language && tk.text.trim()) langs[tk.language] = (langs[tk.language] || 0) + 1; }
        else t += tk.text;
      }
      tail = t;
      partial((final + tail).trim());
      if (m.finished) end();
    };
    ws.onerror = () => { failed = failed || new Error("Soniox can't be reached."); end(); };
    ws.onclose = () => end();
    return {
      audio(pcm) { const b = toS16(pcm).buffer; if (queue) queue.push(b); else if (ws.readyState === 1) ws.send(b); },
      stop() {
        return new Promise((resolve, reject) => {
          const timer = setTimeout(() => { failed = failed || new Error("Soniox didn't answer in time."); end(); }, 8000);
          finished = () => {
            clearTimeout(timer);
            try { ws.close(); } catch {}
            if (failed) return reject(failed);
            const language = Object.entries(langs).sort((a, b) => b[1] - a[1]).map((x) => x[0])[0] || cfg.stt.language;
            resolve({ text: (final + tail).trim(), language });
          };
          if (failed) return finished();
          // An empty TEXT frame ends the stream; an empty binary one is ignored.
          const send = () => ws.send("");
          if (ws.readyState === 1 && !queue) send(); else ws.addEventListener("open", () => setTimeout(send, 0), { once: true });
        });
      },
      abort() { try { ws.close(); } catch {} },
    };
  }

  // An OpenAI-compatible endpoint (Groq, OpenAI, a server of your own that
  // allows the page): the whole recording as WAV once you stop, as
  // internal/stt/openai.go. No live text.
  function openaiEngine() {
    const chunks = [];
    return {
      audio(pcm) { chunks.push(toS16(pcm)); },
      async stop() {
        const n = chunks.reduce((a, c) => a + c.length, 0);
        const wav = new DataView(new ArrayBuffer(44 + n * 2));
        const str = (o, s) => { for (let i = 0; i < s.length; i++) wav.setUint8(o + i, s.charCodeAt(i)); };
        str(0, "RIFF"); wav.setUint32(4, 36 + n * 2, true); str(8, "WAVE"); str(12, "fmt "); wav.setUint32(16, 16, true);
        wav.setUint16(20, 1, true); wav.setUint16(22, 1, true); wav.setUint32(24, RATE, true); wav.setUint32(28, RATE * 2, true);
        wav.setUint16(32, 2, true); wav.setUint16(34, 16, true); str(36, "data"); wav.setUint32(40, n * 2, true);
        let o = 44; for (const c of chunks) for (let i = 0; i < c.length; i++, o += 2) wav.setInt16(o, c[i], true);
        const fd = new FormData();
        fd.append("file", new Blob([wav.buffer], { type: "audio/wav" }), "dictation.wav");
        fd.append("response_format", "json");
        if ((cfg.stt.openai_model || "").trim()) fd.append("model", cfg.stt.openai_model.trim());
        if (cfg.stt.language && cfg.stt.language !== "auto") fd.append("language", cfg.stt.language);
        if (cfg.stt.keyterms_enabled !== false && keyterms().length) fd.append("prompt", keyterms().join(", "));
        const key = (cfg.stt.openai_key || "").trim();
        let resp;
        try {
          resp = await fetch((cfg.stt.openai_base_url || "").trim().replace(/\/+$/, "") + "/audio/transcriptions",
            { method: "POST", body: fd, headers: key ? { Authorization: "Bearer " + key } : {}, signal: AbortSignal.timeout(90000) });
        } catch { throw new Error("The speech endpoint can't be reached from the browser."); }
        const body = await resp.json().catch(() => ({}));
        if (!resp.ok) {
          const msg = (body.error && body.error.message) || "";
          if (looksLikeCredit(resp.status, msg)) throw new CreditError(sttName(), msg);
          throw new Error(sttName() + ": HTTP " + resp.status + (msg ? " — " + msg : ""));
        }
        return { text: (body.text || "").trim(), language: body.language || cfg.stt.language };
      },
      abort() {},
    };
  }

  function newEngine() {
    switch (cfg.stt.provider) {
      case "soniox":
        if (!(cfg.stt.soniox_api_key || "").trim()) throw new Error("Enter your Soniox API key under Settings → Speech recognition.");
        return sonioxEngine();
      case "openai":
        if (!(cfg.stt.openai_base_url || "").trim()) throw new Error("Enter the speech endpoint under Settings → Speech recognition.");
        return openaiEngine();
      default:
        return whistleEngine();
    }
  }
  let engine = null;

  async function start() {
    if (state !== "idle") return;
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) throw new Error("This browser cannot record audio.");
    setState("recording");
    playSound("start");
    try {
      // The window the user acted in (the floating window, or this tab).
      const w = gestureWindow && !gestureWindow.closed ? gestureWindow : window;
      const stream = await w.navigator.mediaDevices.getUserMedia({ audio: {
        deviceId: cfg.audio.input_device ? { ideal: cfg.audio.input_device } : undefined,
        echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
      const ctx = new w.AudioContext();
      if (ctx.state === "suspended") ctx.resume().catch(() => {});
      const url = w.URL.createObjectURL(new w.Blob([WORKLET], { type: "text/javascript" }));
      await ctx.audioWorklet.addModule(url);
      w.URL.revokeObjectURL(url);
      const src = ctx.createMediaStreamSource(stream);
      const node = new w.AudioWorkletNode(ctx, "vito-pcm");
      engine = newEngine();
      const r = { stream, ctx, node, engine, started: performance.now(), samples: 0, lastLoud: performance.now(), heard: false };
      rec = r;
      node.port.onmessage = (ev) => {
        if (rec !== r) return;
        const { pcm, peak } = ev.data;
        r.samples += pcm.length;
        const db = peak > 0 ? 20 * Math.log10(peak) : -100;
        emit({ type: "level", level: Math.max(0, Math.min(100, Math.round((db + 60) / 60 * 100))), clip: peak >= 0.99 });
        if (db > -40) { r.lastLoud = performance.now(); r.heard = true; }
        r.engine.audio(pcm);
        // Auto-stop after a pause, when switched on, as the daemon does.
        if (cfg.audio.auto_stop && r.heard && performance.now() - r.lastLoud > (cfg.audio.auto_stop_silence_ms || 1200)) stop();
        else if (performance.now() - r.lastLoud > (cfg.audio.silence_timeout_sec || 15) * 1000 && !r.heard) cancel();
      };
      src.connect(node);
      // Nothing to hear from the node; connecting it keeps the graph running.
      node.connect(ctx.destination);
    } catch (e) {
      if (rec) release(rec);
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
    let res;
    try { res = await r.engine.stop(); markCredit(sttName(), false); }
    catch (e) {
      playSound("cancel"); setState("idle");
      if (e instanceof CreditError) { markCredit(e.provider, true); emit({ type: "error", error: e.message, credit: e.provider }); }
      else emit({ type: "error", error: (e && e.message) || String(e) });
      return;
    }
    finish(r, res.text || "", res.language || cfg.stt.language);
  }

  function cancel() {
    const r = rec;
    rec = null;
    if (r) { release(r); playSound("cancel"); r.engine.abort(); }
    if (state !== "idle") setState("idle");
  }

  function onWorker(m) {
    if (m.type === "error") console.warn("whistle:", m.error);
    else if (engine && engine.onWorker) engine.onWorker(m);
  }

  async function finish(r, text, language) {
    const sttMS = performance.now() - r.stopped;
    let raw = plain(applyDictionary(text));
    if (!raw.trim()) {
      setState("idle");
      playSound("cancel");
      emit({ type: "error", error: "No speech recognised." });
      return;
    }
    // Vito Assist, as in the daemon (internal/daemon finish): "Vito, …" arms
    // the next dictation — or, about the clipboard, runs on it right away.
    const cl = cfg.cleanup || {};
    const cleanupOn = cl.enabled && cleanupConfigured(cl);
    let instruction = "", clipboardIn = false;
    const cmd = parseCommand(raw);
    if (cmd) {
      if (!cleanupOn) {
        playSound("cancel"); setState("idle");
        emit({ type: "error", error: "Vito Assist needs AI cleanup: switch it on under Settings → AI cleanup." });
        return;
      }
      if (/klembord|clipboard/i.test(cmd)) {
        let clip = "";
        try { clip = (await navigator.clipboard.readText()).trim(); }
        catch { playSound("cancel"); setState("idle"); emit({ type: "error", error: "The browser didn't let Vito read the clipboard." }); return; }
        if (!clip) { playSound("cancel"); setState("idle"); emit({ type: "error", error: "There is no text on the clipboard for this command." }); return; }
        playSound("command");
        emit({ type: "command", command: cmd, command_received: true });
        raw = clip; instruction = cmd; clipboardIn = true;
      } else {
        pendingCmd = cmd;
        playSound("command");
        emit({ type: "command", command: cmd });
        setState("idle");
        // The microphone reopens by itself for the text the command is about.
        setTimeout(() => { start().catch(() => {}); }, 350);
        return;
      }
    } else { instruction = pendingCmd; pendingCmd = ""; }

    // AI cleanup when it is set up, as the daemon does it; the rules when not,
    // or when it fails — the text always arrives. A command runs even below the
    // word threshold, on Assist's own model when it has one.
    let cleaned = "", cleanupErr = "", cleanupMS = 0, cleanupCredit = "";
    let useCfg = cl;
    if (instruction && cfg.assist && cfg.assist.use_cleanup_model === false) {
      useCfg = Object.assign({}, cfg.assist.cleanup, { enabled: true, timeout_ms: (cfg.assist.cleanup && cfg.assist.cleanup.timeout_ms) || cl.timeout_ms });
    }
    if (cleanupOn && (instruction || countWords(raw) >= (cl.min_words || 0))) {
      const t0 = performance.now();
      try { cleaned = plain(await aiCleanup(raw, language, useCfg, instruction)); markCredit(cleanupName(useCfg), false); }
      catch (e) {
        cleanupErr = (e && e.message) || String(e);
        if (e instanceof CreditError) { cleanupCredit = e.provider; markCredit(e.provider, true); }
      }
      cleanupMS = performance.now() - t0;
    }
    if (!cleaned) raw = ruleCleanup(raw);
    const out = cleaned || raw;
    let copied = false;
    // Writing to the clipboard needs no permission, but the browser wants a
    // recent click or key press — absent after an auto-stop on silence, or a
    // long wait. Then the page offers a button, which is that click.
    try { await navigator.clipboard.writeText(out); copied = true; } catch {}
    if (!copied) emit({ type: "copy-failed", text: out });
    const injectedMS = performance.now() - r.stopped;
    const durMS = Math.round(r.samples / RATE * 1000);
    const ms = (v) => Math.round(v) * 1e6; // Go's time.Duration is nanoseconds
    lastTimings = { recording_ms: ms(durMS), stt_final_ms: ms(sttMS), cleanup_ms: ms(cleanupMS), injected_ms: ms(injectedMS) };
    const entry = record({ raw, cleaned, cleanup_error: cleanupErr, language, duration_ms: durMS, stt_ms: Math.round(sttMS),
      cleanup_ms: Math.round(cleanupMS), injected_ms: Math.round(injectedMS), command_text: instruction, clipboard: clipboardIn });
    playSound(cleanupErr ? "warn" : "done");
    emit({ type: "final", raw, cleaned, text: out, timings: lastTimings, entry_id: entry ? entry.id : "",
      cleanup_failed: !!cleanupErr, cleanup_error: cleanupErr, cleanup_credit: cleanupCredit || undefined });
    setState("idle");
    if (copied) emit({ type: "copied" });
  }

  // parseCommand: internal/daemon parseCommand — the wake word first (heard
  // loosely), then a short instruction; anything longer is ordinary text.
  let pendingCmd = "";
  function parseCommand(raw) {
    const s = raw.trim(), low = s.toLowerCase();
    for (const w of ["vito", "vido", "fito", "veto"]) {
      if (!low.startsWith(w) || s.length <= w.length || !" ,:.!-\t".includes(s[w.length])) continue;
      const instr = s.slice(w.length).replace(/^[\s,:.!-]+/, "").trim();
      if (!instr || instr.split(/\s+/).length > 15) return "";
      return instr;
    }
    return "";
  }

  // ---- out of credit: internal/apierr and the daemon's bookkeeping ----
  // A provider whose balance ran out is told apart from any other failure, so
  // the page can say "top up" instead of a raw error. It stays flagged until a
  // request to it succeeds again; a dismissal hides it until it runs out anew.
  const BILLING_WORDS = ["insufficient", "credit balance", "out of credit", "no credit", "insufficient funds",
    "billing", "payment required", "top up", "top-up"];
  const looksLikeCredit = (status, body) => status === 402 ||
    (status >= 400 && status < 500 && BILLING_WORDS.some((w) => String(body || "").toLowerCase().includes(w)));
  class CreditError extends Error { constructor(provider, detail) { super(provider + " account is out of credit" + (detail ? ": " + detail : "")); this.provider = provider; } }
  const creditOut = new Set(read("vito-web-credit", [])), creditHush = new Set(read("vito-web-credit-hush", []));
  const saveCredit = () => { write("vito-web-credit", [...creditOut]); write("vito-web-credit-hush", [...creditHush]); };
  function markCredit(provider, out) {
    if (!provider) return;
    const changed = out ? !creditOut.has(provider) : creditOut.has(provider);
    if (out) { creditOut.add(provider); creditHush.delete(provider); } else { creditOut.delete(provider); creditHush.delete(provider); }
    if (changed) { saveCredit(); emit({ type: "credit" }); }
  }
  const creditList = () => [...creditOut].filter((p) => !creditHush.has(p));
  const cleanupName = (cl) => {
    if (cl.provider === "anthropic") return "Anthropic";
    const b = (cl.openai_base_url || "").toLowerCase();
    return b.includes("groq.com") ? "Groq" : b.includes("openai.com") ? "OpenAI"
      : /localhost|127\.0\.0\.1|0\.0\.0\.0/.test(b) ? "Local model" : "AI cleanup";
  };
  const sttName = () => {
    if (cfg.stt.provider === "soniox") return "Soniox";
    if (cfg.stt.provider === "whistle") return "Whistle";
    const b = (cfg.stt.openai_base_url || "").toLowerCase();
    return b.includes("groq.com") ? "Groq" : b.includes("openai.com") ? "OpenAI"
      : /localhost|127\.0\.0\.1|0\.0\.0\.0/.test(b) ? "Local speech model" : "Speech endpoint";
  };

  // ---- AI cleanup: internal/cleanup, called from the browser ----
  const cleanupConfigured = (cl) => cl.provider === "anthropic" ? !!cl.api_key : !!(cl.openai_base_url && cl.openai_model);
  function systemPrompt(cl, instruction) {
    return instruction ? (CLEANUP.command_prompt || "").replace("{{instruction}}", instruction) : cleanupRules(cl);
  }
  function cleanupRules(cl) {
    const id = cl.active_prompt || "";
    const b = (CLEANUP.builtins || []).find((x) => x.id === id);
    const own = (cl.prompts || []).find((x) => x.id === id);
    const rules = ((b && b.rules) || (own && own.rules) || "").trim() || CLEANUP.default_rules || "";
    return rules + "\n" + (CLEANUP.contract || "");
  }
  function userPrompt(text, language) {
    let u = "Language: " + language + "\n";
    const cs = (cfg.dictionary && cfg.dictionary.corrections) || [];
    if (cs.length) u += "Corrections (misheard -> intended):\n" + cs.map((c) => "- " + JSON.stringify(c.wrong) + " -> " + JSON.stringify(c.right) + "\n").join("");
    return u + "Transcript:\n" + text;
  }
  const maxTokens = (text) => Math.min(4096, Math.floor(text.length / 2) + 2048);
  const stripThinking = (s) => (s || "").replace(/<(think|thinking)>[\s\S]*?<\/(think|thinking)>/g, "").trim();
  async function aiCleanup(text, language, cl, instruction) {
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(), cl.timeout_ms || 5000);
    try {
      let resp, body;
      if (cl.provider === "anthropic") {
        resp = await fetch("https://api.anthropic.com/v1/messages", { method: "POST", signal: ctl.signal,
          headers: { "content-type": "application/json", "x-api-key": cl.api_key, "anthropic-version": "2023-06-01",
            "anthropic-dangerous-direct-browser-access": "true" },
          body: JSON.stringify({ model: cl.model, max_tokens: maxTokens(text), temperature: 0, system: systemPrompt(cl, instruction),
            messages: [{ role: "user", content: userPrompt(text, language) }] }) });
        body = await resp.json().catch(() => ({}));
        if (!resp.ok) {
          const msg = (body.error && body.error.message) || String(resp.status);
          if (looksLikeCredit(resp.status, msg)) throw new CreditError("Anthropic", msg);
          throw new Error("Anthropic: " + msg);
        }
        if (body.stop_reason === "max_tokens") throw new Error("the answer was cut off");
        return stripThinking((body.content || []).filter((b) => b.type === "text").map((b) => b.text).join(""));
      }
      const req = { model: cl.openai_model, temperature: 0, max_tokens: maxTokens(text),
        messages: [{ role: "system", content: systemPrompt(cl, instruction) }, { role: "user", content: userPrompt(text, language) }] };
      if (cl.reasoning_effort) req.reasoning_effort = cl.reasoning_effort;
      resp = await fetch(cl.openai_base_url.replace(/\/+$/, "") + "/chat/completions", { method: "POST", signal: ctl.signal,
        headers: Object.assign({ "Content-Type": "application/json" }, cl.openai_key ? { Authorization: "Bearer " + cl.openai_key } : {}),
        body: JSON.stringify(req) });
      body = await resp.json().catch(() => ({}));
      if (!resp.ok) {
        const msg = (body.error && body.error.message) || "";
        if (looksLikeCredit(resp.status, msg)) throw new CreditError(cleanupName(cl), msg);
        throw new Error("HTTP " + resp.status + ": " + msg);
      }
      const ch = (body.choices || [])[0];
      if (!ch) throw new Error("no answer");
      if (ch.finish_reason === "length") throw new Error("the answer was cut off");
      return stripThinking(ch.message && ch.message.content);
    } catch (e) {
      if (e instanceof CreditError) throw e;
      if (e.name === "AbortError") throw new Error("timed out");
      if (e instanceof TypeError) throw new Error("the provider can't be reached from the browser");
      throw e;
    } finally { clearTimeout(timer); }
  }
  async function testKey(b) {
    const key = (b.key || "").trim();
    let url, headers = {};
    if (b.provider === "soniox") {
      if (!key) return { ok: false, error: "empty" };
      url = "https://api.soniox.com/v1/models";
      headers = { Authorization: "Bearer " + key };
    } else if (b.provider === "anthropic" || b.provider === "cleanup") {
      if (!key) return { ok: false, error: "empty" };
      url = "https://api.anthropic.com/v1/models?limit=1";
      headers = { "x-api-key": key, "anthropic-version": "2023-06-01", "anthropic-dangerous-direct-browser-access": "true" };
    } else if (b.provider === "openai") {
      const base = (b.baseURL || "").trim().replace(/\/+$/, "");
      if (!base) return { ok: false, error: "empty" };
      url = base + "/models";
      if (key) headers.Authorization = "Bearer " + key;
    } else return { ok: false, error: "status", status: 0 };
    try {
      const r = await fetch(url, { headers });
      if (r.ok) return { ok: true };
      if (r.status === 401 || r.status === 403) return { ok: false, error: "unauthorized" };
      return { ok: false, error: "status", status: r.status };
    } catch { return { ok: false, error: "network" }; }
  }

  // ---- feedback sounds (assets/sounds, copied next to this file) ----
  let audioCtx = null;
  const soundBuf = {};
  async function playSound(name, volume) {
    if (volume === undefined && !(cfg.audio && cfg.audio.sounds_enabled)) return;
    try {
      const w = gestureWindow && !gestureWindow.closed ? gestureWindow : window;
      if (!audioCtx || audioCtx.vitoWin !== w) {
        audioCtx = new w.AudioContext(); audioCtx.vitoWin = w;
        for (const k in soundBuf) delete soundBuf[k];
      }
      if (audioCtx.state === "suspended") audioCtx.resume().catch(() => {});
      if (!soundBuf[name]) soundBuf[name] = await fetch(BASE + "sounds/" + name + ".wav").then((r) => r.arrayBuffer()).then((b) => audioCtx.decodeAudioData(b));
      const src = audioCtx.createBufferSource(), gain = audioCtx.createGain();
      gain.gain.value = volume !== undefined ? volume : (cfg.audio.sounds_volume ?? 1);
      src.buffer = soundBuf[name]; src.connect(gain).connect(audioCtx.destination); src.start();
    } catch {}
  }

  // ---- history and the permanent day sums ----
  const newID = () => [...crypto.getRandomValues(new Uint8Array(8))].map((b) => b.toString(16).padStart(2, "0")).join("");
  function record(e) {
    const now = new Date();
    const text = e.cleaned || e.raw;
    const words = countWords(text), sentences = countSentences(text);
    const d = days[dayKey(now)] || (days[dayKey(now)] = { words: 0, sentences: 0, activations: 0, duration_ms: 0 });
    d.words += words; d.sentences += sentences; d.activations++; d.duration_ms += e.duration_ms;
    if (e.command_text) { d.commands = (d.commands || 0) + 1; if (e.clipboard) d.clipboard_commands = (d.clipboard_commands || 0) + 1; }
    write(LS_DAYS, days);
    if (cfg.history && cfg.history.enabled === false) return null;
    if (privacyOn()) return null; // privacy mode: the sums count, the words are not kept
    const entry = { id: newID(), timestamp: now.toISOString(), duration_ms: e.duration_ms, language: e.language || "",
      source: "stream", raw: e.raw, cleaned: e.cleaned || undefined, cleanup_used: !!e.cleaned,
      cleanup_error: e.cleanup_error || undefined, stt_ms: e.stt_ms, cleanup_ms: e.cleanup_ms || 0, injected_ms: e.injected_ms,
      words, sentences, favorite: false, command: !!e.command_text || undefined, command_text: e.command_text || undefined };
    history.unshift(entry);
    pruneHistory();
    return entry;
  }
  const privacyOn = () => privacyUntil === -1 || privacyUntil > Date.now();
  const privacyJSON = () => ({ enabled: privacyOn(), until_ms: privacyUntil > 0 && privacyOn() ? privacyUntil : 0 });
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
    let words = 0, sentences = 0, activations = 0, dur = 0, commands = 0, first = "";
    for (const [k, v] of Object.entries(days)) {
      if ((from && k < from) || k > to) continue;
      words += v.words; sentences += v.sentences; activations += v.activations; dur += v.duration_ms; commands += v.commands || 0;
      if (!first || k < first) first = k;
    }
    return { words, sentences, activations, dur, commands, first };
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
    const st = { period_days: span, words: tot.words, sentences: tot.sentences, activations: tot.activations, commands: tot.commands,
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
    let cmds = 0, clips = 0;
    for (const v of Object.values(days)) { cmds += v.commands || 0; clips += v.clipboard_commands || 0; }
    st.commands = cmds;
    st.wizard = st.activations > cmds && cmds > clips && clips > 0; // all three modes used
    return st;
  }
  function achievements() {
    const st = achievementInputs();
    const value = (g) => ({ words: st.words, spoken: st.spoken, saved: st.saved, streak: st.streak, day: st.day, week: st.week,
      activations: st.activations, commands: st.commands, money: 0 }[g] || 0);
    const earned = (d) => {
      if (d.manual) return false;
      if (d.group === "special") return { first: st.activations > 0, night: st.night, early: st.early, comeback: st.comeback, polyglot: st.languages >= 3, wizard: st.wizard }[d.flag] || false;
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
    if (p === "/api/status") return { state, last_timings: lastTimings, boot: "web", web: true, command: pendingCmd, credit: creditList() };
    if (p === "/api/config") {
      if (method === "GET") return cfg;
      const keep = cfg.ui && cfg.ui.dashboard;
      cfg = merge(structuredClone(DEFAULTS), body || {});
      cfg.ui = cfg.ui || {}; if (keep !== undefined) cfg.ui.dashboard = keep;
      if (!BROWSER_STT.includes(cfg.stt.provider)) { cfg.stt.provider = "whistle"; cfg.stt.model = "whistle"; }
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
    if (p === "/api/privacy") {
      if (method === "PUT") {
        privacyUntil = !(body && body.enabled) ? 0 : body.minutes > 0 ? Date.now() + body.minutes * 60000 : -1;
        write("vito-web-privacy", privacyUntil);
      }
      return privacyJSON();
    }
    if (p === "/api/cleanup/prompts") return { builtins: CLEANUP.builtins || [], contract: CLEANUP.contract || "" };
    if (p === "/api/test-key") return testKey(body || {});
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
    if (p === "/api/play-sound") { const v = parseFloat(q.get("volume")); playSound(q.get("name"), isNaN(v) ? (cfg.audio.sounds_volume ?? 1) : v); return ok(); }
    if (p === "/api/credit/dismiss") {
      let changed = false;
      for (const pr of creditOut) if ((!body || !body.provider || body.provider === pr) && !creditHush.has(pr)) { creditHush.add(pr); changed = true; }
      if (changed) { saveCredit(); emit({ type: "credit" }); }
      return ok();
    }
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
    // The model (18 MB) is fetched once the page itself has loaded, when the
    // browser is idle, so it never competes with the interface.
    connect(fn) {
      onEvent = fn;
      const later = () => ready.then(() => (window.requestIdleCallback || ((f) => setTimeout(f, 800)))(warmUp, { timeout: 3000 }));
      if (document.readyState === "complete") later(); else window.addEventListener("load", later, { once: true });
    },
    // export is the whole browser state, for handing over to the app.
    export() {
      // Names this browser, so the app can tell a repeat hand-over from a new one.
      let source = read("vito-web-id", "");
      if (!source) { source = newID(); write("vito-web-id", source); }
      return { source, config: cfg, history, days, achievements: unlocked };
    },
    get state() { return state; },
    set gestureWindow(w) { gestureWindow = w; },
    // The browser's language when Whistle can't do it, on the first visit only.
    get unsupportedLang() { return unsupportedLang; },
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
