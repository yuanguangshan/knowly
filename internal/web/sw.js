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

// 仅缓存 manifest.json，页面和 API 始终走网络
self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);

  // manifest.json：缓存优先，网络更新
  if (url.pathname === "/manifest.json") {
    e.respondWith(
      fetch(e.request).then((res) => {
        caches.open(CACHE).then((c) => c.put(e.request, res.clone()));
        return res;
      }).catch(() => caches.match(e.request))
    );
    return;
  }

  // 页面和 API：始终走网络
  e.respondWith(fetch(e.request).catch(() => new Response("离线", { status: 503 })));
});
