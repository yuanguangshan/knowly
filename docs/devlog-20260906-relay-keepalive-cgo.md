# Knowly Relay 超时修复 + CGO 构建崩溃大坑 —— 开发日志（2026-09-06）

> 存档备查。记录一次「小症状引出大坑」的完整排障：从 Relay 拉取间歇超时开始，途中误用 `CGO_ENABLED=0` 构建导致 daemon 崩溃循环，最终定位到两层根因并修复。

## 一、背景与症状

- 组件：本机 knowly 剪贴板同步守护进程（v6.82.0，Go 二进制，launchd 托管 `com.knowly.daemon`）
- 首次报错（用户反馈）：

```
2026/09/06 16:43:55 [DEBUG] Relay pull failed: Get "https://knasync.want.biz/pull?queue=general":
         context deadline exceeded (Client.Timeout exceeded while awaiting headers)
```

- 观察日志发现是**间歇性**超时：当天失败 16 次 / 成功 54 次，失败呈小簇爆发（如 16:24:23/33/43）。
- 另有同源错误：`Relay pull failed: ... unexpected EOF`（10:06）、`Result pull failed`（/results 端点）。

## 二、第一层根因：keep-alive 半开连接

### 排查中排除的假设（实测）

| 假设 | 结果 |
|---|---|
| 服务端挂了 | ❌ curl / Go 客户端连续 5 次新连接全部 204/200，0.7~1.5s 秒回 |
| 密钥不对 | ❌ Go HTTP/2 带密钥正常；**服务端对 HTTP/1.1 返回 403**（Python urllib 测得） |
| DNS/网络不通 | ❌ 公网解析到 Cloudflare（104.21.44.228）；本机 Surge 运行中（DNS 解析 198.18.2.67 = fake-ip，TUN 透明接管所有流量） |

### 结论

- 客户端代码 `internal/relay/puller.go:29` / `result_puller.go:47` 使用共享 keep-alive `http.Client{Timeout: 10s}`。
- Surge 网络变动（切节点/规则/TUN 重建）会静默作废旧 TCP 连接为**半开状态**；Go 复用该连接时「写入成功、读响应挂起」→ 卡满 10s 报 `awaiting headers`。
- 新连接（外部测试）每次都成功 ⇒ 服务端健康，问题在连接复用。

### 修复（按广山哥指示：每次复用都失败，复用就没意义 → 直接禁用）

`internal/relay/puller.go` 与 `result_puller.go` 两处 client 改为：

```go
client: &http.Client{
    Timeout: 10 * time.Second,
    Transport: &http.Transport{
        DisableKeepAlives: true, // 不复用连接，规避半开连接超时
        ForceAttemptHTTP2: true, // 服务端对 HTTP/1.1 返回 403，保持 HTTP/2
    },
},
```

8s 轮询场景下 keep-alive 收益极低（每请求 0.7~1.5s），代价却是一次 10s 超时，禁用最省事。

## 三、第二层（大坑）：CGO_ENABLED=0 构建导致 daemon 启动即崩

### 事故经过

用 `CGO_ENABLED=0` 重建二进制替换 `/opt/homebrew/lib/node_modules/knowly/bin/knowly-darwin-arm64` 后，launchd 进入崩溃循环：

- 现象：进程启动 0.1~0.5s 后静默退出，`launchctl list` 显示 `- 2`（exit code 2），**无任何 panic/fatal 输出**；launchd 默认 10s throttle 导致每 ~11s 一轮重启。
- 最初误判为「我改的 relay 代码导致崩溃」，经大量实验排除：
  - HEAD 干净版（无我的改动）同样崩 ⇒ 与 relay 改动无关
  - 环境变量二分（env -i 各种组合）全都崩 ⇒ 与环境无关
  - 剪贴板清空仍崩、SQLite `PRAGMA integrity_check` 全 ok、NAS 健康（.213 Ubuntu-R86S 与 .186 群晖均可读写）⇒ 皆排除
  - lldb 无调试权限（沙箱）；SIGQUIT goroutine dump 因为 `os.Stderr = f` 只改 Go 变量不改 fd 2 而输出到了被丢弃的 stderr
