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

## architecture
knas（Knowledge Async）架构与亮点总结
一、项目定位与设计哲学
knas（Knowledge Async）是一个用 Go 语言编写的剪贴板到 NAS 的自动化同步守护进程。它运行在 macOS 本机，以 500ms 的频率轮询系统剪贴板，将符合条件的文本和图片通过 SSH 协议安全地传输到远程 Ubuntu 服务器，并按照 YYYY/MM/DD/ 的目录结构自动归档。
设计哲学可以概括为三个关键词：无感、安全、韧性。用户无需改变任何使用习惯——正常复制、截图，knas 在后台静默地将内容归档到 NAS。SSH 协议保证了传输层的端到端加密，无需在远程服务器安装任何接收端。而三层去重、自动重连、指数退避重试等机制则确保了在各种网络环境和异常场景下的稳定运行。
从架构层面看，knas 是一个典型的事件驱动型守护进程。它以 CSP（Communicating Sequential Processes）模型组织并发，通过 Go channel 在组件间传递消息，每个包有清晰的职责边界。整个项目约 2000 行 Go 代码，涵盖了从系统级剪贴板交互到网络传输、从本地持久化到远程文件操作的完整链路。
二、整体架构

┌──────────────────────────────────────────────────────────────┐
│                         knas daemon                          │
│                                                              │
│  ┌──────────────┐    ┌──────────────┐    ┌───────────────┐  │
│  │  clipboard   │    │   history    │    │    relay      │  │
│  │   monitor    │───>│    store     │    │    puller     │  │
│  │ (poll+dedup) │    │  (JSONL)     │    │  (HTTP pull)  │  │
│  └──────┬───────┘    └──────────────┘    └───────┬───────┘  │
│         │                                        │          │
│         │           Payload channel              │          │
│         └──────────────┬─────────────────────────┘          │
│                        │                                     │
│                   ┌────▼─────┐    ┌──────────┐              │
│                   │   ssh    │    │  retry   │              │
│                   │  client  │◄───│(backoff) │              │
│                   └────┬─────┘    └──────────┘              │
│                        │ SSH                                  │
└────────────────────────┼─────────────────────────────────────┘
                         │
                    ┌────▼─────┐
                    │  Ubuntu  │
                    │  NAS     │
                    │ YYYY/MM/ │
                    │ DD/*.md  │
                    └──────────┘
项目采用经典的 cmd/ + internal/ 布局，入口点在 cmd/knas/main.go，核心逻辑封装在六个 internal 包中：
包	职责	核心类型
internal/clipboard	剪贴板轮询、去重、过滤、载荷分发	Monitor, Payload 接口
internal/ssh	SSH 连接管理、远程文件读写、自动重连	Client
internal/history	JSONL 追加写入、压缩轮转、ID 查找	Store
internal/retry	指数退避 + Full Jitter 重试	Config, Do
internal/fetcher	URL 检测、HTML 标题提取	FetchTitle, IsURL
internal/relay	HTTP 拉取式消息中继	Puller
internal/config	四段式配置管理、路径展开	Config
三、核心数据流
3.1 剪贴板到 NAS 的完整同步路径
1. Monitor 轮询：clipboard.Monitor 启动一个后台 goroutine，以可配置的间隔（默认 500ms）通过 golang.design/x/clipboard 读取系统剪贴板。优先检测图片格式（PNG），若无图片则回退到文本。
2. Payload 构造：读取到的内容被封装为 TextPayload 或 ImagePayload，两者都实现了 Payload 接口。接口定义了 isPayload() 私有方法（ADT 密封模式）、Hash()、Type() 和 Preview() 方法。Payload 携带内容的 MD5 哈希和时间戳。
3. 去重与过滤：文本载荷经过两层检查——ShouldFilter() 检查长度和敏感词，isDuplicate() 通过内存中的 lastHash 与当前哈希比对。图片载荷仅做哈希去重。通过检查后，更新内部状态并持久化到 status.json。
4. URL 增强：如果文本是 URL（通过预编译的正则判断），enhanceAndSend() 会启动一个带 2 秒超时的 goroutine 抓取网页 <title> 标签，将标题追加到内容末尾。这个操作是异步的且不阻塞主轮询循环。
5. Channel 分发：通过检查的 Payload 被写入带缓冲的 itemChan（默认容量 10）。如果 channel 满了，该条目会被丢弃并记录警告日志，这是一种背压保护策略。
6. 主循环消费：main.go 的事件循环通过 select 同时监听系统信号（SIGINT/SIGTERM）和 mon.Items() channel。收到 Payload 后，以 goroutine 异步调用 handlePayload()。
7. SSH 同步：handlePayload() 根据载荷类型分发到 client.SyncItem()（文本）或 client.SyncImage()（图片）。每个操作都被 retry.Do() 包裹，支持指数退避重试。
8. 远程去重：SyncItem() 在写入前计算内容 MD5 哈希，通过 ExistsByHash() 在远程当天目录中搜索包含该哈希标记的文件。如果命中，返回空路径表示跳过。SyncImage() 通过文件名中的哈希前8位判断重复。
9. 文件写入：文本内容以 Markdown 格式写入，包含 YAML frontmatter（sync_time、source、content_hash），文件名由时间戳和内容前缀组成（如 142545_关于量化交易的思.md）。图片以二进制方式写入 PNG 文件。
10. 历史记录：同步成功后，handlePayload() 将条目追加到 history.Store，记录内容预览、类型、NAS 路径。如果同步被去重跳过（nasPath 为空），则不记录历史，避免无意义条目。
3.2 Relay 拉取路径
Relay 是一个独立的数据入口，用于接收来自其他设备（如手机）推送的内容。relay.Puller 以可配置间隔（默认 5 秒）向远程 HTTP 端点发送 GET 请求，携带 X-Auth-Key 认证头。收到内容后，经过 ShouldFilter() 统一过滤，然后走与剪贴板相同的 SSH 同步 + 历史记录流程。
四、核心设计亮点
4.1 三层去重体系
去重是 knas 最关键的设计之一。没有去重，守护进程会在每次轮询时将同一内容反复写入 NAS，造成存储膨胀和历史污染。knas 实现了从内存到持久化到远程的三层去重：
第一层：内存哈希（Monitor.lastHash）
Monitor 在 sync.RWMutex 保护下维护 lastHash 字段。每次轮询到新内容时，计算 MD5 哈希并与 lastHash 比较。这是最快、最轻量的去重层，能过滤掉同一内容的连续轮询。读写锁的使用确保了读操作（isDuplicate()）不会被写操作（updateState()）阻塞。
第二层：持久化状态（status.json）
进程重启后内存哈希丢失。Monitor 在构造时通过 loadStatus() 从 status.json 恢复上次的 lastHash、lastContent 和 lastType。每次状态更新后通过 saveStatus() 原子写入。这意味着即使守护进程重启，也能正确去重上次同步的最后一个内容。
第三层：远程哈希标记（content_hash）
同一内容可能在不同天被重新复制（例如从历史记录恢复后再复制）。SyncItem() 在写入文件时将 MD5 哈希嵌入 YAML frontmatter 的 content_hash 字段。写入前通过 ExistsByHash() 在当天目录中 grep 搜索包含该哈希的文件。图片去重则通过文件名中的哈希前8位实现（HHMMSS_<hash8>_image.png），用 ls 通配符匹配。
这三层去重各司其职：第一层过滤运行时的重复轮询，第二层抵御进程重启，第三层防止跨天的重复写入。层与层之间不存在耦合——即使某一层失效（比如 status.json 被误删），其他层仍然能提供保护。
4.2 SSH 连接韧性
SSH 连接是 knas 的生命线。网络波动、服务器重启、NAT 超时都可能导致连接中断。knas 实现了一套完整的连接健康管理和自动恢复机制：
轻量级探活（SendRequest）
传统的探活方式是创建一个 SSH Session 执行 echo hello，但 Session 的创建和销毁开销较大。knas 使用 sshClient.SendRequest("keepalive@openssh.com", true, nil) 进行探活——这是 OpenSSH 的标准 keepalive 机制，仅发送一个 SSH 协议层的请求/应答报文，不创建 Session，开销小一个数量级。
ensureConnected 模式
所有需要 SSH 连接的公开方法（SyncItem、SyncImage、ReadFile、WriteFile、WriteBinary、MkdirAll、ExistsByHash、imageExistsByHash、TestConnection）在执行前都调用 ensureConnected()。该方法首先检查现有连接的存活性，如果探测失败则清理旧连接并重新建立。这确保了每个操作都是在健康连接上执行的。
Mutex + connectLock 防死锁
Connect() 方法获取 connMu 互斥锁后委托给 connectLocked() 执行实际连接。ensureConnected() 同样先获取 connMu，检查连接存活，如果需要重连则直接调用 connectLocked()（而不是递归调用 Connect()），避免了死锁风险。这是一种经典的"锁内委托"模式。
守护进程级重试
在 daemon 启动阶段，knas 使用一个无限重试循环尝试 SSH 连接，每 10 秒重试一次。这是专门为 macOS LaunchAgent 设计的——系统可能在网络未就绪时就启动 knas，如果首次连接失败就直接退出，launchd 会反复重启进程造成 CPU 浪费。无限重试确保了即使启动时网络不可用，knas 也能在网络恢复后自动建立连接。
远程家目录动态解析
SSH 客户端首次连接成功后，通过 echo ~ 命令获取远程用户的真实家目录并缓存到 homeDir 字段。expandPath() 优先使用缓存的家目录，避免了对 /home/<user> 的硬编码假设。这对 root 用户（家目录为 /root）和非标准 Linux 发行版尤其重要。
4.3 Payload 接口与 ADT 密封模式
Go 语言没有代数数据类型（ADT）和密封接口，但 knas 通过一个巧妙的模式模拟了这一特性：

type Payload interface {
    isPayload()  // 私有方法，外部包无法实现
    Hash() string
    Type() string
    Preview() string
}

type TextPayload struct { ... }
func (TextPayload) isPayload() {}  // 仅包内可实现

type ImagePayload struct { ... }
func (ImagePayload) isPayload() {}
isPayload() 是一个无导出的空方法，只有 clipboard 包内的类型可以实现它。这确保了 Payload 接口是密封的——外部包无法创建新的 Payload 类型。在 handlePayload() 中通过 type switch 处理不同类型，编译器会检查是否覆盖了所有可能的类型。
这种设计比使用 interface{} + 类型断言更安全，比使用枚举 + 联合结构体更优雅。它将类型安全的责任交给了编译器而非运行时。
4.4 JSONL 存储与原子压缩
历史记录使用 JSONL（JSON Lines）格式存储，每行一个独立的 JSON 对象。这种格式的优势在于追加写入是原子的——操作系统保证 Write(append(data, '\n')) 在正常情况下不会写入半行。
惰性计数与阈值压缩
Store 维护一个 count 字段追踪条目数，但不在构造时遍历文件，而是通过 ensureCount() 在首次 Append() 时惰性统计。每次追加后 count++，当 count > maxEntries * 2 时触发 compact()。
compact() 保留最近 maxEntries（默认 1000）条记录，先写入临时文件（.tmp 后缀），然后通过 os.Rename() 原子替换原文件。os.Rename() 在同一文件系统上是原子操作，保证了压缩过程中不会出现数据丢失。
选择 2x 阈值而非 1x 是因为频繁压缩会带来性能开销——每次压缩都需要读取全部条目并重写文件。2x 阈值意味着在 1000 条的配置下，每插入约 1000 条才触发一次压缩，将压缩的均摊成本降到 O(1)。
ID 设计
条目 ID 采用 YYYYMMDDHHMMSS_<uuid8> 格式——时间戳前缀使 ID 天然有序，UUID 前 8 位保证全局唯一性。在 handleCLI 中显示时截断到 14 字符，既保持了可辨识性又不会占用过多终端空间。
4.5 统一过滤框架
ShouldFilter() 是一个导出的纯函数，实现了剪贴板和 Relay 两条路径的统一内容过滤：
* 长度过滤：minLength（默认 100）过滤掉太短的内容（如单个字母、快捷键误触），maxLength（默认 1MB）过滤掉过大的内容（如整篇文章、二进制数据的误读）。
* 敏感词过滤：支持可配置的排除词列表（默认包含 "password"、"密码"、"token"），使用 strings.Contains 做子串匹配。
将此函数导出为纯函数而非 Monitor 的方法，是一个关键的架构决策。它使得 Relay 等外部模块可以直接复用过滤逻辑，而不需要依赖 Monitor 实例。函数签名清晰——接收内容、参数，返回布尔值——易于测试（monitor_test.go 中有 7 个测试用例覆盖各种边界条件）。
4.6 重试机制：指数退避 + Full Jitter
retry.Do() 实现了 AWS 架构博客推荐的 Full Jitter 重试策略：

delay = min(BaseDelay * 2^(attempt-1), MaxDelay)
jitter = random(0, delay)
wait = delay + jitter
与传统固定间隔重试相比，指数退避避免了在服务端过载时的"惊群效应"；与无抖动退避相比，Full Jitter 通过随机化避免了多个客户端在同一时刻重试的同步碰撞。
重试支持 context.Context 取消——当守护进程收到 SIGTERM 信号时，ctx.Done() channel 关闭，重试循环立即退出，不会在关闭过程中卡住。最大重试次数和基础延迟均可通过配置文件调整。
4.7 URL 智能识别与标题抓取
fetcher 包实现了对 URL 类型剪贴板内容的增强处理：
* URL 检测：通过预编译的正则 https?://[^\s]+ 判断文本是否为 URL，同时限制长度 < 200 字符以排除包含 URL 的长文本。
* 标题抓取：使用标准 HTTP 客户端发送请求，禁用重定向跟随（ErrUseLastResponse），通过正则提取 <title> 标签内容。读取限制为 1MB 防止处理过大页面。
* 超时控制：FetchTitle 接受 context.Context 参数，由调用方（enhanceAndSend）设置 2 秒超时。超时不影响同步——URL 标题抓取是增强性功能，失败时回退到原始 URL。
这个功能的价值在于：当用户复制一个技术文章的 URL 时，knas 不仅归档了 URL 本身，还抓取了文章标题作为元数据，使得后续在 NAS 上检索时能通过标题快速定位。
4.8 Shell 注入防护
所有传递给远程 Shell 的路径参数都经过 shellEscape() 函数处理，使用 POSIX 单引号包裹并转义内部单引号：

func shellEscape(s string) string {
    return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
这是一个经过实战验证的 Shell 转义策略——单引号内只有单引号本身需要转义，其他所有特殊字符（$、`、\、;、|、& 等）都失去特殊含义。这比手动枚举需要转义的字符更安全、更简洁。
4.9 安全设计
knas 在多个层面考虑了安全性：
* 传输加密：SSH 协议提供端到端加密，无需自建 TLS。
* 认证：SSH 公钥认证，私钥不离开本机。
* 主机密钥验证：使用 knownhosts 回调验证服务器身份。首次连接时自动保存主机密钥（TOFU 策略），后续连接验证一致性。
* 敏感词过滤：默认排除包含 "password"、"密码"、"token" 的内容，防止敏感信息被同步到 NAS。
* Shell 注入防护：所有远程命令的路径参数经过单引号转义。
* Secret 认证：Relay 拉取请求携带 X-Auth-Key 认证头，防止未授权访问。
* PID 文件管理：守护进程写入 PID 文件，--stop 命令通过 PID 精准终止进程，避免误杀。
4.10 配置系统的向后兼容
配置管理采用了"宽松读取 + 默认补全"的策略：
* config.Load() 读取 JSON 配置文件，缺失字段使用 Go 的零值。
* main.go 中在加载配置后补全旧配置缺失的默认值（如 MaxLength、MinLength），确保用户升级 knas 后无需手动修改配置文件。
* 配置路径通过 expandPath() 展开 ~ 为实际家目录，支持跨环境部署。
* 四段式配置（SSH、Clipboard、Sync、Relay）结构清晰，每段独立配置、独立默认值。
五、并发模型
knas 充分利用了 Go 语言的 CSP 并发原语：
Goroutine 分工：
* Monitor goroutine：独立的轮询循环，通过 time.Ticker 驱动，收到 stopChan 信号后退出。
* enhanceAndSend goroutine：URL 标题抓取在独立 goroutine 中执行，带 2 秒超时，不阻塞轮询。
* handlePayload goroutine：每个同步操作在独立 goroutine 中执行，避免阻塞主循环。
* Relay puller goroutine：独立的 HTTP 拉取循环。
Channel 通信：
* itemChan（带缓冲的 Payload channel）：Monitor → 主循环，缓冲区满时丢弃（背压保护）。
* stopChan（信号 channel）：优雅关闭各个 goroutine。
锁使用：
* sync.RWMutex（Monitor）：保护 lastHash/lastContent/lastType，读多写少场景优化。
* sync.Mutex（Store）：保护 JSONL 文件的读写和计数操作。
* sync.Mutex（SSH Client.connMu）：保护连接状态和重连逻辑。
* sync.WaitGroup（Monitor）：确保 Stop() 等待轮询 goroutine 退出后再关闭 channel。
优雅关闭：
守护进程通过 signal.NotifyContext 监听 SIGINT/SIGTERM。收到信号后：
1. Monitor 停止轮询并等待 goroutine 退出。
2. PID 文件被清理。
3. SSH 连接断开。
4. Relay puller 停止。
整个关闭过程是确定性的——不依赖 os.Exit，不依赖垃圾回收，每个资源都有明确的清理路径。
六、CLI 设计
knas 的命令行接口支持多种操作模式：
命令	功能	实现
knas --daemon	守护进程模式	写 PID 文件，进入事件循环
knas --stop	停止守护进程	读取 PID，发送 SIGTERM
knas --status	查看运行状态	检查 PID 活性，显示统计
knas history [n]	查看最近 n 条记录	读取 JSONL，倒序展示
knas restore <id>	恢复内容到剪贴板	按 ID 查找，文本直接写剪贴板，图片通过 SSH 读取远程文件
--stop 在配置加载之前处理（无需配置即可停止），--status 在配置加载之后处理（需要显示 SSH 信息）。这种分层处理避免了对无用资源的初始化开销。
图片恢复功能（restore）是一个值得注意的设计：文本条目直接从历史记录读取内容写回剪贴板，但图片条目只存储了元信息（[IMAGE] N bytes），实际数据在远程 NAS 上。恢复时临时建立 SSH 连接，通过 ReadFile() 读取远程 PNG 数据，再写入系统剪贴板。这体现了"本地存元数据、远程存数据"的分层存储理念。
七、文件命名与归档策略
同步到 NAS 的文件按照以下规则命名和组织：

~/knas_archive/
├── 2026/
│   ├── 04/
│   │   ├── 18/
│   │   │   ├── 133119_knas_已成功运行.md
│   │   │   ├── 142545_关于量化交易的思.md
│   │   │   ├── 150405_a1b2c3d4_image.png
* 目录结构：年/月/日/ 三级目录，自然对应时间维度，便于按日期浏览和清理。
* 文件名：HHMMSS_<内容前缀>.md（文本）或 HHMMSS_<hash8>_image.png（图片）。时间戳前缀保证同一天内的排序，内容前缀提供语义信息（一眼看出文件内容是什么），哈希后缀支持去重检查。
* 内容前缀提取：extractContentPrefix() 清理空白字符，只保留字母、数字、中文和常见符号，截取前 N 个字符（可配置，默认 20）。空内容回退到 "untitled"。
* YAML frontmatter：每个文本文件包含 sync_time（同步时间）、source（来源，固定为 clipboard）、content_hash（MD5 哈希，用于远程去重）。这种格式兼容静态站点生成器（如 Hugo），为未来的知识库集成预留了可能性。
八、守护进程生命周期管理
8.1 PID 文件与进程管理
守护进程启动时通过 writePidFile() 将当前进程 PID 写入 ~/.knas/knas.pid。如果写入失败（目录不存在、权限不足），进程直接 log.Fatalf 终止——这是一个关键的安全加固点，因为 PID 文件写入失败意味着后续 --stop 命令无法找到进程。
--stop 通过 syscall.Kill(pid, SIGTERM) 发送终止信号，然后清理 PID 文件。--status 不仅检查 PID 文件是否存在，还通过 syscall.Kill(pid, 0) 验证进程是否真的在运行。如果进程已死但 PID 文件残留，会自动清理。
8.2 macOS LaunchAgent 集成
knas 支持通过 macOS LaunchAgent 实现开机自启和崩溃自动重启。LaunchAgent 的 KeepAlive 属性确保进程异常退出后自动重启。结合守护进程级的 SSH 连接重试循环，形成了一个双层的可用性保障：LaunchAgent 负责进程级的重启，knas 自身负责连接级的重连。
九、测试策略
项目为每个核心包编写了单元测试：
* history/store_test.go：覆盖 Append 基本功能（ID 自动生成、内容正确性）、Recent 倒序返回、空文件处理、Find 按 ID 精确查找、Compaction 触发后保留条数正确性。
* clipboard/monitor_test.go：覆盖哈希函数（相同内容同哈希、不同内容不同哈希、长度正确性）、Monitor 默认值、ShouldFilter 的 7 种边界条件（过短、过长、包含敏感词、中文敏感词、正常内容、空排除列表、恰好等于最小长度）。
* ssh/client_test.go：覆盖 expandPath 的多种场景（有缓存 homeDir、无缓存回退、root 用户）、ensureConnected 在无服务器时的行为、SyncImage 文件名格式。
测试使用 t.TempDir() 创建临时目录，每个测试用例完全隔离，不依赖外部状态。Compaction 测试使用 NewStoreWithLimit 设置小的阈值（10 条），写入 21 条后验证恰好保留 10 条。
十、依赖管理与分发
10.1 依赖极简化
knas 的外部依赖极其精简，仅有三个直接依赖：
* golang.design/x/clipboard：跨平台剪贴板读写（文本 + 图片）。
* golang.org/x/crypto/ssh：SSH 协议实现，支持公钥认证和 known_hosts。
* github.com/google/uuid：UUID 生成，用于历史条目 ID。
没有 Web 框架、没有 ORM、没有配置管理库、没有日志框架。这种极简主义减少了攻击面、降低了依赖维护成本，也使得二进制文件保持小巧。
10.2 NPM 分发
虽然 knas 是 Go 程序，但通过 NPM 分发。这是一种务实的策略——目标用户（开发者）几乎都安装了 Node.js，npm install -g knas 比下载二进制文件更便捷。package.json 中的 postinstall 脚本负责根据平台下载预编译的 Go 二进制文件。
十一、架构演进历程
knas 的架构经历了多次迭代优化，每次迭代都针对实际生产环境中的问题：
第一轮：基础功能验证。实现了剪贴板监听、SSH 同步、按日期归档的基本流程。这个阶段验证了技术可行性。
第二轮：去重体系建立。发现无去重导致 NAS 上大量重复文件。先后实现了内存哈希去重、status.json 持久化、远程 content_hash 去重三层防护。每一层解决上一层无法覆盖的场景。
第三轮：连接韧性。网络波动导致 SSH 断线后同步完全停止。引入了 ensureConnected() 自动重连、SendRequest 轻量探活、守护进程启动时的无限重试循环。将 Connect 拆分为 Connect + connectLocked 避免死锁。
第四轮：历史回溯。用户需要找回被覆盖的剪贴板内容。引入了 JSONL 历史存储、带压缩的轮转机制、history / restore 命令行接口。图片恢复通过临时 SSH 连接读取远程文件。
第五轮：安全与过滤。发现 Relay 路径绕过了剪贴板的敏感词过滤。将过滤逻辑提取为独立的 ShouldFilter() 纯函数，在两条路径中统一使用。加固了 PID 文件写入的错误处理、status 保存的错误日志。
第六轮：路径修复。发现 expandPath 硬编码 /home/ 导致 root 用户和 macOS 远程主机路径错误。引入了 resolveRemoteHome() 动态解析远程家目录并缓存。
每次迭代都遵循"最小改动"原则——只解决当前问题，不做过度设计。三层去重不是一开始就设计好的，而是在实际使用中发现每一层的不足后逐层叠加。连接韧性也不是预先规划的，而是观察到网络波动后的实际影响后针对性修复。
十二、性能考量
12.1 轮询效率
剪贴板轮询间隔 500ms，使用 time.Ticker 而非 time.Sleep——Ticker 会补偿延迟，确保长期运行的计时精度。每次轮询优先检查图片（通常是空的），然后才读取文本，这个顺序是经过考量的：图片读取开销小（无图片时返回空切片），文本读取需要字符串转换。
12.2 SSH 连接复用
所有操作共享同一个 SSH 连接，通过 ensureConnected() 按需重连。避免了每个操作建立独立连接的开销（TCP 握手 + SSH 密钥交换 + 认证，通常需要数百毫秒）。SendRequest 探活比 NewSession + echo 轻量一个数量级。
12.3 JSONL 追加写入
历史文件使用追加模式打开（O_APPEND），每次只写入一行。不需要读取已有内容，不需要解析 JSON 数组，不需要内存中维护完整列表。这是 JSONL 相对于 JSON 数组的核心优势——追加复杂度 O(1) 而非 O(n)。
12.4 Channel 背压
itemChan 设置了 10 个缓冲槽。当消费者（SSH 同步）跟不上生产者（剪贴板轮询）时，channel 满后的 select-default 分支直接丢弃并记录日志，而不是阻塞生产者。这确保了剪贴板轮询永远不会因为 SSH 慢而卡住。
十三、总结
knas 是一个小而全的 Go 项目，在约 2000 行代码中涵盖了系统编程的多个核心领域：进程管理（守护进程、PID 文件、信号处理）、并发编程（goroutine、channel、mutex、WaitGroup）、网络编程（SSH 协议、HTTP 客户端）、文件系统操作（JSONL 追加写入、原子压缩、远程文件操作）、安全（SSH 认证、Shell 注入防护、敏感词过滤）。
其核心设计哲学——三层去重、连接自愈、统一过滤、原子压缩、Payload ADT——都是在实际使用中遇到问题后针对性设计的，每一条都有明确的动机和解决的场景。这种从问题驱动设计的思路，使得每一行代码都有存在的理由，没有为了模式而模式的抽象，也没有为了扩展性而预留的钩子。

