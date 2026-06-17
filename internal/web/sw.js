const CACHE = "knowly-v2";
const ASSETS = [
  "/",
  "/manifest.json"
];

// 安装时预缓存
self.addEventListener("install", (e) => {
  e.waitUntil(
    caches.open(CACHE).then((c) => c.addAll(ASSETS)).then(() => self.skipWaiting())
  );
});

// 激活时清理旧缓存
self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

// 网络优先，离线降级到缓存
self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);

  // API 请求不缓存
  if (url.pathname.startsWith("/api/")) {
    e.respondWith(fetch(e.request).catch(() => new Response(JSON.stringify({ error: "offline" }), { status: 503 })));
    return;
  }

  // 静态资源：缓存优先
  e.respondWith(
    caches.match(e.request).then((cached) => {
      // 有缓存时返回缓存，同时后台刷新缓存
      if (cached) {
        fetch(e.request).then((res) => {
          if (res.ok) {
            caches.open(CACHE).then((c) => c.put(e.request, res));
          }
        }).catch(() => {});
        return cached;
      }
      // 无缓存时从网络获取
      return fetch(e.request).then((res) => {
        return caches.open(CACHE).then((c) => {
          c.put(e.request, res.clone());
          return res;
        });
      }).catch(() => caches.match("/").then(function(fallback) {
        // 完全离线时显示缓存的首页
        return fallback || new Response("离线模式，请检查网络连接后刷新页面", {
          status: 503,
          headers: { "Content-Type": "text/plain; charset=utf-8" }
        });
      }));
    })
  );
});
