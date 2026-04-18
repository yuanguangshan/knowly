# knas 使用指南

## 安装完成后

### 1. 初始化配置

运行以下命令开始配置：

```bash
knas init
```

配置向导会询问以下信息：

| 配置项 | 说明 | 示例 |
|--------|------|------|
| SSH host | 服务器地址 | your-server.com |
| SSH port | SSH 端口 | 22 |
| SSH user | 用户名 | root |
| SSH key path | 密钥路径 | ~/.ssh/id_rsa |
| Remote base path | 远程存储路径 | ~/knas_archive |
| Min length | 最小同步长度 | 100 |

### 2. 配置 SSH 密钥认证

确保你的 SSH 公钥已添加到服务器：

```bash
# 复制公钥到服务器
ssh-copy-id user@your-server.com

# 或手动复制
cat ~/.ssh/id_rsa.pub | ssh user@your-server.com "mkdir -p ~/.ssh && cat >> ~/.ssh/authorized_keys"
```

### 3. 启动服务

```bash
# 启动守护进程
knas start

# 查看状态
knas status

# 查看日志
knas log
```

## 常用命令

### 基本操作

```bash
# 初始化配置
knas init

# 启动服务
knas start

# 停止服务
knas stop

# 查看状态
knas status

# 查看日志
knas log

# 实时跟踪日志
knas log -f
```

### 配置管理

```bash
# 查看当前配置
knas config

# 编辑配置文件
knas config --edit
```

### 系统服务

```bash
# 停止服务
launchctl unload ~/Library/LaunchAgents/com.knas.daemon.plist

# 启动服务
launchctl load ~/Library/LaunchAgents/com.knas.daemon.plist

# 查看状态
launchctl list | grep knas

# 卸载服务（不再开机自启）
launchctl unload ~/Library/LaunchAgents/com.knas.daemon.plist
rm ~/Library/LaunchAgents/com.knas.daemon.plist
```

## 工作流程

1. knas 在后台运行，每 500ms 检查一次剪切板
2. 当检测到新内容时：
   - 检查长度是否 >= 最小长度（默认 100 字符）
   - 检查是否包含敏感词（默认：password, 密码, token）
   - 如果是 URL，尝试抓取网页标题
3. 通过 SSH 将内容同步到远程服务器
4. 文件按日期存储：`~/knas_archive/YYYY/MM/DD/HHMMSS.md`


## 功能特点

- 🎯 **智能过滤**: (v1.2.0+) 针对敏感词（token/password等）进行本地扫描，支持长文本逻辑放行，敏感信息“不出本地”。
- 🏷️ **语义文件名**: 自动提取内容前 10 个字符作为文件名后缀，实现“文件名即索引”。

## 远程文件结构

同步的文件按以下结构存储：

~/knas_archive/
├── 2026/
│   ├── 04/
│   │   ├── 18/
│   │   │   ├── 133119_knas_已成功运行.md  
│   │   │   ├── 142545_关于量化交易的思.md
│   │   │   └── ...


## 文件格式

每个同步的文件格式如下：

```markdown
---
sync_time: 2026-04-18 14:20:30
source: clipboard
---

[内容]
```

如果是 URL，会自动添加标题：

```markdown
---
sync_time: 2026-04-18 14:20:30
source: clipboard
---

# 网页标题

https://example.com
```

## 故障排除

### 服务无法启动

```bash
# 查看日志
knas log

# 检查配置
knas config

# 测试 SSH 连接
ssh user@your-server.com
```

### SSH 连接失败

1. 确认服务器地址和端口正确
2. 确认 SSH 密钥路径正确
3. 确认公钥已添加到服务器
4. 测试连接：`ssh -i ~/.ssh/id_rsa user@your-server.com`

### 文件未同步

1. 检查剪切板内容长度是否 >= 配置的最小长度
2. 检查是否包含敏感词
3. 查看日志了解详细错误信息

## 高级配置

编辑 `~/.knas/config.json`：

```json
{
  "ssh": {
    "host": "your-server.com",
    "port": "22",
    "user": "root",
    "key_path": "~/.ssh/id_rsa",
    "base_path": "~/knas_archive"
  },
  "clipboard": {
    "min_length": 100,
    "poll_interval_ms": 500,
    "exclude_words": ["password", "密码", "token", "secret"]
  },
  "sync": {
    "enabled": true,
    "max_retries": 3,
    "retry_delay_ms": 5000
  }
}
```

## 开机自启动

使用 LaunchAgent 实现开机自启动：

```bash
# 安装服务
knas service install

# 加载服务
launchctl load ~/Library/LaunchAgents/com.knas.daemon.plist

# 检查服务状态
launchctl list | grep knas
```

## 卸载

```bash
# 停止服务
knas stop

# 卸载系统服务（如果已安装）
launchctl unload ~/Library/LaunchAgents/com.knas.daemon.plist
rm ~/Library/LaunchAgents/com.knas.daemon.plist

# 删除配置和数据
rm -rf ~/.knas

# 卸载 NPM 包
npm uninstall -g knas
```
