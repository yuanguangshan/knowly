# 开发日志：历史存储 SQLite 迁移

- **日期**：2026-08-29
- **提交**：`53cd937` feat(history): 存储内核迁移到 SQLite，jsonl 降级为只读留档
- **规模**：+555 / −911 行（净删 356 行），`internal/history/store.go` 1161 行 → 约 700 行
- **部署**：已上线，daemon PID 85566，Web UI 200（28ms）

---

## 1. 背景与决策

当天早些时候完成了对 knowly 的全面性能评估（见 `devlog-20260829.md`）。评估中发现历史查看慢的真正瓶颈是 `/full` 接口的 SSH 往返（已单独修复，2.9s → 毫秒级），而非 history.jsonl 的 O(N) 扫描本身——这让 SQLite 迁移从「性能救火」变成了「架构还债」。

随后做了一次正式的工作量评估，结论：

| 工作项 | 评估量 |
|---|---|
| Schema + Open | ~50 行（照抄 index 包成熟先例，无新依赖） |
| Store 内核替换 | ~500 行改写 |
| jsonl 一次性导入 | ~40 行（幂等） |
| 外部消费者适配 | 1 处（实际发现 2 处） |
| 测试 | 半天（现有 6 个语义不变应直接通过） |

**合计：1 个专注工作日**。收益性质是「架构清爽 + 删代码 + 撑住十万条规模」多于「当下速度」。用户拍板：做。

## 2. 评估阶段的关键发现（决定了实现策略）

用接口面扫描代替拍脑袋：

- `history.Store` 共 **17 个公开方法、跨包 25 处调用**
- **调用方全部引用 `*history.Store` 具体类型**（不是接口）→ 只要保住方法签名，调用方零改动。这是整个方案工作量可控的核心原因
- `store.go` 内部有 **12 处 `readAll()` 依赖**——Stats/GetByID/三个 Update*/AllTags/TrimTo 全部漏斗到全量读取，这正是要换掉的部分
- 外部直读 `history.jsonl` 的地方有 2 处：`main.go` 的 `trim-history` 备份（`os.ReadFile`）和 `showStatus`（数行数），迁移后 jsonl 冻结，这两处必须改走 Store

## 3. Schema 设计

```sql
CREATE TABLE entries (
    id              TEXT PRIMARY KEY,
    content         TEXT NOT NULL DEFAULT '',
    type            TEXT NOT NULL DEFAULT 'text',
    timestamp       INTEGER NOT NULL,      -- UnixNano
    nas_path        TEXT NOT NULL DEFAULT '',
    title           TEXT NOT NULL DEFAULT '',
    publish_title   TEXT NOT NULL DEFAULT '',
    publish_summary TEXT NOT NULL DEFAULT '',
    manual_edit     INTEGER NOT NULL DEFAULT 0,
    tags            TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX idx_entries_timestamp ON entries(timestamp);

CREATE TABLE entry_tags (
    entry_id TEXT NOT NULL,
    tag      TEXT NOT NULL,
    PRIMARY KEY (entry_id, tag)
);
CREATE INDEX idx_entry_tags_tag ON entry_tags(tag);
```

**tags 双写设计的理由**：

- `entries.tags` JSON 列：**保序**。`UpdateEntry` 承诺「保持用户设置的标签顺序」，JSON 列原样保留
- `entry_tags` 倒排表：无序，仅服务 `FindByTag`（索引 JOIN）和 `AllTags`（GROUP BY）。若只用 JSON 列 + LIKE 查询，既慢又怕特殊字符

**并发模型**（与 index 包同一套先例）：

- DSN：`?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL`
- `SetMaxOpenConns(1)`：进程内天然串行，杜绝 SQLITE_BUSY；跨进程（daemon 与独立 `knowly web`）靠 WAL + busy_timeout 协调
- 保留 `sync.Mutex`：保护内存计数镜像 `count`（用于 Append 的压缩触发判断）

**为什么 Stats 不能用 SQL 的 date() 分桶**：SQLite 的 `date(ts,'unixepoch')` 只有 UTC，日趋势会整体错位一天。改为 `SELECT timestamp, type` 拉两列（2 万行毫秒级），在 Go 侧按本地时区 `Format("2006-01-02")` 分桶，ISO 周聚合逻辑与旧实现逐行一致。

