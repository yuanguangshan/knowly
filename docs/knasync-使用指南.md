# knasync 使用指南

> 基于 Cloudflare Workers + D1 的轻量级内容队列分发系统

---

## 目录

- [概述](#概述)
- [架构](#架构)
- [认证](#认证)
- [Base URL](#base-url)
- [队列说明](#队列说明)
- [API 接口详解](#api-接口详解)
  - [1. 健康检查](#1-健康检查-get-health)
  - [2. 提交内容](#2-提交内容-post-submit)
  - [3. 拉取内容](#3-拉取内容-get-pull)
  - [4. 只读查看](#4-只读查看-get-peek)
  - [5. 推送结果](#5-推送结果-post-push)
  - [6. 获取结果](#6-获取结果-get-results)
  - [7. 清空数据](#7-清空数据-post-clear)
- [数据库表详解](#数据库表详解)
- [去重机制](#去重机制)
- [完整使用场景](#完整使用场景)
  - [场景一：手机剪贴板→电脑阅读](#场景一手机剪贴板电脑阅读)
  - [场景二：自动化内容处理流水线](#场景二自动化内容处理流水线)
- [常见问题](#常见问题)
- [附录：快捷指令模板](#附录快捷指令模板)

---

## 概述

knasync 是一个运行在 Cloudflare 边缘的内容分发中间件，采用**生产者-消费者-订阅者**架构。核心理念：

| 角色 | 做什么 | 对应接口 |
|---|---|---|
| **生产者** | 提交 URL 或文本内容 | `POST /submit` |
| **消费者** | 拉取内容，处理后回写结果 | `GET /pull` + `POST /push` |
| **订阅者** | 轮询获取处理完成的结果 | `GET /results` |

典型链路：**手机复制链接 → 自动提交 → 服务器处理 → 客户端拉取结果**

---

## 架构

```
┌─────────────────────────────────────────────────────────┐
│                    Cloudflare Worker (边缘节点)          │
│                                                         │
│  POST /submit                                           │
│  ─────────────►  ┌─────────────────────────────────┐   │
│                  │         去重判断                  │   │
│                  │  ┌──────────┐  ┌──────────────┐ │   │
│                  │  │consumed  │  │ submitted    │ │   │
│                  │  │永久去重   │  │ 30s 窗口防抖  │ │   │
│                  │  └────┬─────┘  └──────┬───────┘ │   │
│                  │       └──────┬────────┘         │   │
│                  │              ▼                   │   │
│                  │        ┌──────────┐              │   │
│  GET /pull        │        │  queue   │              │   │
│  ◄────────────── │        │ (工作队列) │              │   │
│                  │        └────┬─────┘              │   │
│  POST /push       │             │                    │   │
│  ─────────────►  │             ▼                    │   │
│                  │        ┌──────────┐              │   │
│                  │        │ results  │              │   │
│  GET /results     │        │ (广播队列) │              │   │
│  ◄────────────── │        └──────────┘              │   │
│                  └─────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

---

## 认证

除 `/health` 外所有接口都需要认证。支持两种方式传 Key：

**方式一：请求头（推荐）**

```
X-Auth-Key: <your-secret-key>
```

**方式二：查询参数**

```
GET /peek?key=<your-secret-key>
```

认证使用 timing-safe 比较，防止时序侧信道攻击。

---

## Base URL

系统运行在两个域名上，功能完全一致：

| Base URL | 说明 |
|---|---|
| `https://knasync.yuanguangshan.workers.dev` | Cloudflare 分配域名 |
| `https://knasync.want.biz` | 自定义域名 |

下文示例统一用 `https://knasync.want.biz`。

---

## 队列说明

系统内置三个队列：

| 队列 | 用途 | 进入方式 |
|---|---|---|
| `zhihu` | 知乎问答/专栏 | 自动识别（提交 URL 含 `zhihu.com`） |
| `wechat` | 微信公众号文章 | 手动指定：`"queue": "wechat"` |
| `general` | 通用内容 | 默认兜底（其他所有内容） |

拉取时不指定队列时，按 **zhihu → wechat → general** 优先级回退。

---

## API 接口详解

### 1. 健康检查 `GET /health`

无需认证。检测服务是否正常运行。

```bash
curl https://knasync.want.biz/health
```

**响应**

```json
{
  "ok": true,
  "ts": 1715680000000,
  "db": "connected"
}
```

---

### 2. 提交内容 `POST /submit`

生产者将 URL 或文本提交到队列，系统自动分类并去重。

#### 请求格式

**JSON 格式（推荐）**

```json
{
  "content": "https://mp.weixin.qq.com/s/ABC123",
  "queue": "wechat"
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `content` | 是* | 要提交的内容（URL 或文本） |
| `url` | 否* | 支持作为 `content` 的别名 |
| `queue` | 否 | 手动指定队列：`zhihu` / `wechat` / `general` |

> *`content` 和 `url` 至少填一个，优先读取 `content`。

**纯文本格式**

也可以直接发纯文本作为请求体，系统会自动解析：

```
https://mp.weixin.qq.com/s/ABC123
```

#### 示例

```bash
# 提交知乎链接（自动识别为 zhihu 队列）
curl -X POST https://knasync.want.biz/submit \
  -H "Content-Type: application/json" \
  -H "X-Auth-Key: test1234" \
  -d '{"content": "https://zhuanlan.zhihu.com/p/123456"}'

# 提交微信文章（手动指定 wechat 队列）
curl -X POST https://knasync.want.biz/submit \
  -H "Content-Type: application/json" \
  -H "X-Auth-Key: test1234" \
  -d '{"content": "https://mp.weixin.qq.com/s/ABC123", "queue": "wechat"}'

# 提交通用文本
curl -X POST https://knasync.want.biz/submit \
  -H "Content-Type: text/plain" \
  -H "X-Auth-Key: test1234" \
  -d '今天看到一篇好文章，标题是……'

# iOS 快捷指令格式（用 url 字段）
curl -X POST https://knasync.want.biz/submit \
  -H "Content-Type: application/json" \
  -H "X-Auth-Key: test1234" \
  -d '{"url": "https://mp.weixin.qq.com/s/ABC123"}'
```

#### 响应

| 响应 | 含义 |
|---|---|
| `OK (general)` | 提交成功，进入 general 队列 |
| `OK (zhihu)` | 提交成功，进入 zhihu 队列 |
| `OK (wechat)` | 提交成功，进入 wechat 队列 |
| `Duplicate ignored (general)` | 内容重复被拦截（见下文去重机制） |

#### 去重规则

提交时系统按以下顺序判断：

1. **永久去重** — 该内容已被消费者成功处理过 → 永远拦截
2. **窗口去重** — 30 秒内提交过相同内容 → 临时拦截
3. **均未命中** → 放行入队

---

### 3. 拉取内容 `GET /pull`

消费者从队列取走内容进行处理。**拉取即删除**，每条内容只会被一个消费者拿到。

```bash
# 拉取知乎队列
curl -H "X-Auth-Key: test1234" \
  "https://knasync.want.biz/pull?queue=zhihu"

# 按优先级回退（zhihu → wechat → general）
curl -H "X-Auth-Key: test1234" \
  "https://knasync.want.biz/pull"

# 拉取通用队列
curl -H "X-Auth-Key: test1234" \
  "https://knasync.want.biz/pull?queue=general"
```

**参数**

| 参数 | 必填 | 说明 |
|---|---|---|
| `queue` | 否 | 队列名，支持 `zhihu` / `wechat` / `general`，也兼容 `queue_` 前缀（如 `queue_general`） |

**响应**

```json
// 200 OK — 有内容
["https://mp.weixin.qq.com/s/ABC123", "今天看到一篇好文章"]

// 204 No Content — 队列为空
```

---

### 4. 只读查看 `GET /peek`

查看队列当前内容，**不会删除**。用于调试和监控。

```bash
# 查看所有队列
curl -H "X-Auth-Key: test1234" \
  "https://knasync.want.biz/peek"

# 查看指定队列
curl -H "X-Auth-Key: test1234" \
  "https://knasync.want.biz/peek?queue=general"
```

**响应**

```json
{
  "total": 3,
  "queues": {
    "zhihu": [],
    "wechat": [],
    "general": [
      { "content": "https://mp.weixin.qq.com/s/ABC123", "queue_type": "general", "created_at": 1715680000 },
      { "content": "今天看到一篇好文章", "queue_type": "general", "created_at": 1715680005 }
    ]
  }
}
```

---

### 5. 推送结果 `POST /push`

消费者处理完内容后，将结果推送到广播队列。

```bash
# 推送处理结果
curl -X POST https://knasync.want.biz/push \
  -H "Content-Type: application/json" \
  -H "X-Auth-Key: test1234" \
  -d '{"content": "# 文章标题\n\n## 正文\n\n这是处理完成的 Markdown 内容……"}'

# 可选：同时标记原始内容为已消费（永久去重）
curl -X POST https://knasync.want.biz/push \
  -H "Content-Type: application/json" \
  -H "X-Auth-Key: test1234" \
  -d '{
    "content": "# 文章标题\n\n正文……",
    "original": "https://mp.weixin.qq.com/s/ABC123"
  }'
```

**`original` 字段说明**

`original` 是可选的，用于告诉系统「这条 URL 已经被处理完了，以后别再提交了」。传了 `original` 后，该 URL 会被写入 `consumed` 表永久去重，即使 30 秒窗口已过，再次提交也会被永久拦截。

| 场景 | 传 original? | 效果 |
|---|---|---|
| 老消费者（未适配） | 不传 | ✅ 正常推送，不做永久去重 |
| 新消费者（已适配） | 传原始 URL | ✅ 推送 + 标记已消费，永久拦截重复 |

**响应**

```
OK
```

---

### 6. 获取结果 `GET /results`

客户端轮询拉取处理完成的结果。支持游标分页，适合多客户端增量读取。

```bash
# 首次拉取（不传 since，从最开始的记录拉）
curl -H "X-Auth-Key: test1234" \
  "https://knasync.want.biz/results?limit=10"

# 增量拉取（传上次返回的 cursor，只拉新数据）
curl -H "X-Auth-Key: test1234" \
  "https://knasync.want.biz/results?since=1715680010&limit=10"
```

**参数**

| 参数 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `since` | 否 | `0` | 时间戳游标，只返回 `created_at > since` 的记录 |
| `limit` | 否 | `50` | 最大返回条数，上限 50 |

**响应**

```json
{
  "cursor": 1715680020,
  "items": [
    { "t": 1715680010, "c": "处理后的 Markdown 内容" },
    { "t": 1715680020, "c": "另一篇处理结果" }
  ]
}
```

**⭕ 轮询示例**（伪代码）

```
cursor = 0
loop:
  resp = GET /results?since={cursor}&limit=10
  for item in resp.items:
    display(item.c)   // 展示处理结果
  cursor = resp.cursor
  sleep(3)            // 3 秒后拉下一次
```

---

### 7. 清空数据 `POST /clear`

清空所有工作队列和结果队列。维护/调试用。

```bash
curl -X POST https://knasync.want.biz/clear \
  -H "X-Auth-Key: test1234"
```

**响应** `OK (all queues cleared)`

---

## 数据库表详解

系统使用 Cloudflare D1（Serverless SQLite）存储数据，共 4 张表：

```
                    ┌─────────────┐
                    │  submitted   │  ← 30 秒窗口去重（临时）
                    └──────┬──────┘
                           │
 生产者 ─ POST /submit ────┤
                           │
                    ┌──────┴──────┐          ┌─────────────┐
                    │    queue     │          │   consumed   │  ← 永久去重
                    │  (工作队列)   │          └──────┬──────┘
                    └──────┬──────┘                 │
                           │                        │ 消费者回传 original 时
                           ▼                        │ 写入
                消费者 ─ GET /pull ──→ 处理 ──→ POST /push ──→ results (广播结果)
```

### `queue` — 工作队列

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | INTEGER | 自增主键 |
| `content` | TEXT | 提交的内容（URL 或文本） |
| `queue_type` | TEXT | 队列分类：`zhihu` / `wechat` / `general` |
| `created_at` | INTEGER | 提交时间戳 |

- **容量**：每队列上限 50 条，超旧自动淘汰
- **读取即删除**：`GET /pull` 取出后立即删除

### `submitted` — 窗口去重

| 列 | 类型 | 说明 |
|---|---|---|
| `content` | TEXT | 内容全文（主键） |
| `last_seen_at` | INTEGER | 最近一次提交的时间戳 |

- **存活期**：30 秒
- **自动清理**：每次提交时删除超过 30 秒的旧记录

### `consumed` — 永久去重

| 列 | 类型 | 说明 |
|---|---|---|
| `content` | TEXT | 原始内容（主键） |
| `consumed_at` | INTEGER | 被消费的时间戳 |

- **存活期**：永久
- **写入时机**：消费者 `POST /push` 时传了 `original` 字段
- **效果**：一旦写入，该内容永远不会再被 `submit` 接受

### `results` — 广播结果

| 列 | 类型 | 说明 |
|---|---|---|
| `id` | INTEGER | 自增主键 |
| `content` | TEXT | 处理结果（如 Markdown 正文） |
| `created_at` | INTEGER | 结果写入时间戳 |

- **容量**：上限 50 条，超旧自动淘汰
- **读取**：`GET /results` 游标分页，不限次数

---

## 去重机制

系统有两层去重，按顺序执行：

```
提交内容 ──→ 查 consumed 表 ──→ 命中？→ 永久拒绝（Duplicate ignored）
                │
                未命中
                ▼
            查 submitted 表 ──→ 30 秒内出现过？→ 临时拒绝（Duplicate ignored）
                │
                未命中
                ▼
            通过 → 入队
```

| 层 | 表 | 窗口 | 目的 |
|---|---|---|---|
| ① 永久去重 | `consumed` | 永久 | 已被消费者处理过的内容不再入队 |
| ② 窗口防抖 | `submitted` | 30 秒 | 挡住快捷指令连发 / 网络重试 |

---

## 完整使用场景

### 场景一：手机剪贴板→电脑阅读

**需求**：在手机上看到好文章，复制链接后自动投递到电脑端阅读。

```
[手机] iOS 快捷指令监听剪贴板
    │ 复制链接 → 自动触发
    ▼
POST /submit ──→ [queue] ──→ 等待扩展拉取
                                │
                        [Chrome 扩展] GET /pull
                                │ 抓取文章内容
                                ▼
                        POST /push ←─ 推送 Markdown
                                │
                        [results 表]
                                │
                        [浏览器标签页] GET /results 轮询
                                │ 展示处理结果
                                ▼
                            阅读文章
```

**步骤**：

1. 手机安装 iOS 快捷指令，监听剪贴板变化
2. 复制文章链接 → 快捷指令自动提交到 `/submit`
3. Chrome 扩展通过 `/pull` 拉取到 URL
4. 扩展抓取文章正文，转为 Markdown
5. 扩展通过 `/push` 推回结果（带 `original` 字段）
6. 浏览器阅读页面通过 `/results` 轮询获取结果并展示

### 场景二：自动化内容处理流水线

**需求**：每天批量收集知乎精华回答，自动汇总。

```
定时任务
    │ 批量提交知乎链接
    ▼
POST /submit × N ──→ [zhihu queue]
                        │
                处理服务 GET /pull?queue=zhihu
                        │ 逐个抓取回答内容
                        ▼
                AI 摘要 → POST /push ──→ [results]
                                            │
                                    日报页面 /results 轮询
                                            ▼
                                        展示精华日报
```

---

## 常见问题

### Q：提交了两个不同的微信文章 URL，为什么返回 Duplicate ignored？

两个 URL 是两条独立的记录，不会互相拦截。`Duplicate ignored` 是因为每个 URL 在**30 秒内被重复提交过**（比如快捷指令连发）。等 30 秒后再提交同一个 URL，就会正常通过。

### Q：怎么判断是 30 秒窗口拦截还是永久拦截？

返回信息统一为 `Duplicate ignored (队列名)`，不区分原因。可以通过管理后台直接查数据库：

```bash
# 查 consumed 表（永久去重记录）
wrangler d1 execute knasync --remote --command \
  "SELECT * FROM consumed WHERE content = 'https://mp.weixin.qq.com/s/ABC'"

# 查 submitted 表（窗口去重记录）
wrangler d1 execute knasync --remote --command \
  "SELECT * FROM submitted WHERE content = 'https://mp.weixin.qq.com/s/ABC'"
```

### Q：我用的消费者没传 `original` 字段，会影响工作吗？

**完全不影响**。`original` 是可选字段，不传的话推送结果照常，只是不会触发永久去重。系统仍然靠 30 秒窗口防抖，足够了。

### Q：消费者拉取后崩溃了，内容会丢吗？

会。`/pull` 是「拉取即删除」，如果消费者拉取后崩溃且没有 `/push` 回结果，这条内容就丢失了。解决方式：

1. **30 秒后重新提交** — 窗口过期后再次提交同一个 URL 即可
2. **未来改进** — 可以在消费者侧实现「先处理再确认」的两阶段拉取

### Q：队列满了怎么办？

每个队列上限 50 条，超出时自动淘汰最旧的记录。如果频繁出现队列满，说明消费者处理速度跟不上生产速度，需要优化消费者。

### Q：key 怎么设置？

部署时通过 wrangler 设置：

```bash
wrangler secret put SECRET_KEY
```

默认值是 `test1234`（部署脚本里可修改）。部署后可通过环境变量修改，无需改代码。

---

## 附录：快捷指令模板

以下是一个 iOS 快捷指令的核心逻辑，用于监听剪贴板并自动提交：

```
1. 获取剪贴板内容
2. 如果内容是以 http:// 或 https:// 开头的 URL
3.   获取当前时间
4.   如果距离上次提交超过 30 秒
5.     使用"获取URL内容"操作：
       - URL: https://knasync.want.biz/submit
       - 方法: POST
       - 请求头: X-Auth-Key: test1234
       - 请求体: {"url":"（剪贴板内容）"}
6.   保存当前时间为"上次提交时间"
```

> 注意：30 秒的防抖判断在服务端已经做了，快捷指令端不做也可以。但做一层客户端防抖可以减少不必要的网络请求。
