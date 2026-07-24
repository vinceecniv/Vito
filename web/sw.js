// Vito service worker: caches the app shell so the installed PWA loads
// instantly and survives daemon restarts (shows the cached UI + a reconnect
// state instead of a browser error). API and WebSocket traffic is never cached.
// Bumped when cached content changes shape: activate() drops every other cache,
// which is what clears out translations from a previous version of the app.
const CACHE = "vito-v3";
const CORE = ["/", "/manifest.webmanifest", "/favicon.svg", "/icon-192.png", "/icon-512.png",
  "/fonts-baloo2.woff2", "/fonts-sora.woff2"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(CORE)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  // Never intercept API/WS or non-GET; let the network handle them.
  if (e.request.method !== "GET" || url.pathname.startsWith("/api") || url.pathname === "/ws") return;
  // Cross-origin (Google Fonts): leave to the browser's own cache.
  if (url.origin !== location.origin) return;

  if (e.request.mode === "navigate") {
    // Network-first for the page (keeps the injected token fresh), cache fallback.
    e.respondWith(
      fetch(e.request)
        .then((r) => { const cp = r.clone(); caches.open(CACHE).then((c) => c.put("/", cp)); return r; })
        .catch(() => caches.match("/"))
    );
  } else if (url.pathname.startsWith("/i18n/")) {
    // Translations change with the app, so serve them network-first and keep a
    // copy only as an offline fallback. Cache-first would pin a language file
    // for as long as the cache name stays the same, and a stale one silently
    // leaves the interface in English.
    e.respondWith(
      fetch(e.request)
        .then((r) => { if (r.ok) { const cp = r.clone(); caches.open(CACHE).then((c) => c.put(e.request, cp)); } return r; })
        .catch(() => caches.match(e.request))
    );
  } else {
    // Cache-first for static assets.
    e.respondWith(
      caches.match(e.request).then((r) =>
        r || fetch(e.request).then((resp) => {
          if (resp.ok) { const cp = resp.clone(); caches.open(CACHE).then((c) => c.put(e.request, cp)); }
          return resp;
        })
      )
    );
  }
});
