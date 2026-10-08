// Whistle in the browser: Cactus Compute's speech model running as WebAssembly
// in this worker, so the page stays responsive while it transcribes.
//
// It mirrors the daemon's whistleStream (internal/stt/whistle_stream.go): the
// recording is cut at its pauses, each finished segment is transcribed once,
// and the segment still being spoken is transcribed again every so often for
// the live text. The engine takes at most 30 s per call and is single-threaded
// here, so whole phrases once each is what keeps it ahead of the speaker — its
// own streaming mode re-transcribes everything every second and falls behind.
//
// Messages in:  {type:"load"} | {type:"start", lang, keywords} |
//               {type:"audio", pcm: Float32Array (16 kHz mono)} | {type:"stop"} | {type:"abort"}
// Messages out: {type:"progress", done, total} | {type:"ready"} | {type:"error", error} |
//               {type:"partial", text} | {type:"final", text, language}
"use strict";

// Pinned like the daemon pins them (internal/whistle/assets.go): the same
// model revision, checked against the same digest before it is used.
const MODEL_URL = "https://huggingface.co/Cactus-Compute/whistle/resolve/b358ddadd89b7a713b5aa131f23032d3cca1b251/whistle.cact";
const MODEL_SHA256 = "b6e02f048568ac5d01a2042556c658061e699acbc0aa2a1439f52f3d461dffeb";
const MODEL_SIZE = 16919407;
const CACHE = "vito-whistle-model";

const RATE = 16000;
const FRAME = RATE * 30 / 1000;        // 30 ms
const PAUSE_FRAMES = 15;               // 450 ms of quiet ends a segment...
const MIN_SEG_FRAMES = 50;             // ...once it is at least 1.5 s long
const MAX_SEG_FRAMES = 22 * 1000 / 30 | 0; // never longer than 22 s
const LOOK_FRAMES = 4 * 1000 / 30 | 0; // a forced cut lands in the quietest frame of the last 4 s
const LOOK_EVERY = 1200;               // ms between looks at the open segment
const MIN_LOOK = RATE * 3 / 2;         // ...once it holds 1.5 s

let M = null, loading = null;

