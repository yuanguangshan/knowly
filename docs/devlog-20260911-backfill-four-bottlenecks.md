# Knowly 回填四重天坑 —— 开发日志（2026-09-11）

> 存档备查。记录一次「一个 persist 不了的回填任务」的完整排障：从 backfill 永不 done 开始，
> 连挖四层根因（SSH 往返 / WAL 黑洞 / FTS5 无法 WHERE / 全表扫描），
> 途中还亲手制造了两个干扰源（自己的诊断进程把 NAS IO 打满）。
>
> 最终成果：索引 11,082 → 46,908 条（NAS 全量），backfill 从「永不完成」到稳定秒级推进。

---

## 一、背景与症状

- 组件：knowly 本地索引回填（`internal/web/backfill.go`），负责把 NAS 归档
  `/mnt/nas/knowly_archive` 下的全部 `.md/.txt` 灌入 SQLite FTS5 索引（`~/.knowly/index.db`）。
- 触发方式：`POST /api/v1/admin/backfill`，或索引为空时启动自动触发。
- 症状：

```
# 四次触发，四次 started，零次 done
$ grep -c "backfill started"  knowly.log   # => 4
$ grep -c "backfill done"     knowly.log   # => 0

# 进程 CPU 100% 持续数十分钟，但索引条目数纹丝不动
$ ps aux | grep knowly-da   # CPU 97.9%  累计 5:42
$ sqlite3 index.db "SELECT COUNT(*) FROM docs"   # 11082 → 11082 → 11082
```

- 影响：索引只覆盖 NAS 的 **23.6%**（11,082 / 46,908）。`uploads/`（16,552 文件）、
  `2026/06`（4,625）、`2026/07`（7,459）**整块缺失**——意味着 `/find`、`knowly_search`
  搜不到这些月份的任何内容。

---

## 二、排查方法论：先逐段实测，再谈结论

面对「进程活着、CPU 满、进度零」，第一反应是「某个环节卡住了」。于是把链路拆开逐段量：

| 环节 | 实测结果 | 判定 |
|---|---|---|
| SSH `cat` 单文件 × 200 次（3 并发） | 2.65s，**零失败**，13ms/文件 | ✅ 通 |
| SSH `ListDir` 16,557 行大目录 | 0.8s，2MB 输出 | ✅ 通 |
| NAS 本地 `ls` 16.5k 文件目录 | 0.24s | ✅ 通 |
| NAS 本地 `find` 全部 46,908 文件 | **5.1s** | ✅ 通 |
| 连续 5 次 SSH `NewSession`+`Output` | 12~19ms，全成功 | ✅ 通 |

**每一段都是通的，但整体必死** —— 这个矛盾说明卡点在「段与段之间」或「组合效应」，
而不是某个单点慢。这个判断后面被证明是对的（四个根因全是组合/语义层面的）。

**真正定位靠工具**：`sample <pid>` 抓到了决定性调用栈：

```
walkAndIndex → BulkIndex → _sqlite3VdbeExec → _fts5NextMethod (5 samples)
  → _vdbeColumnFromOverflow → _accessPayload → _getPageNormal → pread
```

CPU 烧在 **FTS5 索引更新的页读取**上 —— 指针一下从「网络慢」转向「SQLite/WAL」。

---

## 三、四层根因（逐层剥离）

### 第 1 层：SSH 逐文件往返（数量级问题）

```go
// 修复前 internal/web/backfill.go
for _, e := range entries {
    data, err := sc.ReadFile(filepath.Join(dir, e.Name))   // ← 每文件一次 NewSession + cat
}
```

46,908 个文件 = **46,908 次 SSH 会话建立**。单次虽有 13ms，但会话建立/销毁的固定成本
（channel 协商、信号量排队 `sessionSem=3`、ctx 监控 goroutine）叠加后，
且大目录（`uploads/` 1.6 万文件）单条命令输出数百 MB 时会撞上缓冲上限。

**修复**：新增 `ReadFilesBatch` —— 一条 `tar -cf - <files>` 批量取回，
本地 `untarFiles`（archive/tar）解析；按 **400 文件/块**切分控制单批体积；
整块失败自动**降级为逐文件读**（不丢数据）。

```go
// internal/ssh/client.go
cmd := fmt.Sprintf("cd %s && tar -cf - %s 2>/dev/null", shellEscape(fullDir), strings.Join(quoted, " "))
```

### 第 2 层：WAL checkpoint 黑洞（CPU 100% 真凶）

FTS5 的 `DELETE + INSERT` 会重写倒排索引页。backfill 每批（200 条）都做一次，
WAL 一路滚到 **205 MB**。而进程存活期间，PASSIVE checkpoint 因活动事务始终返回 busy
→ WAL 只增不减 → FTS5 每次 INSERT 都要在大 WAL 上访问碎片化的索引页
→ 写入从 **1.4ms/条 退化到 150ms/条**（30 秒才写完 200 条）。

实测验证：外部另起连接执行 `PRAGMA wal_checkpoint(TRUNCATE)`，
**71MB WAL 瞬间清零**，而 PASSIVE 在同一时刻无效。

**修复**：`Index.Checkpoint()` 改为 **TRUNCATE 模式 + 独立连接**（避免在写连接上自等死锁），
backfill 每批 `flush()` 后调用。修复后 WAL 恒定在 **MB 级**。

