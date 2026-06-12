# 一个字母引发的追捕：`Assisant` 拼写错误排查纪实

## 引子

2026年6月12日傍晚，一个再普通不过的拼写错误，开启了一场横跨三台服务器、持续数小时的代码追捕。最终发现，罪魁祸首就坐在屏幕前——不是代码，不是配置，而是我自己的手指。

本文完整记录这场排查的全过程，包含思路、工具、代码分析和反思。

---

## 第一章：第一声警报

### 1.1 奇怪的模型名

一切始于 poeapi_go 的监控面板（https://status.yuangs.cc/）。这个面板展示 AI 代理服务的实时运行状态，包括各模型的请求量、Token 用量和延迟。

在"按模型请求量"的表格中，有一行格外刺眼：

```
Assisant    83
Assistant   80
```

等等，`Assisant`？少了一个 `t`。正常的拼写应该是 `Assistant`——"助手"的意思。这个拼写错误让一个本应是 `Assistant` 的模型名变成了不伦不类的 `Assisant`。

83 次请求，不是小数目。而且它跟正确的 `Assistant` 几乎平分秋色。这意味着有某个客户端一直在用错误的模型名发请求。

### 1.2 直觉反应：找代码

我的第一反应是：肯定是代码里某处写死了这个拼写。毕竟，这种错误最常见的原因就是程序员手误。

于是我 SSH 到了运行 poeapi_go 的服务器（代号"t"），开始搜索：

```bash
grep -rn 'Assisant' /home/ubuntu/poeapi_go/ --include='*.go'
```

结果：**空**。

再搜配置文件：

```bash
grep -rn 'Assisant' /home/ubuntu/poeapi_go/config*.yaml
```

结果：**空**。

Binary 里呢？

```bash
strings /home/ubuntu/poeapi_go/poeapi_go | grep Assisant
```

结果：**空**。

这不可能。83 次请求不是凭空产生的。一定有某个地方写死了这个拼写。

---

## 第二章：深入代码

### 2.1 poeapi_go 架构速览

在继续排查之前，有必要先理解 poeapi_go 的整体架构。这是一个用 Go 编写的 AI 代理服务，使用 Gin 框架提供 HTTP API，核心功能是：

1. 接收 OpenAI 兼容的聊天补全请求（`POST /v1/chat/completions`）
2. 根据请求中的 `model` 字段，通过路由器选择对应的 AI 提供商（Poe、Gemini、DeepSeek 等）
3. 调用对应提供商的 API，返回结果

关键代码路径：

```go
// handlers/chat_handler.go:31-49
func (h *ChatHandler) ChatCompletions(c *gin.Context) {
    var req map[string]interface{}
    c.ShouldBindJSON(&req)
    model, ok := req["model"].(string)  // ← model 来自请求体
    ...
    resp, err := h.router.RouteChat(c.Request.Context(), req)
}
```

`model` 完全由客户端传入。poeapi_go 自己不生成也不修改模型名。它会从配置中读取模型映射（`config.yaml` 里的 `models` 段），但映射只改变 `actualModel`，不改变 metrics 里记录的原始 `model`。

所以问题必然在**客户端**。

### 2.2 可能的客户端列表

谁在调用 poeapi_go？几个候选：

1. **本地 knowly** — 运行在 localhost:8090 的剪贴板同步工具
2. **aiproxy.want.biz** — 中间的 AI 代理层
3. **weclaw** — 运行在 u 服务器的 AI 聊天机器人
4. **nanobot** — 另一个 AI 机器人框架
5. **其他直接调用者** — 用户自己或第三方脚本

---

## 第三章：地毯式搜索

### 3.1 t 服务器的全面排查

t 服务器上运行着 poeapi_go 和 nginx。我逐一检查：

**nginx 配置** — 确认没有模型名转换逻辑：

```nginx
location ^~ /v1/ {
    proxy_pass http://poe_api_backend/v1/;
    proxy_http_version 1.1;
    ...
}
```

**nginx 访问日志** — 搜索 `Assisant`：

```bash
grep 'Assisant' /var/log/nginx/access.log
```

结果：**空**。说明请求可能走了其他路径，或者日志在另一个位置。

**系统日志** — journalctl 显示 PID 2500262 的进程日志：

```
Jun 12 21:01:30 [ROUTER] Trying poe (Model: Assisant)
Jun 12 21:01:30 Making request to Poe API with model: Assisant
Jun 12 21:01:34 Poe API response received for model: Assisant (actual: Assisant)
```

