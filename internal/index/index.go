// Package index 提供基于 SQLite FTS5（trigram 分词）的本地全文索引。
// 设计目标：在 Mac 本机为群晖归档建立毫秒级全文检索，对外暴露干净的查询接口，
// 同时群晖仍是唯一真源（索引可随时从 NAS 回溯重建）。
//
// 为什么用 trigram：对中文/英文子串匹配均友好，且 modernc.org/sqlite 为纯 Go 实现，
// 无 cgo、零系统依赖，契合「私有化、自主可控」的哲学。
package index

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

// Indexer 是索引能力的最小接口，便于在 ssh / web 包中注入，也便于测试 mock。
type Indexer interface {
	Index(path, nasPath, title, tags, typ, content string, t time.Time) error
	BulkIndex(docs []Doc) error
	Checkpoint()
	Search(q string, limit int) ([]Hit, error)
	GetByPath(path string) (*Doc, error)
	PathsInDir(relDir string) (map[string]bool, error)
	AllTags() ([]TagCount, error)
	Count() (int, error)
	Close() error
}

// Doc 一条已被索引的归档条目（含完整正文，供详情接口直接返回，无需再走 SSH）。
type Doc struct {
	Path    string    `json:"path"`
	NasPath string    `json:"nas_path"`
	Title   string    `json:"title"`
	Tags    string    `json:"tags"`
	Type    string    `json:"type"`
	Time    time.Time `json:"time"`
	Content string    `json:"content"`
}

// Hit 搜索命中。
type Hit struct {
	Path    string `json:"path"`
	NasPath string `json:"nas_path"`
	Title   string `json:"title"`
	Tags    string `json:"tags"`
	Type    string `json:"type"`
	Time    string `json:"time"`
	Snippet string `json:"snippet"`
	Rank    int    `json:"rank"`
}

// TagCount 标签计数。
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// Index 基于 SQLite FTS5 trigram 的本地全文索引。
type Index struct {
	db   *sql.DB
	path string
	mu   sync.Mutex // 串行化所有写入，杜绝并发写竞争导致 SQLITE_BUSY 丢数据
}

// Open 打开（或创建）索引数据库，建表并启用 FTS5 trigram 分词。
//
// 通过 DSN 在【连接池的每个连接】上启用 WAL + busy_timeout + synchronous=NORMAL：
//   - journal_mode=WAL：读写可并发，写不再阻塞读；
//   - busy_timeout=5000：写冲突时等待而非立即返回 SQLITE_BUSY；
//   - synchronous=NORMAL：WAL 下提交更快且安全。
//
// 这些 PRAGMA 由 modernc 驱动在每次建立连接时自动执行，因此连接池里
// 任意连接都具备一致的行为，无需在每条语句前后手动 SET。
func Open(path string) (*Index, error) {
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open index db: %w", err)
	}
	// 允许多个读连接并发（WAL 下读不阻塞写），写入由 mu 串行化。
	db.SetMaxOpenConns(8)
	// trigram 分词：将文本切成 3 字符重叠片段，对子串/中文匹配友好。
	// path/nas_path/type/time 为 UNINDEXED（仅存储，不参与全文检索）。
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS docs USING fts5(
		path UNINDEXED,
		nas_path UNINDEXED,
		title,
		tags,
		type UNINDEXED,
		time UNINDEXED,
		content,
		tokenize='trigram'
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create fts5 table: %w", err)
	}
	// doc_paths 是普通表：FTS5 虚拟表不支持对 UNINDEXED 列做 WHERE 过滤
	// （path LIKE 永远空），但 backfill 判重必须能按路径精确/前缀查找。
	// 把它单独存普通表，写入时与 docs 同步维护。
	//
	// 除 path 外还冗余两份元数据：
	//   - tags：status/tags 这类聚合接口若直接 COUNT/SELECT FTS5 的 docs，要遍历
	//     整个倒排索引与内容页（实测 count(*) 2.7s、写入竞争时到 7.6s），
	//     查这张紧凑普通表只要 0.03s。
	//   - fts_rowid：docs 里对应行的 rowid。删除旧行时必须用它——FTS5 对
	//     UNINDEXED 的 path 列做 WHERE 只能全表扫（实测 22.5k 行时 2.15s/次，
	//     是 backfill 单批 8 分钟的真凶）；按 rowid 删是索引查找，微秒级。
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS doc_paths(
		path TEXT PRIMARY KEY,
		tags TEXT NOT NULL DEFAULT '',
		fts_rowid INTEGER NOT NULL DEFAULT 0
	) WITHOUT ROWID`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create doc_paths table: %w", err)
	}
	// 轻量 schema 升级：老库的 doc_paths 缺列时逐列补上（新建库建表即带全列）。
	// 补过的列需要从 docs 回填一次，未补列的库不重复扫（避免每次启动都扫 1.5GB）。
	upgraded := false
	for _, col := range []struct{ name, ddl string }{
		{"tags", `ALTER TABLE doc_paths ADD COLUMN tags TEXT NOT NULL DEFAULT ''`},
		{"fts_rowid", `ALTER TABLE doc_paths ADD COLUMN fts_rowid INTEGER NOT NULL DEFAULT 0`},
	} {
		has, cerr := columnExists(db, "doc_paths", col.name)
		if cerr != nil || has {
			continue
		}
		if _, err := db.Exec(col.ddl); err != nil {
			db.Close()
			return nil, fmt.Errorf("upgrade doc_paths.%s: %w", col.name, err)
		}
		upgraded = true
	}
	if needDocPathSync(db, upgraded) {
		syncDocPathMeta(db)
	}
	return &Index{db: db, path: path}, nil
}

// needDocPathSync 判断是否需要从 docs 全表扫一次重建 doc_paths 元数据。
//
// 触发条件（任一）：
//   - 本次刚补齐了缺失的列（upgraded）；
//   - doc_paths 为空但 docs 非空（极老的库，doc_paths 表尚未存在过）；
//   - doc_paths 里存在 fts_rowid=0 的行（上次进程在写入中途被杀，映射残缺）。
//
// 全新库（doc_paths 与 docs 都空）不触发，避免无谓扫描。
func needDocPathSync(db *sql.DB, upgraded bool) bool {
	if upgraded {
		return true
	}
	var pathCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doc_paths`).Scan(&pathCount); err != nil {
		return false
	}
	if pathCount == 0 {
		// EXISTS+LIMIT 1：只在 docs 非空时才付出一次扫描代价。
		var hasDoc int
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM docs LIMIT 1)`).Scan(&hasDoc); err != nil {
			return false
		}
		return hasDoc == 1
	}
	var zeros int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doc_paths WHERE fts_rowid = 0`).Scan(&zeros); err != nil {
		return false
	}
	return zeros > 0
}

