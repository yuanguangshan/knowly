// DeepSeek Reverse Proxy - Cloudflare Worker
// 部署到: deepseek.yuanguangshan.workers.dev
// 功能：代理 chat.deepseek.com + fe-static.deepseek.com，剥离安全头
// 手机/电脑都可以通过 iframe 加载 DeepSeek

const TARGET_MAIN = 'https://chat.deepseek.com';
const TARGET_CDN  = 'https://fe-static.deepseek.com';
const CDN_PREFIX  = '/_cdn';

const HEADERS_TO_REMOVE = [
  'x-frame-options',
  'content-security-policy',
  'content-security-policy-report-only',
  'x-content-type-options',
];

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const workerOrigin = url.origin;

    // 健康检查
    if (url.pathname === '/health') {
      return new Response('OK', { status: 200 });
    }

    // OPTIONS 预检
    if (request.method === 'OPTIONS') {
      return new Response(null, {
        status: 204,
        headers: {
          'access-control-allow-origin': '*',
          'access-control-allow-methods': 'GET, POST, PUT, DELETE, PATCH, OPTIONS',
          'access-control-allow-headers': '*',
          'access-control-max-age': '86400',
        },
      });
    }

    // 路由：/_cdn/xxx -> fe-static.deepseek.com/xxx，其余 -> chat.deepseek.com
    let targetUrl;
    let targetOrigin;
    if (url.pathname.startsWith(CDN_PREFIX + '/')) {
      const realPath = url.pathname.slice(CDN_PREFIX.length);
      targetUrl = TARGET_CDN + realPath + url.search;
      targetOrigin = TARGET_CDN;
    } else {
      targetUrl = TARGET_MAIN + url.pathname + url.search;
      targetOrigin = TARGET_MAIN;
    }

    // 构造转发 headers
    const headers = new Headers(request.headers);
    headers.delete('host');
    headers.set('origin', TARGET_MAIN);
    headers.set('referer', TARGET_MAIN + '/');

    try {
      const response = await fetch(targetUrl, {
        method: request.method,
        headers: headers,
        body: ['GET', 'HEAD'].includes(request.method) ? undefined : request.body,
        redirect: 'manual',
      });

      // 构造新响应头
      const newHeaders = new Headers(response.headers);
      for (const h of HEADERS_TO_REMOVE) {
        newHeaders.delete(h);
      }
      newHeaders.set('access-control-allow-origin', '*');
      newHeaders.set('access-control-allow-methods', 'GET, POST, PUT, DELETE, PATCH, OPTIONS');
      newHeaders.set('access-control-allow-headers', '*');

      // 处理 Set-Cookie
      const setCookieHeader = response.headers.get('set-cookie');
      if (setCookieHeader) {
        newHeaders.delete('set-cookie');
        const cookies = setCookieHeader.split(/,(?=\s*\w+=)/);
        for (const cookie of cookies) {
          const cleaned = cookie
            .replace(/;\s*domain=[^;]*/gi, '')
            .replace(/;\s*secure/gi, '')
            .replace(/;\s*samesite=[^;]*/gi, '; SameSite=None');
          newHeaders.append('set-cookie', cleaned.trim());
        }
      }

      // 处理重定向
      if (newHeaders.has('location')) {
        let location = newHeaders.get('location');
        location = location.replace(TARGET_CDN, workerOrigin + CDN_PREFIX);
        location = location.replace(TARGET_MAIN, workerOrigin);
        newHeaders.set('location', location);
      }

      const contentType = response.headers.get('content-type') || '';

      // HTML 响应：替换域名 + 移除 SRI integrity 属性
      if (contentType.includes('text/html')) {
        let body = await response.text();
        // 移除 integrity 属性（因为我们修改了 JS/CSS 内容，哈希会变）
        body = body.replace(/\s+integrity="[^"]*"/g, '');
        body = body.replace(/\s+integrity='[^']*'/g, '');
        // 移除 crossorigin 属性（避免不必要的 CORS 校验）
        body = body.replace(/\s+crossorigin(?:="[^"]*")?/g, '');
        // CDN 域名 → Worker/_cdn（必须先替换，因为更具体）
        body = body.replace(/https?:\/\/fe-static\.deepseek\.com/g, workerOrigin + CDN_PREFIX);
        // 主站域名 → Worker
        body = body.replace(/https?:\/\/chat\.deepseek\.com/g, workerOrigin);
        return new Response(body, { status: response.status, headers: newHeaders });
      }

      // JS/CSS 响应：替换 CDN 域名 + 主站域名
      // 主站域名也必须替换，因为 DeepSeek JS 有 hostname 白名单校验
      if (contentType.includes('javascript') || contentType.includes('text/css')) {
        let body = await response.text();
        // 1. 先替换带协议的完整 URL
        body = body.replace(/https?:\/\/fe-static\.deepseek\.com/g, workerOrigin + CDN_PREFIX);
        body = body.replace(/https?:\/\/chat\.deepseek\.com/g, workerOrigin);
        // 2. 再替换裸域名（hostname 白名单校验用的是不带协议的域名）
        const workerHost = new URL(workerOrigin).hostname;
        body = body.replace(/chat\.deepseek\.com/g, workerHost);
        return new Response(body, { status: response.status, headers: newHeaders });
      }

      // 如果响应是 wasm 文件，需要确保 Content-Type 为 application/wasm
      if (url.pathname.endsWith('.wasm')) {
        newHeaders.set('content-type', 'application/wasm');
      }

      // JSON / 其他类型（图片、字体、wasm等）直接透传
      return new Response(response.body, { status: response.status, headers: newHeaders });

    } catch (err) {
      return new Response('Proxy Error: ' + err.message, { status: 502 });
    }
  },
};
