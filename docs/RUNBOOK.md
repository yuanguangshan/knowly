# 知识系统运行手册 (RUNBOOK)

> 最后更新：2026-05-28
> 基于 knowly、weclaw、knasync 最新文档

---

## 一、系统组件与端口

| 组件 | 类型 | 本机端口/地址 | 公网端点 | 关键进程/服务 |
|------|------|--------------|----------|--------------|
| **Knowly** | Go 守护进程 (macOS) | `http://localhost:8090` Web 管理界面 | `https://knowly.want.biz` | `knowly start` LaunchAgent: `com.knowly.daemon` |
| **WeClaw** | Go 守护进程 | HTTP API: `127.0.0.1:18011` Admin: `/admin` | 主动推送: `https://wx.want.biz/api/send` | `weclaw start` systemd/launchd 可选 |
| **knasync** | Cloudflare Worker | – | `https://knasync.want.biz` `https://knasync.yuanguangshan.workers.dev` | 队列端点，需 `X-Auth-Key` |
| **WireGuard** | VPN 隧道 | Hub: `43.153.67.212:443` | 节点 `10.0.0.x` | 用于内网互通（NAS、沙箱等） |

---

## 二、配置与数据位置

### Knowly (`~/.knowly/`)

| 文件/目录 | 用途 |
|-----------|------|
| `config.json` | 主配置（SSH、剪贴板、AI、Relay、发布渠道） |
| `knowly.log` | 守护进程日志 |
| `knowly.pid` | PID 文件（带文件锁） |
| `status.json` | 上次剪贴板哈希（进程重启去重） |
| `history.jsonl` | 本地历史记录（JSONL 格式） |
| `outbox/pending.jsonl` | 离线暂存队列 |
| `cron_jobs.json` | 定时任务（Knowly 内置 cron） |

关键配置片段：

```json
{
  "ssh": { "host": "你的NAS", "user": "root", "key_path": "~/.ssh/id_rsa", "base_path": "/data/archive" },
  "web": { "enabled": true, "port": 8090, "auth": "user:pass" },
  "ai": { "enabled": true, "api_key": "sk-...", "endpoint": "https://openrouter.ai/api/v1", "model": "anthropic/claude-sonnet-4-20250514" },
  "relay": { "enabled": true, "endpoint": "https://knasync.want.biz", "secret": "你的密钥" },
  "blog": { "enabled": true, "api_url": "https://api.yuangs.cc/api/publish" },
  "ima": { "enabled": true, "client_id": "...", "api_key": "..." }
}
```

### WeClaw (`~/.weclaw/`)

| 文件/目录 | 用途 |
|-----------|------|
| `config.json` | Agent 配置、默认 Agent、API 地址 |
| `weclaw.log` | 后台日志 |
| `weclaw.pid` | PID 文件 |
| `accounts/*.json` | 微信账号凭证（每个账号一个文件） |
| `hub/shared/` | Agent Hub 共享文件（Markdown + YAML frontmatter） |
| `hub/templates/` | Prompt 模板 |
| `cron_jobs.json` | Cron 定时任务 |
| `todos.json` | 待办事项 |
| `timers.json` | 倒计时器 |

关键配置片段：

```json
{
  "default_agent": "claude",
  "api_addr": "127.0.0.1:18011",
  "save_dir": "~/Documents/weclaw-saves",
  "agents": {
    "claude": { "type": "acp", "command": "/usr/local/bin/claude-agent-acp", "env": { "ANTHROPIC_API_KEY": "sk-..." } }
  },
  "publish_url": "http://192.168.31.100:8090",
  "relay_url": "https://knasync.want.biz",
  "relay_auth_key": "你的密钥"
}
```

### knasync (Cloudflare Worker)

- **环境变量：** `SECRET_KEY`（认证密钥，需与 Knowly/WeClaw 中 `relay.secret` 一致）
- **数据库：** Cloudflare D1（表 `queue`, `submitted`, `results`）
- 无需本地文件，完全托管在 CF 边缘。

### WireGuard 关键节点

| 节点 | IP | 用途 | 访问方式 |
|------|-----|------|---------|
| Hub (t) | `10.0.0.1` | 中心服务器 | `ssh t` 或 `43.153.67.212` |
| Mac 本机 | `10.0.0.4` | Knowly 运行机 | – |
| 家里 (u) | `10.0.0.5` | NAS/备份 | `ssh u` |
| 云沙箱 | `10.0.0.10` | IMA 技能运行环境 | `ssh sandbox` |

