const CACHE = "knowly-v3";

// 安装时跳过等待
self.addEventListener("install", (e) => {
  self.skipWaiting();
});

// 激活时清理旧缓存
self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

// 全部走网络，离线用缓存兜底
self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);

  // API 请求不缓存
  if (url.pathname.startsWith("/api/")) {
    e.respondWith(fetch(e.request).catch(() => new Response(JSON.stringify({ error: "offline" }), { status: 503 })));
    return;
  }

  // 网络优先
  e.respondWith(
    fetch(e.request).then((res) => {
      // 只缓存 manifest.json
      if (url.pathname === "/manifest.json" && res.ok) {
        caches.open(CACHE).then((c) => c.put(e.request, res.clone()));
      }
      return res;
    }).catch(() => {
      return caches.match(e.request).then(function(cached) {
        return cached || new Response("离线模式", { status: 503 });
      });
    })
  );
});