async function sha256Hex(buf) {
  const d = await crypto.subtle.digest("SHA-256", buf);
  return [...new Uint8Array(d)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// fetchModel returns the model bytes: from the cache when an earlier visit
// stored a copy that still checks out, otherwise downloaded with progress.
async function fetchModel() {
  let cache = null;
  try { cache = await caches.open(CACHE); } catch {}
  if (cache) {
    const hit = await cache.match(MODEL_URL);
    if (hit) {
      const buf = await hit.arrayBuffer();
      if (await sha256Hex(buf) === MODEL_SHA256) return buf;
      await cache.delete(MODEL_URL);
    }
  }
  const resp = await fetch(MODEL_URL);
  if (!resp.ok) throw new Error("model download failed: " + resp.status);
  const total = +resp.headers.get("content-length") || MODEL_SIZE;
  const reader = resp.body.getReader();
  const buf = new Uint8Array(total);
  let done = 0, lastPost = 0;
  for (;;) {
    const { value, done: end } = await reader.read();
    if (end) break;
    if (done + value.length > buf.length) throw new Error("model is larger than expected");
    buf.set(value, done);
    done += value.length;
    if (Date.now() - lastPost > 100) { postMessage({ type: "progress", done, total }); lastPost = Date.now(); }
  }
  const model = buf.subarray(0, done);
  if (await sha256Hex(model) !== MODEL_SHA256) throw new Error("the downloaded model does not match its checksum");
  if (cache) {
    try { await cache.put(MODEL_URL, new Response(model, { headers: { "content-type": "application/octet-stream" } })); } catch {}
  }
  return model.buffer.slice(model.byteOffset, model.byteOffset + model.byteLength);
}

function load() {
  if (loading) return loading;
  loading = (async () => {
    importScripts("needle.js");
    const mod = await createNeedle({ locateFile: (p) => p });
    const model = new Uint8Array(await fetchModel());
    const p = mod._malloc(model.length);
    mod.HEAPU8.set(model, p);
    const r = mod._needle_load(p, BigInt(model.length));
    mod._free(p);
    if (r < 0) throw new Error("model failed to load: " + mod.UTF8ToString(mod._needle_last_error()));
    M = mod;
  })();
  loading.catch(() => { loading = null; });
  return loading;
}

function cstr(s) {
  if (!s) return 0;
  const b = new TextEncoder().encode(s + "\0");
  const p = M._malloc(b.length);
  M.HEAPU8.set(b, p);
  return p;
}

// transcribe runs the engine over pcm (Float32Array, at most 30 s).
function transcribe(pcm, lang, keywords) {
  const n = Math.min(pcm.length, RATE * 30);
  const fp = M._malloc(n * 4);
  new Float32Array(M.HEAPU8.buffer, fp, n).set(pcm.subarray(0, n));
  const cap = 1 << 16, out = M._malloc(cap);
  const lp = cstr(lang), kp = cstr(keywords);
  try {
    const r = M._needle_transcribe(fp, n, lp, kp, 0, out, cap);
    if (r < 0) throw new Error(M.UTF8ToString(M._needle_last_error()));
    const j = JSON.parse(M.UTF8ToString(out));
    return { text: (j.text || "").trim(), language: j.language || "" };
  } finally {
    M._free(fp); M._free(out);
    if (lp) M._free(lp);
    if (kp) M._free(kp);
  }
}

// ---- segmenter: a port of internal/whistle/segment.go, on float samples ----
function Segmenter() {
  this.pending = new Float32Array(0);
  this.start = 0;       // sample offset where the open segment begins
  this.levels = [];
  this.floor = -60;
  this.speech = false;
  this.quietRun = 0;
}
Segmenter.prototype.add = function (pcm) {
  const cuts = [];
  const data = new Float32Array(this.pending.length + pcm.length);
  data.set(this.pending); data.set(pcm, this.pending.length);
  const n = Math.floor(data.length / FRAME) * FRAME;
  for (let off = 0; off < n; off += FRAME) {
    let sum = 0;
    for (let i = off; i < off + FRAME; i++) sum += data[i] * data[i];
    const db = sum === 0 ? -100 : 10 * Math.log10(sum / FRAME);
    if (db < this.floor) this.floor = db; else this.floor += 0.05;
    const loud = db > Math.max(this.floor + 12, -55);
    this.levels.push(db);
    if (loud) { this.speech = true; this.quietRun = 0; } else this.quietRun++;
    if (this.speech && this.quietRun >= PAUSE_FRAMES && this.levels.length >= MIN_SEG_FRAMES) {
      cuts.push(this.cut(this.levels.length - (this.quietRun >> 1)));
    } else if (!this.speech && this.levels.length > 70) {
      const drop = this.levels.length - 17;
      this.start += drop * FRAME;
      this.levels = this.levels.slice(drop);
    } else if (this.levels.length >= MAX_SEG_FRAMES) {
      let q = this.levels.length - 1, lo = Infinity;
      for (let i = this.levels.length - LOOK_FRAMES; i < this.levels.length; i++) {
        if (this.levels[i] < lo) { q = i; lo = this.levels[i]; }
      }
      cuts.push(this.cut(q + 1));
    }
  }
  this.pending = data.slice(n);
  return cuts;
};
Segmenter.prototype.cut = function (f) {
  const at = this.start + f * FRAME;
  const rest = this.levels.slice(f);
  this.start = at; this.levels = rest;
  this.speech = rest.some((db) => db > Math.max(this.floor + 12, -55));
  this.quietRun = 0;
  return at;
};

// ---- one dictation ----
let S = null;

function newSession(lang, keywords) {
  return { lang: lang || "", keywords: (keywords || []).join("\n"), pcm: new Float32Array(RATE * 60), len: 0,
    seg: new Segmenter(), cutAt: 0, queue: [], texts: [], openText: "", lastLook: 0,
    finished: false, langSeen: "", scheduled: false };
}

function append(s, pcm) {
  if (s.len + pcm.length > s.pcm.length) {
    const grown = new Float32Array(Math.max(s.pcm.length * 2, s.len + pcm.length));
    grown.set(s.pcm.subarray(0, s.len));
    s.pcm = grown;
  }
  s.pcm.set(pcm, s.len);
  s.len += pcm.length;
}

const joined = (s) => [...s.texts, s.openText].filter(Boolean).join(" ");

// pump does one transcription — a finished segment first, else a look at the
// open one when it is due — and schedules itself again while there is work.
// Running from a timer lets audio that arrived meanwhile be taken in first.
function schedule(s) {
  if (s.scheduled) return;
  s.scheduled = true;
  setTimeout(() => { s.scheduled = false; pump(s); }, 0);
}

function pump(s) {
  if (S !== s) return;
  if (s.queue.length) {
    const [a, b] = s.queue.shift();
    try {
      const r = transcribe(s.pcm.subarray(a, b), s.lang, s.keywords);
      if (r.text) s.texts.push(r.text);
      if (r.language) s.langSeen = r.language;
    } catch (e) { postMessage({ type: "error", error: String(e.message || e) }); }
    s.openText = "";
    const live = joined(s);
    if (live && !s.finished) postMessage({ type: "partial", text: live });
    schedule(s);
    return;
  }
  if (s.finished) {
    postMessage({ type: "final", text: s.texts.join(" "), language: s.langSeen });
    S = null;
    return;
  }
  const open = s.len - s.seg.start;
  if (s.seg.speech && open >= MIN_LOOK && Date.now() - s.lastLook >= LOOK_EVERY) {
    s.lastLook = Date.now();
    try {
      const r = transcribe(s.pcm.subarray(s.seg.start, s.len), s.lang, s.keywords);
      s.openText = r.text;
      if (r.language) s.langSeen = r.language;
    } catch {}
    const live = joined(s);
    if (live) postMessage({ type: "partial", text: live });
  }
}

onmessage = async (ev) => {
  const m = ev.data;
  switch (m.type) {
    case "load":
      try { await load(); postMessage({ type: "ready" }); }
      catch (e) { postMessage({ type: "error", error: String(e.message || e), fatal: true }); }
      break;
    case "start":
      S = newSession(m.lang, m.keywords);
      break;
    case "audio": {
      const s = S;
      if (!s || s.finished || !M) break;
      append(s, m.pcm);
      for (const c of s.seg.add(m.pcm)) {
        if (c > s.cutAt) { s.queue.push([s.cutAt, c]); s.cutAt = c; }
      }
      schedule(s);
      break;
    }
    case "stop": {
      const s = S;
      if (!s) { postMessage({ type: "final", text: "", language: "" }); break; }
      // What is left after the last pause is the final segment.
      if (s.len - s.cutAt >= RATE * 2 / 5) { s.queue.push([s.cutAt, s.len]); s.cutAt = s.len; }
      s.finished = true;
      s.openText = "";
      schedule(s);
      break;
    }
    case "abort":
      S = null;
      break;
  }
};