- **破案线索**：`go tool nm` 对比新旧二进制符号，旧二进制（能稳定运行）含 `_cgo_*`、`_clipboard_*` 符号 —— **旧版是 CGO_ENABLED=1 构建的**！
- 用 `CGO_ENABLED=1` 重建 ⇒ **立即稳定**（env -i 下 8s+ 存活，launchd 下持续运行）。

### 为什么 CGO=0 会 exit 2

未深入 runtime 层（非本次目标）；经验结论：**这台 Mac 上 knowly 必须 CGO=1 构建**（clipboard 等依赖 C 实现）。`make build` / `make darwin` 默认即 CGO=1，**手动构建时不要加 `CGO_ENABLED=0`**。

### 排障过程中的其他坑（备查）

| 坑 | 说明 |
|---|---|
| `$!` 陷阱 | `cd / && env -i ... binary &` 的 `$!` 可能是 bash 子 shell，`kill $!` 杀不干净，残留进程占 pid 锁（`knowly.pid` Flock）和 8090 端口 → 后续实例 `log.Fatalf("另一个 Knowly 守护进程正在运行")` exit 1。**残留进程一度把「旧二进制稳定」误判为对照结论**。 |
| `os.Stderr = f` 不改变 fd 2 | Go 侧赋值不影响 runtime 写 fd 2；panic / SIGQUIT dump 仍走原始 stderr。launchd 下会进 `daemon.log`（StandardErrorPath），重定向实验里则被 `/dev/null` 吃掉，导致「无输出」假象。 |
| Flock 锁冲突 | launchd 实例与手动跑的实例共用 `~/.knowly/knowly.pid` 排他锁；测试前务必 `pgrep -lf knowly` 清理。 |

## 四、最终修复与验证

1. 正式版 = CGO=1 + keep-alive 禁用（relay 两文件改动），安装路径 `/opt/homebrew/lib/node_modules/knowly/bin/knowly-darwin-arm64`
2. 备份：`knowly-darwin-arm64.bak_0906`（原 Sep5 版）、`knowly-darwin-arm64.bak_0906_headclean`（CGO=0 HEAD 版）
3. launchd：`com.knowly.daemon` 正常托管，运行 PID 持续稳定
4. 验证：观察数分钟 **0 崩溃、0 新增 `Relay pull failed` / `Result pull failed`**；relay 全链路正常（拉取 → URL 抓取 1.6s → 同步 `/mnt/nas/knowly_archive/...` → push OK）

## 五、经验教训

1. **症状 vs 根因**：间歇性超时类问题先验证服务端（新连接实测），再怀疑客户端连接池；keep-alive 在低频轮询场景价值极低。
2. **改代码后构建行为要与原二进制对齐**：先 `go version -m <原二进制>` 对比 Go 版本 / CGO / 符号，再动手构建，能避开一整轮崩溃事故。
3. **崩溃循环排查三件套**：`launchctl list`（exit code）、`ps/pgrep`（残留进程）、`known_hosts`/锁文件（并发冲突）；先清理环境再下结论。
4. **二分法有效**：stash 改动 → 构建 HEAD 干净版对照，一步排除「我的改动导致崩溃」。
5. **用户直觉值得信**：广山哥一句「每次复用都失败？那复用还有什么意义」直接指向最终修复方向。

## 六、涉及文件 / 命令（备查）

- 源码：`/Users/ygs/ygs/knowly/internal/relay/puller.go`、`result_puller.go`（改动未提交，建议 commit）
- 二进制：`/opt/homebrew/lib/node_modules/knowly/bin/knowly-darwin-arm64`
- 配置/日志：`~/.knowly/config.json`（relay.endpoint/secret）、`~/.knowly/knowly.log`、`~/.knowly/daemon.log`
- launchd：`~/Library/LaunchAgents/com.knowly.daemon.plist`
- 关键命令：
  - 重启：`launchctl kickstart -k "gui/$(id -u)/com.knowly.daemon"`
  - 构建：`cd /Users/ygs/ygs/knowly && CGO_ENABLED=1 go build -o bin/knowly-darwin-arm64 ./cmd/knowly`（**必须 CGO=1**）
  - 排障：`curl -H "X-Auth-Key: ..." https://knasync.want.biz/pull?queue=general`；`go version -m <binary>`；`go tool nm <binary> | grep cgo`