配置文件位置：
- Hub: `/etc/wireguard/wg0.conf`
- Mac: `~/wg0.conf`
- 其他节点类似。

---

## 三、常用运维命令

### Knowly

```bash
# 启动/停止/状态
knowly start           # 后台运行（LaunchAgent 方式）
knowly stop
knowly status

# 前台调试
knowly start -f

# 配置
knowly config          # 查看当前配置
knowly config --edit   # 编辑配置文件

# 历史记录
knowly history [n]     # 查看最近 n 条同步记录
knowly restore <id>    # 恢复内容到剪贴板

# 日志
knowly log             # 查看日志
knowly log -f          # 实时跟踪

# 更新
knowly update          # 从 GitHub 更新二进制并重启
```

系统服务（macOS）

```bash
launchctl load ~/Library/LaunchAgents/com.knowly.daemon.plist
launchctl unload ...
launchctl list | grep knowly
```

### WeClaw

```bash
# 启动/停止/状态
weclaw start           # 后台运行
weclaw start -f        # 前台
weclaw stop
weclaw restart
weclaw status

# 登录/账号
weclaw login           # 扫码添加微信账号

# 主动发送消息
weclaw send --to "user_id@im.wechat" --text "hello"
weclaw send --to "user_id" --media "https://example.com/image.png"

# 更新
weclaw update
weclaw version
```

系统服务（Linux systemd）

```bash
sudo systemctl enable --now weclaw
sudo systemctl status weclaw
```

### knasync (Worker 运维)

```bash
# 部署
cd knasync
wrangler deploy

# 本地测试
wrangler dev --remote

# 设置密钥
wrangler secret put SECRET_KEY

# 查看日志
wrangler tail
```

### WireGuard 快速检查

```bash
# 查看连接状态
sudo wg show
# 查看 Hub 转发规则
sudo iptables -L -n -t nat | grep MASQUERADE
# 测试节点互通
ping 10.0.0.5   # 从本机 ping 家里 NAS
```

---

## 四、关键 API 与队列

### knasync 队列

| 队列 | 消费者 | 说明 |
|------|--------|------|
| `zhihu` | Chrome 扩展 | 知乎链接专用 |
| `wechat` | IMA skill / WeClaw 自身 | 微信消息转发 |
| `general` | Knowly (Relay puller) | 通用内容归档 |

### 常用 API 调用（需 `X-Auth-Key`）

```bash
# 提交内容（自动分类）
curl -X POST https://knasync.want.biz/submit \
  -H "X-Auth-Key: your-key" \
  -H "Content-Type: application/json" \
  -d '{"content": "https://zhuanlan.zhihu.com/p/xxx"}'

# 拉取队列（消费者）
curl "https://knasync.want.biz/pull?queue=general" \
  -H "X-Auth-Key: your-key"

# 推送结果（消费者处理完后）
curl -X POST https://knasync.want.biz/push \
  -H "X-Auth-Key: your-key" \
  -d '{"content": "处理后的 Markdown"}'

# 获取结果（订阅者）
curl "https://knasync.want.biz/results?since=1715000000&limit=10" \
  -H "X-Auth-Key: your-key"
```

### Knowly Web API（常用）

```bash
# 上传文件（自动触发 AI 处理）
curl -X POST https://knowly.want.biz/api/upload \
  -u "user:pass" -F "file=@notes.md"

# 发布内容
curl -X POST https://knowly.want.biz/api/publish \
  -u "user:pass" \
  -H "Content-Type: application/json" \
  -d '{"content": "正文", "targets": ["blog", "ima"]}'

# 查看状态
curl -u "user:pass" https://knowly.want.biz/api/status

# 搜索 NAS
curl -u "user:pass" "https://knowly.want.biz/api/search?q=关键词"
```

> `-u "user:pass"` 默认可不加

### WeClaw 主动推送 API（内网）

```bash
curl -X POST http://127.0.0.1:18011/api/send \
  -H "Content-Type: application/json" \
  -d '{"to": "o9cq80wpGQpRIUxH2LGdGFrksGak@im.wechat", "text": "通知"}'
```

公网端点 `https://wx.want.biz/api/send` 同样可用（当前无认证）。