// Close 关闭数据库。
func (ix *Index) Close() error { return ix.db.Close() }

// columnExists 判断普通表 table 是否已有 col 列，用于轻量级 schema 升级判断。
// 借助 SQLite 的 pragma_table_info 表值函数（3.16+，modernc 支持）。
func columnExists(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// syncDocPathMeta 从 docs 一次性重建 doc_paths 的元数据（path → tags / fts_rowid）。
//
// 全表扫一次（22.5k 行实测 2–8s）即可拿到所有 rowid，绝不能逐条
// `WHERE path = ?` 去查——那是 22.5k × 2s ≈ 12 小时。
// 每 500 条提交，避免长事务。
func syncDocPathMeta(db *sql.DB) {
	rows, err := db.Query(`SELECT rowid, path, tags FROM docs`)
	if err != nil {
		log.Printf("[WARN] doc_paths meta sync skipped: %v", err)
		return
	}
	type rec struct {
		id   int64
		path string
		tags string
	}
	buf := make([]rec, 0, 500)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		tx, terr := db.Begin()
		if terr != nil {
			buf = buf[:0]
			return
		}
		stmt, perr := tx.Prepare(`INSERT INTO doc_paths(path, tags, fts_rowid) VALUES(?,?,?)
			ON CONFLICT(path) DO UPDATE SET tags = excluded.tags, fts_rowid = excluded.fts_rowid`)
		if perr != nil {
			_ = tx.Rollback()
			buf = buf[:0]
			return
		}
		for _, e := range buf {
			_, _ = stmt.Exec(e.path, e.tags, e.id)
		}
		_ = stmt.Close()
		_ = tx.Commit()
		buf = buf[:0]
	}

	n := 0
	for rows.Next() {
		var e rec
		if err := rows.Scan(&e.id, &e.path, &e.tags); err != nil {
			continue
		}
		buf = append(buf, e)
		n++
		if len(buf) >= 500 {
			flush()
		}
	}
	_ = rows.Close()
	flush()
	log.Printf("[INFO] doc_paths meta synced: %d rows", n)
}