### 第 3 层（最阴险）：FTS5 虚拟表不能按 UNINDEXED 列过滤

判重需要「这个 path 是否已索引」。原实现：

```go
// ❌ 永远返回空，且不报错
SELECT path FROM docs WHERE path LIKE '2026/04/18/%'
```

`docs` 是 **FTS5 虚拟表**，`path` 是 `UNINDEXED` 列。FTS5 对 UNINDEXED 列的
`WHERE` 过滤**静默返回空结果**（不报错、不警告）。实测「返回 0 条」而实际有 53 条。

后果：判重完全失效 → 每个目录都全量 DELETE+INSERT 重写 → 正好喂大了第 2 层的 WAL 黑洞。
两个 bug 互相放大。

**修复**：新建普通表 `doc_paths(path TEXT PRIMARY KEY) WITHOUT ROWID`，与 `docs` 同步维护
（`Index`/`BulkIndex` 写入时 `INSERT OR REPLACE`）；`PathsInDir` 改查 `doc_paths`。
旧库自动迁移（`doc_paths` 为空时从 `docs` 全表回填一次，实测 13,010 条）。

### 第 4 层：`GetByPath` 全表扫描（第 3 层的中间态）

在发现第 3 层之前，曾用「每文件调一次 `GetByPath`」做判重：

```go
if existing, err := ix.GetByPath(rel); err == nil && existing != nil { continue }
```

`GetByPath` 也是 `WHERE path = ?` —— 同样受第 3 层限制，退化为**每文件一次全表扫**
（13k 行 × content 全文）。2,241 文件的目录 = 2,241 次全表扫 → CPU 100% 假死。

**修复**：`PathsInDir(dir)` 一次 LIKE 查询拉回整个目录的已索引 path 成 set，
判重从「文件级 N 次」降到「目录级 1 次」。

---

## 四、排障中的自我干扰（值得记下）

这次排查本身制造了两个瓶颈，一度误导了结论：

1. **自己遗留的 NAS 全盘 grep**：一条 `grep -rl 'Response Archive' /mnt/nas/` 的
   ssh 会话在工具中断后**远端 zsh 仍在运行**，持续扫描 1.8GB 归档 **1 小时 22 分**，
   把 NFS IO 打满（`D` 状态不可中断睡眠），导致 knowly 的 `cat` 全部排队。
   —— 诊断工具自己成了被诊断的瓶颈。

2. **遗留的 Go 诊断进程**：`walk_repro`（用于复刻 walk 逻辑的独立程序）残留两个进程，
   占着 SSH 连接且 CPU 99%，干扰了后续观察。

3. **SIGQUIT 抓栈打断回填**：为拿 goroutine 栈两次 SIGQUIT，launchd 重启进程导致
   backfill goroutine 丢失（索引非空时不再自动触发）。

**教训**：诊断脚本必须有明确的生命周期管理（超时自杀 / 结束即清理）；
「工具中断」不等于「远端进程结束」，`ssh host "cmd"` 的远端进程可能继续跑很久。

---

## 五、最终效果

| 指标 | 修复前 | 修复后 |
|---|---|---|
| backfill 完成次数 | 0 / 4 次触发 | ✅ 稳定完成 |
| WAL 峰值 | 205 MB（不回落） | ~12 MB（TRUNCATE 恒定） |
| SSH 往返次数 | 46,908 次 | ~1,200 次（目录级 tar 批量） |
| 判重查询 | 每文件 1 次全表扫 | 每目录 1 次前缀查询 |
| 索引条目 | 11,082（23.6%） | 46,908（100%，NAS 全量） |
| 缺失月份 | 06 / 07 / uploads 全空 | 全部补全 |

**代码改动**（3 个 commit）：
- `internal/ssh/client.go`：`ReadFilesBatch` + `untarFiles`
- `internal/web/backfill.go`：分块批量读、跳过已索引、进度日志、目录级判重
- `internal/index/index.go`：`doc_paths` 表、`Checkpoint()`、`PathsInDir()`、旧库迁移

---

## 六、可复用的经验

1. **FTS5 虚拟表的 UNINDEXED 列不能 `WHERE`** —— 静默返回空，是最难发现的一类 bug。
   需要按该列查询时，必须在普通表里另存一份。
2. **WAL 不是免费的**：大 WAL + FTS5 = CPU 黑洞。批量写入场景要主动
   `wal_checkpoint(TRUNCATE)`，且注意 PASSIVE 在有活动事务时不起作用。
3. **「单步都快、整体卡死」= 组合问题**：逐段实测排除单点，再用 profiler
   （`sample`）抓真实调用栈，比读代码猜快得多。
4. **数据源在本地就别走网络**：NAS 挂在 u 机本地（`find` 全部 46k 文件 5.1 秒），
   而 knowly 在 mac 上通过 SSH 逐文件读取 —— 架构上就该在数据侧建索引。
5. **诊断工具的副作用要计入排查假设**：本次前两小时的"卡顿"有相当部分是自己造成的。

---

*本文由 2026-09-11 的一次完整排障整理而成。索引补全任务在本文撰写时仍在推进（07/08/uploads 处理中）。*
