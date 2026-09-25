"use strict";

// CACHE is bumped when the cached assets change, which forces a full refetch on
// install. The fetch handler below also revalidates in the background, so a
// forgotten bump can no longer strand a client on a stale build.
const CACHE = "airqr-v15";
const ASSETS = [
  "./",
  "./index.html",
  "./styles.css",
  "./app.js",
  "./fountain.js",
  "./audio.html",
  "./audio.js",
  "./manifest.webmanifest",
  "./vendor/jsqr/jsQR.js",
  "./vendor/pako/pako_inflate.min.js",
  "./icons/icon-180.png",
  "./icons/icon-192.png",
  "./icons/icon-512.png",
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    // cache: "reload" is what actually makes the version bump mean something.
    // A plain addAll fetches through the browser's HTTP cache, so a bumped
    // CACHE could be filled with the very stale copies it was bumped to evict.
    caches.open(CACHE)
      .then((cache) => cache.addAll(ASSETS.map((url) => new Request(url, { cache: "reload" }))))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") {
    return;
  }
  // Leave cross-origin requests (the webfont) to the browser: caching opaque
  // responses buys nothing and they were never in ASSETS.
  if (new URL(request.url).origin !== self.location.origin) {
    return;
  }

  // Stale-while-revalidate. The cached copy answers immediately, so the app
  // still opens instantly and still works with no network at all, but every
  // request also refreshes the cache in the background — so the next load
  // picks up a new build whether or not CACHE was bumped.
  //
  // Plain cache-first could strand a client on an old build indefinitely: the
  // only thing that invalidated the cache was remembering to change the
  // version string by hand, and forgetting it is silent.
  const network = fetch(request)
    .then(async (response) => {
      // cache.put rejects on a partial response, and response.ok is true for
      // 206, so match on 200 exactly.
      if (response && response.status === 200) {
        const cache = await caches.open(CACHE);
        await cache.put(request, response.clone());
      }
      return response;
    })
    .catch(() => null);

  // Registered before any await so the update survives the worker being idled.
  event.waitUntil(network);

  event.respondWith(
    caches.match(request).then(async (cached) => {
      if (cached) {
        return cached;
      }
      const fresh = await network;
      if (fresh) {
        return fresh;
      }
      if (request.mode === "navigate") {
        return (await caches.match("./index.html")) || Response.error();
      }
      return Response.error();
    }),
  );
});