// deleteByPathTx 删除 path 在 FTS5 docs 中的旧行（幂等写入的前置步骤）。
//
// 关键：先查 doc_paths 取回 rowid，再 `DELETE ... WHERE rowid = ?`。
// FTS5 虚拟表对 UNINDEXED 的 path 列做 WHERE 无法走索引，只能全表扫描——
// 实测 22.5k 行时 2.15s/次，backfill 每批 200 条就要烧掉 7 分钟，且随数据量
// 线性恶化。按 rowid 删除是 B-tree 索引查找，微秒级。
//
// 返回是否真的删除了行（false 表示该 path 尚未索引，无需删除）。
func deleteByPathTx(tx *sql.Tx, path string) (bool, error) {
	var old int64
	err := tx.QueryRow(`SELECT fts_rowid FROM doc_paths WHERE path = ?`, path).Scan(&old)
	if err == sql.ErrNoRows {
		return false, nil // 新文档：FTS5 里没有旧行
	}
	if err != nil {
		return false, err
	}
	if old <= 0 {
		return false, nil // 映射缺失：交由启动时的 syncDocPathMeta 修复
	}
	if _, err := tx.Exec(`DELETE FROM docs WHERE rowid = ?`, old); err != nil {
		return false, err
	}
	return true, nil
}