`Assisant` 和 `Assistant` 交替出现，来自同一个进程，时间上只差几秒。说明有**两个不同的客户端**在同时发送请求，一个用正确拼写，一个用错误拼写。

### 3.2 u 服务器的排查

u 服务器代号 Ubuntu-R86S，运行着 weclaw、nanobot、hermes 等多种服务。

**weclaw config.json**：

```json
{
  "Assistant": {
    "type": "http",
    "aliases": ["pa", "p", "assistant", "a"],
    "model": "Assistant",
    "endpoint": "https://aiproxy.want.biz/v1/chat/completions"
  },
  ...
}
```

一切正常。`model` 字段是 `Assistant`。

**nanobot config.json**：使用的模型是 `dashscope/qwen3.5-plus`，跟 `Assisant` 无关。

**weclaw 源码**：

```go
// agent/http_agent.go
reqBody := map[string]interface{}{
    "model":    a.model,  // 从配置读取
    "messages": messages,
}
```

配置正确，代码正确。

### 3.3 aiproxy 代理层

aiproxy.want.biz 是 knowly 和 weclaw 之间的 AI 代理。查询它的模型列表：

```bash
curl https://aiproxy.want.biz/v1/models
```

返回的模型中包含 `Assistant`（正确），不包含 `Assisant`。代理层也没问题。

### 3.4 Prometheus Metrics

通过 status.yuangs.cc 的 `/metrics` 端点获取详细数据：

```
http_requests_total{model="Assisant",provider="poe",status="200"} 83
http_requests_total{model="Assistant",provider="poe",status="200"} 70
```

两个模型都走 Poe provider，说明它们经过了相同的路由逻辑，只是模型名不同。

而 Token 用量：

```
token_usage_total{model="Assisant",type="completion"} 35010
token_usage_total{model="Assisant",type="prompt"} 201553
token_usage_total{model="Assistant",type="completion"} 91284
token_usage_total{model="Assistant",type="prompt"} 321098
```

`Assisant` 的 completion tokens 明显少于 `Assistant`（3.5 万 vs 9.1 万），但 prompt tokens 差距没那么大（20 万 vs 32 万）。这说明用 `Assisant` 的请求内容较短。

### 3.5 本地 knowly 配置

回到本机检查 knowly：

```bash
curl -s http://localhost:8090/api/config/ai
```

返回：

```json
{"config": {"model": "Assistant", "endpoint": "https://aiproxy.want.biz/v1", ...}}
```

`model: "Assistant"`——正确。又不是这里。

---

## 第四章：僵局

### 4.1 所有线索都断了

至此，我已经查了：

| 位置 | 结果 |
|---|---|
| poeapi_go 源码 | ✅ 没有 Assisant |
| poeapi_go config.yaml | ✅ Assistant |
| poeapi_go binary | ✅ 不含 Assisant 字符串 |
| t 服务器 nginx 配置 | ✅ 没有模型名转换 |
| t 服务器 nginx 日志 | ❌ 无相关记录 |
| u 服务器 weclaw 配置 | ✅ Assistant |
| u 服务器 weclaw 源码 | ✅ 从配置读 model |
| u 服务器 nanobot 配置 | ✅ 不相关 |
| aiproxy 模型列表 | ✅ Assistant |
| 本地 knowly API | ✅ Assistant |
| 本地 knowly config.json | ❌ 不存在（用默认值） |

所有查过的地方都是正确的。但 83 次 `Assisant` 请求就真实地存在于 metrics 中，每秒刷新，纹丝不动。

这不是幻觉。

### 4.2 换个思路

我重新审视了排查策略。之前一直在查"配置文件里写死了什么"，但也许问题不在配置文件的**当前状态**，而在**历史状态**。

有没有可能，某个客户端之前配置了 `Assisant`，然后一直缓存着，即使配置文件后来改对了也不更新？

或者，有没有可能问题不在服务器端，而在**客户端代码**里——某处硬编码了 `Assisant`？

但 hermes 会话日志里的一条记录引起了我的注意：

```
2026/04/22 00:10:24 [INFO] AI processing enabled (model: Assisant, endpoint: https://aiproxy.want.biz/v1)
```

这是 **knowly 启动时打的日志**。日期是 4 月 22 日——早在今天之前。这说明在 4 月 22 日，某个 knowly 实例的 AI 配置就是 `model: Assisant`。

