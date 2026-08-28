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
	"sort"
	"strings"
	"sync"
	"unicode"
	"time"

	_ "modernc.org/sqlite"
)

// Indexer 是索引能力的最小接口，便于在 ssh / web 包中注入，也便于测试 mock。
type Indexer interface {
	Index(path, nasPath, title, tags, typ, content string, t time.Time) error
	BulkIndex(docs []Doc) error
	Search(q string, limit int) ([]Hit, error)
	GetByPath(path string) (*Doc, error)
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
	return &Index{db: db, path: path}, nil
}

// Close 关闭数据库。
func (ix *Index) Close() error { return ix.db.Close() }

// Index 增量写入一条。同 path 先删后插，保证幂等（重复同步不会重复索引）。
// 写入经 mu 串行化，与 BulkIndex / 增量同步的写互斥，从根本上消除 SQLITE_BUSY。
func (ix *Index) Index(path, nasPath, title, tags, typ, content string, t time.Time) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM docs WHERE path = ?`, path); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO docs(path, nas_path, title, tags, type, time, content) VALUES(?,?,?,?,?,?,?)`,
		path, nasPath, title, tags, typ, t.Format(time.RFC3339), content); err != nil {
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
	del, err := tx.Prepare(`DELETE FROM docs WHERE path = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer del.Close()
	ins, err := tx.Prepare(`INSERT INTO docs(path, nas_path, title, tags, type, time, content) VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer ins.Close()
	for _, d := range docs {
		if _, err := del.Exec(d.Path); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := ins.Exec(d.Path, d.NasPath, d.Title, d.Tags, d.Type, d.Time.Format(time.RFC3339), d.Content); err != nil {
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
func (ix *Index) GetByPath(path string) (*Doc, error) {
	var d Doc
	var t string
	err := ix.db.QueryRow(
		`SELECT path, nas_path, title, tags, type, time, content FROM docs WHERE path = ? LIMIT 1`, path,
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
func (ix *Index) AllTags() ([]TagCount, error) {
	rows, err := ix.db.Query(`SELECT tags FROM docs WHERE tags != ''`)
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
func (ix *Index) Count() (int, error) {
	var n int
	err := ix.db.QueryRow(`SELECT count(*) FROM docs`).Scan(&n)
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