// Checkpoint 把 WAL 合并回主库并截断 WAL（TRUNCATE 模式）。
//
// 为什么必须 TRUNCATE 而不是 PASSIVE：PASSIVE 在检测到任何活动读事务时
// 返回 busy 不截断——backfill 批量写入期间 WAL 会一路滚到数百 MB，FTS5 在
// 大 WAL 上每次 INSERT 都要访问碎片化索引页，写入从毫秒级退化到 150ms/条
// （线上实测 71MB WAL 时 200 条/批耗时 30s）。TRUNCATE 会等到无活动事务
// 再把 WAL 清零（实验中另起连接 8s 内成功截断 71MB → 0MB）。
//
// 调用时机：backfill 每批 BulkIndex Commit 之后（无活动事务），此时
// TRUNCATE 立即成功，WAL 恒定在 MB 级。
func (ix *Index) Checkpoint() {
	// 单独新连接执行 TRUNCATE，避免在连接池写连接上等待造成死锁。
	checkDb, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_busy_timeout=8000", ix.path))
	if err != nil {
		return
	}
	defer checkDb.Close()
	row := checkDb.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`)
	var busy, log, checkpointed int
	if err := row.Scan(&busy, &log, &checkpointed); err != nil {
		return // 非致命：WAL 继续增长，下次 checkpoint 或进程退出时自然合并
	}
}

// Index 增量写入一条。同 path 先删后插，保证幂等（重复同步不会重复索引）。
// 写入经 mu 串行化，与 BulkIndex / 增量同步的写互斥，从根本上消除 SQLITE_BUSY。
func (ix *Index) Index(path, nasPath, title, tags, typ, content string, t time.Time) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	// 先删旧行：经 doc_paths 取 rowid 后按 rowid 删（见 deleteByPathTx 注释）。
	if _, err := deleteByPathTx(tx, path); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO docs(path, nas_path, title, tags, type, time, content) VALUES(?,?,?,?,?,?,?)`,
		path, nasPath, title, tags, typ, t.Format(time.RFC3339), content); err != nil {
		tx.Rollback()
		return err
	}
	// 同步维护普通表 doc_paths（FTS5 的 path 列不可 WHERE 查询）：
	// tags 供 status/tags 聚合，fts_rowid 供下次幂等写入精确删除旧行。
	var newID int64
	if err := tx.QueryRow(`SELECT last_insert_rowid()`).Scan(&newID); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO doc_paths(path, tags, fts_rowid) VALUES(?,?,?)`,
		path, tags, newID); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// BulkIndex 在一个事务内批量写入多条，供 backfill 使用：把上千次单条事务
// 压缩为少量批量事务，大幅降低锁竞争与提交开销。幂等（同 path 先删后插）。
// 写入同样经 mu 串行化，与 Index 的写互斥。
func (ix *Index) BulkIndex(docs []Doc) error {
	if len(docs) == 0 {
		return nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	del, err := tx.Prepare(`SELECT fts_rowid FROM doc_paths WHERE path = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer del.Close()
	ddel, err := tx.Prepare(`DELETE FROM docs WHERE rowid = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer ddel.Close()
	ins, err := tx.Prepare(`INSERT INTO docs(path, nas_path, title, tags, type, time, content) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer ins.Close()
	lastID, err := tx.Prepare(`SELECT last_insert_rowid()`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer lastID.Close()
	pathIns, err := tx.Prepare(`INSERT OR REPLACE INTO doc_paths(path, tags, fts_rowid) VALUES(?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer pathIns.Close()
	for _, d := range docs {
		// 幂等前置：若该 path 已索引，按 doc_paths 里记的 rowid 精确删除旧行。
		// 直接 DELETE ... WHERE path = ? 会触发 FTS5 全表扫描（2.15s/条）。
		var old int64
		switch err := del.QueryRow(d.Path).Scan(&old); err {
		case nil:
			if old > 0 {
				if _, err := ddel.Exec(old); err != nil {
					tx.Rollback()
					return err
				}
			}
		case sql.ErrNoRows: // 新文档，无旧行
		default:
			tx.Rollback()
			return err
		}
		if _, err := ins.Exec(d.Path, d.NasPath, d.Title, d.Tags, d.Type, d.Time.Format(time.RFC3339), d.Content); err != nil {
			tx.Rollback()
			return err
		}
		var newID int64
		if err := lastID.QueryRow().Scan(&newID); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := pathIns.Exec(d.Path, d.Tags, newID); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Search 全文搜索。query 长度 >= 3 个字符走 FTS5 trigram；否则（如 1-2 字中文）
// 走 LIKE 兜底，保证短词也能命中。
func (ix *Index) Search(q string, limit int) ([]Hit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []Hit{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if len([]rune(q)) >= 3 {
		return ix.searchFTS(q, limit)
	}
	return ix.searchLike(q, limit)
}

// searchFTS 使用 FTS5 trigram 匹配，按相关度 rank 排序，并生成高亮摘要。
func (ix *Index) searchFTS(q string, limit int) ([]Hit, error) {
	escaped := strings.ReplaceAll(q, "\"", "\"\"")
	match := "\"" + escaped + "\""
	// content 是表第 7 列（0 基），snippet 从 content 中提取上下文。
	rows, err := ix.db.Query(`
		SELECT path, nas_path, title, tags, type, time,
		       snippet(docs, 6, '<mark>', '</mark>', '…', 24)
		FROM docs
		WHERE docs MATCH ?
		ORDER BY rank
		LIMIT ?`, match, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()

	var hits []Hit
	rank := 0
	for rows.Next() {
		var h Hit
		var t string
		if err := rows.Scan(&h.Path, &h.NasPath, &h.Title, &h.Tags, &h.Type, &t, &h.Snippet); err != nil {
			return nil, err
		}
		h.Time = t
		h.Rank = rank
		rank++
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// searchLike 短词兜底：在 content/title/tags 上做 LIKE 扫描，手动生成摘要。
func (ix *Index) searchLike(q string, limit int) ([]Hit, error) {
	like := "%" + q + "%"
	rows, err := ix.db.Query(`
		SELECT path, nas_path, title, tags, type, time, content
		FROM docs
		WHERE content LIKE ? OR title LIKE ? OR tags LIKE ?
		LIMIT ?`, like, like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("like search: %w", err)
	}
	defer rows.Close()

	var hits []Hit
	rank := 0
	for rows.Next() {
		var h Hit
		var t, content string
		if err := rows.Scan(&h.Path, &h.NasPath, &h.Title, &h.Tags, &h.Type, &t, &content); err != nil {
			return nil, err
		}
		h.Time = t
		h.Snippet = makeSnippet(content, q, 60)
		h.Rank = rank
		rank++
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// GetByPath 按路径取回完整条目（含正文）。未命中返回 (nil, nil)。
//
// 不由 FTS5 直接 WHERE path 取——那是全表扫（22.5k 行实测 2.15s）。改为经
// doc_paths 取回 rowid 后按 rowid 命中（索引查找），两次 O(log n)。
func (ix *Index) GetByPath(path string) (*Doc, error) {
	var rid int64
	err := ix.db.QueryRow(`SELECT fts_rowid FROM doc_paths WHERE path = ?`, path).Scan(&rid)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d Doc
	var t string
	err = ix.db.QueryRow(
		`SELECT path, nas_path, title, tags, type, time, content FROM docs WHERE rowid = ?`, rid,
	).Scan(&d.Path, &d.NasPath, &d.Title, &d.Tags, &d.Type, &t, &d.Content)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.Time, _ = time.Parse(time.RFC3339, t)
	return &d, nil
}

// PathsInDir 返回指定相对目录前缀下所有已索引 path（如 "2026/05/02"）。
//
// 查询走普通表 doc_paths（而非 FTS5 docs——FTS5 虚拟表不支持对 UNINDEXED
// path 列做 LIKE/WHERE，返回恒为空）。backfill 用它一次拉回整个目录的
// 已索引 path 成 set，避免对每文件各调一次全表扫描。
func (ix *Index) PathsInDir(relDir string) (map[string]bool, error) {
	prefix := strings.Trim(relDir, "/")
	out := make(map[string]bool)
	rows, err := ix.db.Query(`SELECT path FROM doc_paths WHERE path LIKE ?`, prefix+"/%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}

// GetByNasPath 按 NAS 绝对路径取全文。供 /api/history/{id}/full 等
// 历史详情接口优先命中本地索引，省去一次 2~3 秒的 SSH 往返。
// nas_path 是 UNINDEXED 列，走线性扫描；数千条规模下毫秒级，可接受。
func (ix *Index) GetByNasPath(nasPath string) (*Doc, error) {
	var d Doc
	var t string
	err := ix.db.QueryRow(
		`SELECT path, nas_path, title, tags, type, time, content FROM docs WHERE nas_path = ? LIMIT 1`, nasPath,
	).Scan(&d.Path, &d.NasPath, &d.Title, &d.Tags, &d.Type, &t, &d.Content)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.Time, _ = time.Parse(time.RFC3339, t)
	return &d, nil
}

// AllTags 聚合所有标签及出现次数，按次数降序。
// 走 doc_paths 普通表（紧凑 B 树，实测 ~0.03s）而非 FTS5 docs 全表扫
// （1.5GB 索引上实测 ~1.9s，写入竞争时更慢）。
func (ix *Index) AllTags() ([]TagCount, error) {
	rows, err := ix.db.Query(`SELECT tags FROM doc_paths WHERE tags != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var tags string
		if err := rows.Scan(&tags); err != nil {
			return nil, err
		}
		for _, tk := range strings.Split(tags, ", ") {
			tk = strings.TrimSpace(tk)
			if tk != "" {
				counts[tk]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	res := make([]TagCount, 0, len(counts))
	for t, c := range counts {
		res = append(res, TagCount{Tag: t, Count: c})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Count > res[j].Count })
	return res, nil
}

// Count 返回索引条目总数。
// 走 doc_paths 普通表（实测 ~0.03s）而非 FTS5 docs 的 count(*)（~2.7s，
// backfill 批量写入竞争时劣化到 7s+，会让 /api/v1/status 与 Web UI 卡顿）。
// doc_paths 与 docs 在同一事务内同步维护，两者条数恒等。
func (ix *Index) Count() (int, error) {
	var n int
	err := ix.db.QueryRow(`SELECT count(*) FROM doc_paths`).Scan(&n)
	return n, err
}

// makeSnippet 围绕 query 在 content 中截取一段上下文，前后加省略号。
// indexRune 在 rs 中查找 sub 首次出现的下标（均为 rune 空间），未命中返回 -1。
func indexRune(rs, sub []rune) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(rs); i++ {
		ok := true
		for j := range sub {
			if rs[i+j] != sub[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func makeSnippet(content, q string, around int) string {
	if around <= 0 {
		around = 60
	}
	c := []rune(content)
	// 匹配必须在 rune 空间：strings.Index 返回字节偏移，中文内容字节偏移 ≈3× rune
	// 偏移，匹配点靠后时 start>end 直接 panic（2026-08-29 热榜类短词全崩，
	// slice bounds out of range [16990:9250]，panic 栈在 makeSnippet）。
	// 逐 rune ToLower 保证大小写映射不改变 rune 数，下标严格对齐。
	lq := make([]rune, 0, len(q))
	for _, r := range q {
		lq = append(lq, unicode.ToLower(r))
	}
	lc := make([]rune, len(c))
	for i, r := range c {
		lc[i] = unicode.ToLower(r)
	}
	idx := indexRune(lc, lq)
	if idx < 0 {
		if len(c) > around*2 {
			return string(c[:around*2]) + "…"
		}
		return string(c)
	}
	start := idx - around
	if start < 0 {
		start = 0
	}
	end := idx + len(lq) + around
	if end > len(c) {
		end = len(c)
	}
	if start > end {
		start = end
	}
	snip := string(c[start:end])
	if start > 0 {
		snip = "…" + snip
	}
	if end < len(c) {
		snip = snip + "…"
	}
	return snip
}