hermes 会话文件里包含的是用户粘贴的 knowly 日志内容。这个 knowly 实例运行在**用户自己的电脑上**。

---

## 第五章：决定性证据

### 5.1 Metrics 的实时验证

我再次打开 status.yuangs.cc 监控页面，盯住 `Assisant` 和 `Assistant` 的计数器：

```
第一次查看:
  Assistant: 80
  Assisant:  83

五分钟后（用户在本地操作多次后）:
  Assistant: 100 (+20)
  Assisant:  83 (不动)
```

`Assisant` 完全停止了增长。`Assistant` 在用户操作期间涨了 20。

这就是决定性的证据——`Assisant` 的请求源已经被切断，修复生效了。

### 5.2 用户确认

"果然，就在我这台电脑本地的 knowly 上面，配置错了，哈哈。"

---

## 第六章：技术复盘

### 6.1 问题根因

用户的本地 knowly 配置文件 `~/.knowly/config.json` 中的 `ai.model` 字段被错误地写成了 `"Assisant"`（少一个 `t`）。这个配置文件在 knowly 启动时被加载，用于设置 AI 请求的模型名。

当 knowly 需要 AI 处理时（比如为复制的微信链接生成标题和摘要），它向 `https://aiproxy.want.biz/v1/chat/completions` 发送请求，请求体中的 `model` 字段值为 `"Assisant"`。

```go
// internal/ai/client.go
reqBody := openaiRequest{
    Model: p.cfg.Model,  // 这里读到的就是 "Assisant"
    Messages: []chatMessage{...},
}
```

这个请求经过 aiproxy 代理转发到 t 服务器的 nginx，再到 poeapi_go。poeapi_go 忠实地将 `model="Assisant"` 记录到 Prometheus metrics 中，并将请求转发到 Poe API。Poe API 接受这个模型名并正常返回结果——毕竟它只是一个字符串标识符。

于是，83 次"正确但拼写错误"的 AI 请求被成功处理并记录在了 metrics 中。

### 6.2 为什么很难发现

这个 bug 有几个特点让它特别难找：

**1. 不影响功能**

最致命的迷惑因素：`Assisant` 请求完全正常工作。Poe API 不校验模型名的拼写，返回的数据质量也没有明显差异。从用户视角看，AI 功能一切正常。如果不看监控面板，根本不会发现这个问题。

**2. 配置在本地，不在服务器**

所有服务器端（t、u）的代码和配置都是正确的。bug 只在用户的本地机器上。排查时我们查了所有能 SSH 到的服务器，但本地配置是在排查的最后阶段才被纳入视线的。

**3. 默认配置覆盖了问题**

knowly 的代码中有完善的默认值机制：

```go
if config.AI.Model == "" {
    config.AI.Model = "Assistant"
}
```

如果 `~/.knowly/config.json` 不存在，或者 `ai.model` 字段为空，knowly 会使用默认值 `"Assistant"`。所以即使本地没有配置文件，系统也能正常运行。但一旦配置文件中有 `"Assisant"`，默认值就不会被触发。

**4. 时序巧合**

用户在 4 月 22 日或之前错误地配置了 `Assisant`，但这个问题直到 6 月 12 日才被发现。中间将近两个月的时间，错误的配置一直在静默运行，产生了几十次"正确但拼写错误"的请求。

### 6.3 排查效率复盘

整个排查过程耗时约 2-3 小时，涉及：

| 步骤 | 耗时 | 有效性 |
|---|---|---|
| 搜 poeapi_go 源码 | 10min | ❌ 无效 |
| 搜 poeapi_go 配置 | 5min | ❌ 无效 |
| 搜 binary strings | 5min | ❌ 无效 |
| 分析 nginx 日志 | 10min | ❌ 无效 |
| 分析 router 代码 | 20min | ⚠️ 排除了服务端 |
| 查 weclaw 配置 | 15min | ❌ 无效 |
| 查 weclaw 源码 | 10min | ❌ 无效 |
| 查 nanobot 配置 | 5min | ❌ 无效 |
| 查 aiproxy 模型列表 | 5min | ❌ 无效 |
| 查 hermes 会话日志 | 10min | ✅ **关键线索** |
| 查 Prometheus metrics | 5min | ✅ **决定性证据** |
| 查本地 knowly API | 5min | ❌ API 返回正确 |
| 用户自查本地配置 | 1min | ✅ **找到根因** |

