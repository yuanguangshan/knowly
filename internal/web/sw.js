const CACHE = "knowly-v5";

// 安装时跳过等待，立即生效
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

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  const isSameOrigin = url.origin === self.location.origin;

  // 只处理同源 GET/HEAD 请求
  if (!isSameOrigin || (e.request.method !== "GET" && e.request.method !== "HEAD")) {
    return;
  }

  // API 请求：始终走网络，不缓存
  if (url.pathname.startsWith("/api/")) {
    e.respondWith(fetch(e.request));
    return;
  }

  // Service Worker 自身：始终走网络（避免缓存锁死无法更新）
  if (url.pathname === "/sw.js") {
    e.respondWith(fetch(e.request));
    return;
  }

  // HTML 页面和静态资源：stale-while-revalidate
  // 先秒回缓存，后台同步更新
  e.respondWith(
    caches.open(CACHE).then((cache) => {
      return cache.match(e.request).then((cached) => {
        // 后台异步更新缓存（不阻塞响应）
        const fetchPromise = fetch(e.request).then((res) => {
          // 只缓存成功响应
          if (res && res.status === 200) {
            cache.put(e.request, res.clone());
          }
          return res;
        }).catch(() => cached);

        // 有缓存就先返回，没有就等网络
        return cached || fetchPromise;
      });
    })
  );
});
