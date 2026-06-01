"use strict";

// Bump CACHE whenever the cached assets change so clients pick up the update.
const CACHE = "airqr-v3";
const ASSETS = [
  "./",
  "./index.html",
  "./styles.css",
  "./app.js",
  "./manifest.webmanifest",
  "./vendor/jsqr/jsQR.js",
  "./vendor/pako/pako_inflate.min.js",
  "./icons/icon-192.png",
  "./icons/icon-512.png",
];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches.open(CACHE).then((cache) => cache.addAll(ASSETS)).then(() => self.skipWaiting()),
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
  // Cache-first: the scanner is fully static, so serve from cache and fall back
  // to the network only on a miss. A failed navigation returns the cached page,
  // which keeps the app working with no network at all.
  event.respondWith(
    caches.match(request).then((cached) => {
      if (cached) {
        return cached;
      }
      return fetch(request).catch(() => {
        if (request.mode === "navigate") {
          return caches.match("./index.html");
        }
        return Response.error();
      });
    }),
  );
});
