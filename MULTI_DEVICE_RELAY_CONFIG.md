# 多设备同步时的 Relay 配置指南

## 背景

Knowly 支持通过 Relay 功能接收来自其他设备（如手机、外部电脑）推送的内容。Relay 端点（Cloudflare Worker）采用“拉取后删除”设计，确保每条内容只被一个客户端消费。

当您在多台电脑（如 Mac mini 和 Mac Air）上同时运行 Knowly 时，如果都启用 Relay 拉取功能，可能导致：

1. **竞争性重复同步**：两台电脑几乎同时拉取到相同内容，分别同步到 NAS，造成重复归档。
2. **资源浪费**：多台设备持续轮询 Relay 端点，增加网络开销和 Relay 服务负载。
3. **逻辑混乱**：难以追踪哪台设备处理了哪条内容。

## 最佳实践：单 Relay 接收器

### 推荐配置

| 设备角色 | Relay 配置 | 剪贴板监听 | 说明 |
|----------|------------|------------|------|
| **主力机**（如 Mac mini） | ✅ 启用 | ✅ 启用 | 作为唯一的 Relay 接收器，负责拉取手机/外部设备推送的内容，并同步到 NAS |
| **辅助机**（如 Mac Air） | ❌ 禁用 | ✅ 启用 | 仅通过 SSH 直接同步本地剪贴板内容到 NAS，不参与 Relay 拉取 |

### 配置示例

#### 主力机（Relay 启用）

```json
{
  "relay": {
    "enabled": true,
    "endpoint": "https://your-relay-worker.example.com",
    "secret": "your-secret-key",
    "pull_interval_sec": 10
  }
}
```

#### 辅助机（Relay 禁用）

```json
{
  "relay": {
    "enabled": false,
    "endpoint": "",
    "secret": "",
    "pull_interval_sec": 5
  }
}
```

## 工作原理

### Relay 端点逻辑（Cloudflare Worker）

```javascript
// 关键行为：
// 1. /push 存入内容到 'latest' 键
// 2. /pull 读取 'latest' 键内容后立即删除该键
// 3. 如果内容已被删除，返回 204 No Content
```

### 单 Relay 接收器的优势

1. **避免重复**：只有主力机拉取内容，辅助机不会收到相同内容。
2. **降低负载**：减少一半的轮询请求。
3. **职责清晰**：主力机负责外部内容，辅助机仅处理本地内容。

## 配置步骤

### 1. 确认当前配置

检查每台设备的 Relay 状态：

```bash
knowly config | grep -A5 '"relay"'
```

### 2. 修改配置

#### 禁用辅助机的 Relay

```bash
# 编辑配置文件
knowly config --edit

# 或手动修改 ~/.knowly/config.json
# 将 "relay": { "enabled": true } 改为 false
```

#### 确保主力机 Relay 启用

```bash
# 在主力机上确认
knowly config --edit
```

### 3. 重启服务

```bash
# 停止并启动守护进程
knowly stop
knowly start

# 验证状态
knowly --status
```

## 故障排除

### 问题：手机推送的内容未被同步

- 检查主力机 Relay 是否启用
- 查看主力机日志：`knowly log | grep -i relay`
- 验证 Relay 端点可达性：`curl -H "X-Auth-Key: your-secret" https://your-relay-worker.example.com/pull`

### 问题：辅助机仍然拉取到内容

- 确认辅助机 Relay 已禁用（`enabled: false`）
- 检查配置文件路径是否正确（`~/.knowly/config.json`）
- 重启辅助机 Knowly 服务

### 问题：重复同步

- 确保只有一台设备 Relay 启用
- 检查两台设备的拉取间隔是否错开（如一台 10 秒，一台 15 秒）
- 考虑修改 Relay Worker，为每台设备分配独立键名

## 高级场景

### 双 Relay 接收器（需修改 Worker）

如果确实需要两台设备都拉取相同内容（例如冗余备份），可修改 Worker 逻辑：

1. **为每台设备分配独立存储键**（如 `latest_macmini`、`latest_macair`）
2. **广播模式**：不删除键，让所有客户端都能拉取（需自行处理去重）

### 动态配置

通过环境变量或远程配置动态控制哪台设备启用 Relay，实现负载均衡。

## 总结

在多设备 Knowly 部署中，**强烈建议只在一台主力机上启用 Relay 功能**，其他设备通过 SSH 直连 NAS 同步本地剪贴板内容。这种配置：

- ✅ 避免重复同步
- ✅ 减少网络开销
- ✅ 简化运维复杂度
- ✅ 确保数据一致性

保持此配置，您的多设备知识归档系统将高效、可靠地运行。