---

## 五、故障排查速查

### 1. Knowly 不同步 / 无反应

- 检查守护进程：`knowly status`，日志 `knowly log -f`
- SSH 连通性：`ssh <你的NAS>` 确认密钥有效
- 远程路径：`ssh <NAS> "ls -la /data/archive/2026/05/28/"`
- Relay 拉取：`curl -H "X-Auth-Key: xxx" https://knasync.want.biz/pull?queue=general` 看是否有积压
- AI 配置：访问 `https://knowly.want.biz/api/config/ai` 检查 API Key 是否正确
- 本地 outbox：`cat ~/.knowly/outbox/pending.jsonl` 看是否有未重试内容

### 2. WeClaw 收不到消息 / 不回复

- 状态：`weclaw status`，前台运行 `weclaw start -f` 观察输出
- 登录凭证：`ls ~/.weclaw/accounts/` 应有文件，若空则 `weclaw login`
- Agent 可用性：`curl http://127.0.0.1:18011/api/agents` 看列表
- 默认 Agent：`weclaw config | grep default_agent`，确保配置中存在
- 日志：`tail -f ~/.weclaw/weclaw.log`
- 消息去重：同一 `message_id` 5分钟内只处理一次，正常现象

### 3. knasync 队列积压

- 健康检查：`curl https://knasync.want.biz/health`
- 查看队列长度：`curl -H "X-Auth-Key: xxx" "https://knasync.want.biz/peek"`
- 手动清空（谨慎）：`curl -X POST -H "X-Auth-Key: xxx" https://knasync.want.biz/clear`
- 检查消费者：Knowly 的 Relay 是否启用（`config.json` 中 `relay.enabled: true`），拉取间隔默认 5 秒

### 4. WireGuard 隧道不通

- 检查 Hub 转发：`iptables -L -n -t nat | grep MASQUERADE`
- 检查各节点 `AllowedIPs`：必须包含 `10.0.0.0/24`
- 持久 keepalive：`PersistentKeepalive = 25` 应配置在 NAT 后的节点
- 查看握手：`sudo wg show` 看 latest handshake 时间

### 5. 上传文件失败（Knowly /api/upload）

- 大小限制：≤500MB
- SSH 写入权限：远程 `base_path/uploads` 目录是否存在且可写
- 认证：若配置了 `web.auth`，需携带 Basic Auth

---

## 六、常用数据流快速参考

### 剪贴板 → NAS

```
Cmd+C (text/image) → Knowly monitor (500ms) → 去重/过滤 → SSH → /data/archive/YYYY/MM/DD/HHMMSS_前缀.md
```

### 微信文章 → 自动分析

```
微信发 URL → WeClaw 接收 → SaveLinkToLinkhoard (保存 MD) → 触发 nanobot 分析 → 回复分析结果 → Knowly Relay puller → SSH → NAS
```

### 手机灵感 → NAS

```
手机复制文本 → 快捷指令 POST /submit (knasync) → general 队列 → Knowly Relay puller → SSH → NAS
```

### 多 Agent 协作

```
微信发 /hub pipe gemini "主题" → WeClaw 将默认 agent 回复存入 Hub → 将 Hub 内容注入 gemini → gemini 回复
```

### 定时发布

```
/cron add "0 9 * * *" to:xxx text:"早安" → WeClaw cron 调度 → 发送微信消息
```

---

## 七、升级与维护注意事项

1. **Knowly 升级：** `npm update -g knowly` 或 `knowly update`（需网络）。升级后配置文件自动迁移。
2. **WeClaw 升级：** `weclaw update` 会自动下载最新 GitHub Release 并重启。
3. **knasync 升级：** `git pull && wrangler deploy`，注意 D1 表结构变更需执行迁移。
4. **WireGuard 密钥更换：** 需同步更新所有节点的 `PrivateKey` 和对应 `PublicKey`。
5. **备份关键数据：**
   - Knowly: `~/.knowly/config.json`, `history.jsonl`
   - WeClaw: `~/.weclaw/config.json`, `accounts/`, `hub/`
   - NAS 归档: 定期 rsync 到另一个存储

---

> **提示：** 将本文件中的 `your-key`、`user:pass`、`sk-...` 等占位符替换为你的实际密钥。建议将此文件放在项目目录docs/并加入版本控制（排除敏感值）。