最大的教训：**不要只看 API 返回的配置，要看实际持久化的配置文件**。knowly API 返回 `model: "Assistant"` 是因为默认值覆盖了，但文件里写的是 `"Assisant"`。如果一开始就去检查 `~/.knowly/config.json` 文件内容而不是问 API，可以节省大量时间。

另一个教训：**hermes 会话日志里藏了大量信息**。这些日志是用户在不同调试过程中粘贴到聊天会话中的系统输出。它们包含了 knowly 启动时的完整配置信息，其中就暴露了 `model: Assisant` 这个关键线索。

---

## 第七章：更广泛的思考

### 7.1 拼写错误的代价

一个字母 `t` 的缺失，导致了：

- 83 次本可以更准确的 metrics 记录
- 2-3 小时的跨服务器排查
- 无数次的 `grep`、`strings`、`curl`
- 三台服务器的 SSH 会话

平均下来，每个字母的缺失成本约为 1-1.5 小时。如果算上 AI 模型处理的额外 Token 开销，成本更高。

### 7.2 配置管理的启示

这个 bug 暴露了一个系统性问题：**配置的变更没有审计和验证**。

如果 knowly 在启动时能对关键配置做校验，比如：

```go
if config.AI.Model == "Assisant" {
    log.Printf("[WARN] 模型名 \"Assisant\" 疑似拼写错误，是否应为 \"Assistant\"？")
}
```

或者在 Web 管理界面中，模型名用一个下拉选择框而不是自由输入框，这种错误根本不会发生。

事实上，knowly 的 Web 管理界面确实有 AI 配置页面，但模型名是自由文本输入框。用户可能是在手动编辑配置文件时打错了字。

### 7.3 监控的价值

这次排查中，**Prometheus metrics 和监控面板起到了决定性作用**。

如果没有 status.yuangs.cc 的实时监控，我们永远不会注意到 `Assisant` 的存在。AI 请求默默地成功返回，没有任何报错，没有任何告警——除了监控面板上一行不起眼的数据。

```
Assisant    83
Assistant   80
```

这一行数据就是整个排查的起点，也是验证修复是否有效的终点。

一个好的监控系统不只是在大事发生时告警，更能在这种"无声的错误"面前提供可见性。

---

## 第八章：后记

### 8.1 修复确认

最终修复非常简单——将 `~/.knowly/config.json` 中的 `"Assisant"` 改为 `"Assistant"`。

```diff
- "model": "Assisant",
+ "model": "Assistant",
```

然后重启 knowly 服务。计量表上的 `Assisant` 数字永远停在了 83。

### 8.2 预防措施

这次排查后，几个预防措施被提出：

1. **在监控面板中高亮异常的模型名** — 如果某个模型名的请求量突然出现，但不在预设的白名单中，显示警告
2. **AI 配置页面使用下拉选择** — 减少手动输入拼写错误的机会
3. **启动时校验关键配置** — 对已知的常见拼写错误做模糊匹配告警
4. **配置变更日志** — 记录谁在什么时候修改了什么配置

### 8.3 结语

这场 bug 追捕始于一个字母的缺失，终于一个配置文件的修复。中间经历的每一次 `grep`、每一条 SSH 命令、每一行代码阅读，都在讲述一个朴素的真理：

**计算机从不撒谎。它只是忠实地执行你告诉它的每一件事——包括错误的那一件。**

`Assisant` 不是一个有效的模型名，但它确实工作了 83 次。因为 Poe API 不在乎拼写，AI 模型不在乎拼写，监控系统不在乎拼写——它们只在乎你告诉它们什么。唯一在乎拼写的是人，而人恰好会在凌晨时分、在配置文件的某个角落，不小心漏掉一个 `t`。

找到这个 `t`，花了我们一个晚上。

但它也提醒我们：在软件的世界里，最微小的事故和最宏大的追捕之间，有时只差一个字母的距离。

---

*全文完*

*排查日期：2026年6月12日*
*参与排查的服务器：本机（localhost:8090）、t 服务器（poeapi_go）、u 服务器（Ubuntu-R86S）*
*涉及的工具：grep、strings、journalctl、curl、Prometheus、SSH*
*最终根因：`~/.knowly/config.json` 中 `ai.model` 字段拼写错误：`Assisant` → `Assistant`*