## 4. 语义保持清单（易错点全录）

迁移最容易翻车的是隐含语义。实现前通读了全部 1161 行，逐条对齐：

| 语义点 | 旧实现行为 | 处理 |
|---|---|---|
| ID 格式 | `20060102150405_uuid前8位` | 原样保留 |
| Append 覆写 Timestamp | 无条件 `time.Now()` | 原样保留 |
| `Find(id)` | **前缀匹配**（`HasPrefix`），文件序第一个命中，未找到返回 `nil,nil` 不报错 | `substr(id,1,?) = ?` 参数化实现，避免 LIKE 通配符注入 |
| `GetByID(id)` | 严格匹配，未找到报错 | 区别于 Find，保持 |
| `UpdateTags` | **合并语义**（旧标签保留 + 新标签去重）；旧实现用 map 迭代导致顺序随机 | 改为确定性合并（旧序在前，新的追加） |
| `UpdateEntry` | title/summary 空则不改；`newTags != nil` 整体替换且**不去重保序**；`ManualEdit = !clearManualEdit` 无条件设置 | 逐条保持 |
| `UpdatePublishMeta` | `manual_edit=1` 时静默跳过返回 nil；条目不存在才报错 | `WHERE ... AND manual_edit=0` + 存在性二段检查 |
| compact 触发 | `count > 2×maxEntries` 时保留最新 `maxEntries` 条 | 内存计数镜像 + `DELETE WHERE rowid NOT IN (top max)` |
| Recent 空库 | 返回 `nil`（不是空切片），有测试锁定 | `var entries []Entry` 天然 nil |
| 排序稳定性 | 文件顺序 = 插入顺序；测试里同毫秒连续 Append | `ORDER BY rowid`（插入序）而非 timestamp，杜绝同秒乱序 |
| 坏行容错 | jsonl 解析失败跳过 | 迁移时同样跳过 |
| Stats 输出 | 30 天日趋势（含零日）+ 8 个 ISO 周 | Go 侧同款聚合，逐字节一致 |

`RecentAfter` 有一处**有意的行为改进**：旧实现只从最新 `maxEntries` 条的窗口里过滤，游标太老时会漏分页数据（逆向读窗口的 artifact）；SQL 版 `WHERE timestamp < ? ORDER BY rowid DESC LIMIT n` 在任何深度都精确返回。实际数据量下两者结果相同，SQL 版严格更正确。

## 5. 迁移设计

- **幂等**：`migrateFromJSONL` 先查 `SELECT COUNT(*) FROM entries`，表非空即跳过。删库重开也会从 jsonl 重新导，TrimTo 过的库不会重复导入（表非空）
- **jsonl 降级为只读留档**：不再写入，不删除。这是用户数据的三重保险之一
- **单事务导入**：1016 条在一个 tx 内完成，失败整体回滚不留半截状态

## 6. 外围适配

1. **`trim-history` 备份**：原来 `os.ReadFile(history.jsonl)` 直接上传，迁移后 jsonl 是陈旧的。改为 `histStore.ReadAll()` + `json.Encoder` 逐条写——NAS 备份文件格式（JSONL of Entry）不变，历史备份仍可回放
2. **`showStatus`**：原来数 jsonl 换行符，改走 `Store.Count()`

## 7. 测试报告

### 单元测试（7/7 通过）

| 测试 | 验证点 | 结果 |
|---|---|---|
| TestAppend | 追加、ID 生成 | PASS |
| TestRecent | 倒序列表 | PASS |
| TestRecentEmpty | 空库返回 nil 语义 | PASS |
| TestFind | 精确查找、未找到 nil,nil | PASS |
| TestCompaction | 21 条触发压缩保留最新 10 条（L..U 顺序断言） | PASS |
| TestStatsIncremental | TrimTo 后统计收敛 | PASS |
| **TestJSONLMigration**（新增） | 见下 | PASS |

**现有 6 个测试零修改直接通过**——语义保持的第一手证据。

### TestJSONLMigration 断言点

1. 手工构造 jsonl（含一条坏行）→ 迁移后 Count=2（坏行被跳过）
2. Recent 倒序正确（最新在前）
3. `manual_edit`、`title`（含中文）等字段无损
4. 倒排表可用：FindByTag 命中 2 条、AllTags 计数排序正确
5. **幂等**：重开同一目录不重复导入
6. UpdateTags 合并语义（2 旧 + 1 新 = 3）

