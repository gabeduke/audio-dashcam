// Offline shell for the Hindsight UI.
//
// Only ever registered in a secure context (see app.js). Caching is a
// convenience for the app shell — the recordings and the live stream come from
// the Pi itself, so there is nothing useful to serve when it is unreachable.

// Bump whenever SHELL changes, so installed phones fetch the new shell.
const CACHE = 'hindsight-shell-v4';
const SHELL = [
  '/',
  '/index.html',
  '/wave.html',
  '/styles.css',
  '/app.js',
  '/lib/live.js',
  '/lib/meter.js',
  '/lib/ribbon.js',
  '/lib/takes.js',
  '/lib/wakelock.js',
  '/lib/wave/geometry.js',
  '/lib/wave/tiles.js',
  '/lib/wave/view.js',
  '/lib/wave/overview.js',
  '/lib/wave/clock.js',
  '/lib/wave/page.js',
  '/lib/wave/share.js',
  '/vendor/wavesurfer.esm.js',
  '/manifest.json',
  '/icons/icon-192.png',
];

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url);
  if (e.request.method !== 'GET' || url.pathname.startsWith('/api/')) return;

  // Network-first so a redeploy is picked up immediately, cache as fallback.
  e.respondWith(
    fetch(e.request)
      .then((res) => {
        if (res.ok) {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(e.request, copy));
        }
        return res;
      })
      .catch(() => caches.match(e.request).then((r) => r || caches.match('/index.html')))
  );
});
