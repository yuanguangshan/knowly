# Knowly Web API 文档

**Base URL:** `https://knowly.want.biz` 或 `http://localhost:8090`
**认证:** 如配置了 `web.auth`（格式 `user:password`），**所有**请求需携带 HTTP Basic Auth：
```bash
curl -u "user:password" https://knowly.want.biz/api/status
```
未配置 `web.auth` 时所有接口无需认证。

## 目录

- [1. 日志 API](#1-日志-api)
- [2. 归档 API](#2-归档-api)
- [3. 历史 API](#3-历史-api)
- [4. 发布 API](#4-发布-api)
- [5. 搜索 API](#5-搜索-api)
- [6. 统计 API](#6-统计-api)
- [7. 配置 API](#7-配置-api)
- [8. 管理 API](#8-管理-api)
- [9. 文件上传 API](#9-文件上传-api)
- [10. 前端页面](#10-前端页面)
- [错误响应格式](#错误响应格式)

---

## 1. 日志 API

### 1.1 GET /api/logs

获取最近的日志条目。

**URL:** [https://knowly.want.biz/api/logs](https://knowly.want.biz/api/logs)

**Query 参数:**

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `level` | string | 否 | 日志级别过滤：`info`, `warn`, `error`, `all`，默认全部 |
| `limit` | int | 否 | 返回条数，默认 300 |

**示例:** [https://knowly.want.biz/api/logs?level=info&limit=50](https://knowly.want.biz/api/logs?level=info&limit=50)

**响应示例:**

```json
[
  {
    "timestamp": "2026-05-21 18:00:00",
    "level": "INFO",
    "message": "knowly started"
  }
]
```

### 1.2 GET /api/logs/stream

SSE 实时日志流。

**URL:** [https://knowly.want.biz/api/logs/stream](https://knowly.want.biz/api/logs/stream)

**Query 参数:**

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `level` | string | 否 | 日志级别过滤 |

**响应:** `text/event-stream`，每条日志为一行 `data: ...`。客户端保持连接持续接收。

---

## 2. 归档 API

通过 SSH 连接远程 NAS 浏览归档文件。

### 2.1 GET /api/archive/list

列出指定目录下的文件/子目录。

**URL:** [https://knowly.want.biz/api/archive/list](https://knowly.want.biz/api/archive/list)

**Query 参数:**

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `path` | string | 是 | 相对于 `ssh.base_path` 的路径，如 `2026/05/21` |

**示例:** [https://knowly.want.biz/api/archive/list?path=2026/05/21](https://knowly.want.biz/api/archive/list?path=2026/05/21)

**响应:** 目录条目数组。

```json
[
  { "name": "article.md", "is_dir": false, "size": 4096, "mod_time": "2026-05-21T10:00:00Z", "title": "文章标题" }
]
```

### 2.2 GET /api/archive/today

一次性返回归档初始化数据（年/月/日层级 + 当日文件）。

**URL:** [https://knowly.want.biz/api/archive/today](https://knowly.want.biz/api/archive/today)

**无参数。** 自动使用当前日期。

**响应:**

```json
{
  "years": [{ "name": "2026", "is_dir": true, ... }],
  "months": [{ "name": "05", "is_dir": true, ... }],
  "days": [{ "name": "21", "is_dir": true, ... }],
  "files": [{ "name": "article.md", "is_dir": false, ... }],
  "year": "2026",
  "month": "05",
  "day": "21"
}
```

### 2.3 GET /api/archive/file

读取归档文件内容（在线预览）。

**URL:** [https://knowly.want.biz/api/archive/file](https://knowly.want.biz/api/archive/file)

**Query 参数:**

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `path` | string | 是 | 相对于 `ssh.base_path` 的文件路径 |

**示例:** [https://knowly.want.biz/api/archive/file?path=2026/05/21/article.md](https://knowly.want.biz/api/archive/file?path=2026/05/21/article.md)

**响应:** 文件内容。根据扩展名自动设置 `Content-Type`（`.md`/`.txt` → `text/plain`，`.png` → `image/png` 等）。

### 2.4 GET /api/archive/download

下载归档文件（强制下载对话框）。

**URL:** [https://knowly.want.biz/api/archive/download](https://knowly.want.biz/api/archive/download)

**Query 参数:**

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `path` | string | 是 | 相对于 `ssh.base_path` 的文件路径 |

**示例:** [https://knowly.want.biz/api/archive/download?path=2026/05/21/article.md](https://knowly.want.biz/api/archive/download?path=2026/05/21/article.md)

**响应:** 文件内容，带 `Content-Disposition: attachment` header。

---

## 3. 历史 API

### 3.1 GET /api/history

获取本地历史记录列表。

**URL:** [https://knowly.want.biz/api/history](https://knowly.want.biz/api/history)

**Query 参数:**

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `limit` | int | 否 | 返回条数，默认 50 |
| `type` | string | 否 | 类型过滤，如 `clipboard`、`sync`，或 `all` |
| `tag` | string | 否 | 标签过滤，匹配包含该标签的条目 |
| `after` | string | 否 | 分页：返回该时间戳 **之前** 的条目，格式 `2006-01-02 15:04:05` |

**示例:** [https://knowly.want.biz/api/history?limit=20&type=clipboard](https://knowly.want.biz/api/history?limit=20&type=clipboard)

**响应:** 历史记录数组。

```json
[
  {
    "id": "abc123",
    "content": "剪贴板内容...",
    "type": "clipboard",
    "timestamp": "2026-05-21 18:00:00",
    "nas_path": "/remote/path/article.md",
    "tags": ["AI", "技术"],
    "title": "文章标题",
    "manual_edit": false
  }
]
```

### 3.2 GET /api/history/{id}

获取单条历史记录。

**URL:** `https://knowly.want.biz/api/history/{id}`

将 `{id}` 替换为实际条目 ID，如：[https://knowly.want.biz/api/history/abc123](https://knowly.want.biz/api/history/abc123)

**响应:**

```json
{
  "id": "abc123",
  "content": "...",
  "type": "clipboard",
  "timestamp": "2026-05-21 18:00:00",
  "nas_path": "/remote/path/article.md",
  "tags": ["AI"],
  "title": "标题",
  "publish_summary": "摘要...",
  "manual_edit": false
}
```

### 3.3 PUT /api/history/{id}

更新历史记录的标题、标签或摘要。

**URL:** `https://knowly.want.biz/api/history/{id}`

**请求体:**

```json
{
  "title": "新标题",
  "tags": ["AI", "技术"],
  "summary": "新摘要"
}
```

**响应:** `{"status": "saved", "manual_edit": true}`

### 3.4 GET /api/history/{id}/full

获取完整内容（如本地只有预览内容，会从 NAS 读取全文）。

**URL:** `https://knowly.want.biz/api/history/{id}/full`

示例：[https://knowly.want.biz/api/history/abc123/full](https://knowly.want.biz/api/history/abc123/full)

**响应:**

```json
{ "content": "完整内容..." }
```

### 3.5 POST /api/history/{id}/reprocess

重新对该条目运行 AI 处理（生成标签、摘要、评分等）。

**URL:** `https://knowly.want.biz/api/history/{id}/reprocess`

示例：[https://knowly.want.biz/api/history/abc123/reprocess](https://knowly.want.biz/api/history/abc123/reprocess)

**无请求体。**

**响应:**

```json
{
  "status": "processing",
  "title": "...",
  "tags": ["AI"],
  "summary": "...",
  "score": 5
}
```

### 3.6 GET /api/tags

返回所有标签及使用次数。

**URL:** [https://knowly.want.biz/api/tags](https://knowly.want.biz/api/tags)

**响应:**

```json
[
  { "name": "AI", "count": 42 },
  { "name": "技术", "count": 15 }
]
```

---

## 4. 发布 API

### 4.1 POST /api/publish

发布内容到指定渠道（Blog/Podcast/IMA/Kindle）。

**URL:** [https://knowly.want.biz/api/publish](https://knowly.want.biz/api/publish)

**请求体:**

```json
{
  "content": "要发布的内容正文...",
  "targets": ["blog", "ima", "kindle", "podcast"]
}
```

AI 会自动生成标题。

**响应:**

```json
[
  { "target": "blog", "ok": true },
  { "target": "ima", "ok": false, "error": "IMA 配置不完整" }
]
```

### 4.2 POST /api/tag-and-publish

为已有历史条目添加标签并发布到指定渠道。

**URL:** [https://knowly.want.biz/api/tag-and-publish](https://knowly.want.biz/api/tag-and-publish)

**请求体:**

```json
{
  "id": "abc123",
  "tag": "待发布",
  "target": "blog"
}
```

`target` 可选值：`blog`、`podcast`、`ima`、`kindle`

**响应:**

```json
{
  "tag_added": true,
  "target": "blog",
  "published": true
}
```

---

## 5. 搜索 API

### 5.1 GET /api/search

在远程 NAS 归档中搜索内容。

**URL:** [https://knowly.want.biz/api/search](https://knowly.want.biz/api/search)

**Query 参数:**

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `q` | string | 是 | 搜索关键词 |
| `limit` | int | 否 | 返回条数，默认 50 |

**示例:** [https://knowly.want.biz/api/search?q=AI&limit=20](https://knowly.want.biz/api/search?q=AI&limit=20)

**响应:** 搜索结果数组（由 SSH 远程执行搜索返回）。

---

## 6. 统计 API

### 6.1 GET /api/stats

获取本地历史记录统计信息。

**URL:** [https://knowly.want.biz/api/stats](https://knowly.want.biz/api/stats)

**响应:**

```json
{ "total": 1234, "by_type": { "clipboard": 800, "sync": 434 } }
```

### 6.2 GET /api/status

获取守护进程运行状态。

**URL:** [https://knowly.want.biz/api/status](https://knowly.want.biz/api/status)

**响应:**

```json
{
  "ssh": { "host": "...", "user": "...", "port": "22", "base_path": "/path" },
  "daemon_running": true,
  "total_syncs": 500,
  "ssh_connected": true,
  "start_time": "2026-05-21 10:00:00",
  "uptime": 28800,
  "pid": 12345,
  "version": "6.50.0",
  "publishers": {
    "blog": { "enabled": true },
    "podcast": { "enabled": false },
    "ima": { "enabled": true },
    "kindle": { "enabled": true }
  }
}
```

---

## 7. 配置 API

### 7.1 GET /api/config/ai

获取当前 AI 配置（API Key 已脱敏）。

**URL:** [https://knowly.want.biz/api/config/ai](https://knowly.want.biz/api/config/ai)

**响应:**

```json
{
  "config": {
    "enabled": true,
    "api_key": "****abcd",
    "endpoint": "https://...",
    "model": "sonnet",
    "preset": "openrouter",
    "timeout": 60,
    "min_content_len": 100,
    "max_content_len": 10000
  },
  "presets": { "openrouter": { "endpoint": "...", "model": "...", "label": "..." } },
  "prompt_templates": { "default": "..." }
}
```

### 7.2 PUT /api/config/ai

更新 AI 配置。

**URL:** [https://knowly.want.biz/api/config/ai](https://knowly.want.biz/api/config/ai)

**请求体:**

```json
{
  "enabled": true,
  "api_key": "sk-...",
  "endpoint": "https://...",
  "model": "sonnet",
  "preset": "openrouter",
  "prompt": "自定义 prompt",
  "prompt_template": "模板名",
  "timeout": 60,
  "min_content_len": 100,
  "max_content_len": 10000
}
```

> 如果 `api_key` 传空值或脱敏值（`****` 开头），保留原 key 不变。

**响应:** `{"status": "saved"}`

### 7.3 GET /api/config

获取完整配置（敏感字段自动脱敏）。

**URL:** [https://knowly.want.biz/api/config](https://knowly.want.biz/api/config)

**响应:** 完整 JSON 配置对象。

### 7.4 PUT /api/config

更新完整配置。

**URL:** [https://knowly.want.biz/api/config](https://knowly.want.biz/api/config)

**请求体:** 完整配置 JSON 对象。建议先 GET 获取当前配置，修改所需字段后再 PUT 回传。

**示例（仅修改部分字段）：**

```json
{
  "ssh": {
    "host": "your-server.com",
    "port": "22",
    "user": "root",
    "key_path": "~/.ssh/id_ed25519",
    "base_path": "/data/archive",
    "filename_prefix_length": 16
  },
  "web": {
    "enabled": true,
    "port": 8090,
    "auth": "admin:secret123",
    "refresh_sec": 30,
    "log_refresh_sec": 30
  },
  "ai": {
    "enabled": true,
    "api_key": "sk-...",
    "endpoint": "https://openrouter.ai/api/v1",
    "model": "anthropic/claude-sonnet-4-20250514",
    "preset": "openrouter",
    "timeout": 60,
    "min_content_len": 100,
    "max_content_len": 10000
  }
  // ... 其余字段保持原样或省略
}
```

> 脱敏字段（`api_key`、`secret`、`auth`、`sender_password`）若传 `****` 开头或空值，保留原值。

**响应:** `{"status": "saved"}`

---

## 8. 管理 API

> **注意：** 管理操作（restart/update/release）不应并发调用。同时发起多个请求可能导致状态混乱或服务异常。

### 8.1 POST /api/admin/restart

重启守护进程（读取 PID 文件，发送 SIGTERM 后重新启动）。

**URL:** [https://knowly.want.biz/api/admin/restart](https://knowly.want.biz/api/admin/restart)

**无请求体。**

**响应:** `{"status": "restarting"}`

### 8.2 POST /api/admin/update

从源码重新编译、替换二进制文件并重启。返回 SSE 流。

**URL:** [https://knowly.want.biz/api/admin/update](https://knowly.want.biz/api/admin/update)

**无请求体。**

**响应 (SSE):** 各步骤进度事件，`step` 值枚举如下：

| step | 说明 |
|------|------|
| `building` | 编译中 |
| `replacing` | 替换二进制文件 |
| `pushing` | 提交并推送到远程 |
| `done` | 全部完成 |
| `error` | 某步骤失败 |

### 8.3 POST /api/admin/release

版本发布流程：git commit → git push → npm version minor → git push --tags。返回 SSE 流。

> 注：`npm version minor` 仅用于版本号递增，项目本身是 Go 后端。

**URL:** [https://knowly.want.biz/api/admin/release](https://knowly.want.biz/api/admin/release)

**无请求体。**

**响应 (SSE):** 各步骤进度事件，`step` 值枚举如下：

| step | 说明 |
|------|------|
| `commit` | 代码提交中 |
| `push` | 推送到远程 |
| `version` | 升级版本号 |
| `tags` | 推送标签 |
| `done` | 全部完成 |
| `error` | 某步骤失败 |

---

## 9. 文件上传 API

### 9.1 POST /api/upload

上传文件到远程 NAS（通过 SSH 连接）。

**URL:** [https://knowly.want.biz/api/upload](https://knowly.want.biz/api/upload)

**Content-Type:** `multipart/form-data`

**Form 字段:**

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `file` | File | 是 | 上传的文件 |

**大小限制:** 最大 200MB

**文件保存位置:** `{ssh.base_path}/uploads/<filename>`（同名文件自动追加时间戳避免覆盖）

**特殊处理:** 当上传的文件后缀为 `.txt` 或 `.md` 时，文件保存后将**自动触发异步文本同步流程**，包括：
- AI 处理：自动生成 tags、summary、评分及内容重组
- 归档写入：另存一份处理后的文件至 NAS 标准归档目录（`{ssh.base_path}/YYYY/MM/DD/HHMMSS_<prefix>.md`）
- 历史索引：记入 `history.jsonl`
- 自动发布：若配置了 Blog/Podcast/IMA/Kindle 等发布渠道，将自动推送

原始上传文件保留在 `uploads/` 目录（扁平，不按日期细分）。非文本文件（PDF、图片等）仅保存至该目录，不触发后续处理。

**响应:**

```json
{
  "status": "ok",
  "filename": "report.pdf",
  "saved_as": "report.pdf",
  "path": "/data/archive/uploads/report.pdf",
  "size": 1234567
}
```

**curl 示例:**

```bash
curl -X POST https://knowly.want.biz/api/upload \
  -u "user:password" \
  -F "file=@/path/to/report.pdf"
```

---

## 10. 前端页面

### 10.1 GET /

Web 管理界面，提供以下功能：

- **实时日志查看** — 流式展示日志，支持按级别过滤
- **归档浏览** — 年/月/日层级浏览远程 NAS 文件
- **历史记录** — 查看、编辑、重新 AI 处理、标签过滤
- **手动发布** — 发布内容到 Blog/Podcast/IMA/Kindle
- **配置管理** — AI 配置、完整配置的在线编辑
- **管理操作** — 重启服务、源码编译更新、版本发布

**URL:** [https://knowly.want.biz/](https://knowly.want.biz/)

---

## 错误响应格式

所有 API 错误统一返回 JSON：

```json
{ "error": "错误描述信息" }
```

HTTP 状态码遵循标准语义（200/400/404/405/500/503）。