### 全仓回归

`go test ./... -count=1` 全部通过（clipboard/config/fetcher/history/index/retry/ssh/web），`go vet` 干净。

## 8. 真实数据验证（部署级）

### 迁移执行

```
2026/08/29 20:56:30 [INFO] History migrated: 1016 entries imported from history.jsonl (SQLite)
```

### 迁移前后 API 快照逐字段 diff

部署前抓取快照（`/api/history?limit=50`、`/api/stats`、单条详情、`/api/tags`），部署后同接口复抓：

- ✅ 历史 50 条：id/content/type/nas_path/tags/title/publish_*/manual_edit/时间戳 **完全一致**
- ✅ 统计：完全一致
- ✅ 单条详情：完全一致
- （jq 对某条非标时间戳报 format 错误，但迁移前后该值原样一致——恰好证明 round-trip 忠实）

### tags 计数仲裁（发现老 bug，见 §10）

以 jsonl 原始数据为仲裁基准：

```
jsonl 原始统计          SQLite entry_tags
31 system_log     =     31 system_log
19 WeClaw         =     19 WeClaw
13 MCP            =     13 MCP
13 连通性探测      =     13 连通性探测
...                     （逐一吻合）
```

### /full 冷启动疑云排查

复测时抽样条目 `/full` 首次 1.5s，疑似回归。排查：

```
第1次 /full: 1.00s   ← 361MB trigram 索引冷页首查
第2次 /full: 0.049s
第3次 /full: 0.033s
其他条目抽查: 0.024~0.029s
```

结论：非回归，是 FTS 索引冷页首次命中。且热态比昨测的 0.12s 更快——`/full` 内部调用的 `GetByID` 也换成了 SQLite。

## 9. 性能对比（同机实测）

| 操作 | jsonl 时代 | SQLite 时代 | 提升 |
|---|---|---|---|
| 单条详情 GetByID | 94ms | **0.9ms** | 100× |
| 历史列表 50 条 | 74ms | **19ms** | 4× |
| 统计聚合 | 98ms | **32ms** | 3× |
| 打标签/编辑 | ~200ms（全文件重写） | **<2ms** | 100× |
| /full 完整内容 | 0.12s | **24–49ms** | ~3× |
| 标签列表 | 0.4ms（缓存命中时，但数据是错的） | 2.2ms（实时且正确） | — |

规模容量：jsonl 方案在数万条时列表/编辑会退化到秒级；SQLite 方案十几万条内上述数字基本不动。

## 10. 意外收获：修掉陈旧 tag_cache 老bug

验证 tags 时发现迁移前后计数对不上，仲裁后确认是**旧实现的持久化缓存陈旧**：

- `~/.knowly/tag_cache.json` 停在 **8月26日 15:42**，之后 3 天未重建
- 旧逻辑「首次构建 → 持久化 → 只增量更新」，而增量路径有漏洞（UpdateTags 只加不减、跨进程实例不共享缓存），导致 `/api/tags` 长期返回偏小的错误计数——`system_log` 显示 5 条，实际 31 条
- SQLite 版每次实时 SQL 聚合（2.2ms），**标签计数从此永远正确**。这条 bug 属于「不迁移就发现不了」的类型

## 11. 回滚方案

任一环节出问题：

```bash
git revert 53cd937 && go build -o knowly ./cmd/knowly   # 代码回滚
cp ~/.knowly/history.jsonl.premigration.bak ~/.knowly/history.jsonl
rm ~/.knowly/history.db*                                 # 数据回滚
```

jsonl 迁移前备份 + 迁移后 jsonl 本身未动，双保险。

## 12. 变更文件清单

| 文件 | 变更 |
|---|---|
| `internal/history/store.go` | 全量重写：SQLite 内核（1161 → ~700 行） |
| `internal/history/store_test.go` | +imports，新增 TestJSONLMigration |
| `cmd/knowly/main.go` | trim-history 备份改走 Store 导出；showStatus 改走 Count()；+bytes/json import |

删除的补丁机制（随内核一并清掉）：逆向读块 + `chunkPool`（sync.Pool）、`tag_cache.json` 持久化缓存、增量统计字段（incTotal/incText/…）、jsonl compact 原子重写、`ensureCount`/`maxLineSize`。
