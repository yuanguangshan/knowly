// Package history 提供剪贴板历史条目的本地持久化。
//
// 存储内核为 SQLite（modernc.org/sqlite 纯 Go 实现，无 cgo），替代早期的
// history.jsonl 追加文件：
//   - 单条读写从 O(全文件) 降为 O(索引查找)，打标签/编辑不再全量重写 16MB 文件；
//   - 列表/标签/统计走 SQL，逆向读块、sync.Pool、tag_cache.json、增量统计等
//     为 jsonl 打的补丁机制全部移除；
//   - history.jsonl 保留为只读留档：首次打开时幂等导入，之后不再写入。
//
// 并发模型：连接池固定 1 条连接（进程内天然串行），跨进程（daemon 与独立
// `knowly web`）靠 WAL + busy_timeout 协调，与 index 包同一套先例。
package history

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const defaultMaxEntries = 20000

// Entry 历史条目（JSON 标签与旧 jsonl 格式保持一致，NAS 备份文件格式不变）
type Entry struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`   // 截断后的预览，完整内容从 NAS 反查
	Type      string    `json:"type"`      // "text" 或 "image"
	Timestamp time.Time `json:"timestamp"`
	NASPath   string    `json:"nas_path"` // 可选，指向完整归档文件路径
	Tags      []string  `json:"tags"`     // AI 生成的标签
	// 同步时异步预生成的发布标题/摘要，手动发布时直接使用避免重复 AI 调用
	PublishTitle   string `json:"publish_title,omitempty"`
	PublishSummary string `json:"publish_summary,omitempty"`
	// 用户设置的标题或 AI 生成的标题
	Title string `json:"title,omitempty"`
	// 用户是否手动编辑过此条目
	ManualEdit bool `json:"manual_edit"`
}

// Stats 聚合统计
type Stats struct {
	TotalSyncs  int         `json:"total_syncs"`
	TextCount   int         `json:"text_count"`
	ImageCount  int         `json:"image_count"`
	WeeklyTrend []WeekCount `json:"weekly_trend"`
	DailyTrend  []DayCount  `json:"daily_trend"`
}

// WeekCount 周统计
type WeekCount struct {
	Label      string `json:"label"` // 如 "2026-W35"
	Count      int    `json:"count"`
	TextCount  int    `json:"text_count"`
	ImageCount int    `json:"image_count"`
}

// DayCount 天统计
type DayCount struct {
	Date  string `json:"date"` // "2006-01-02"
	Count int    `json:"count"`
}

// TagCount 标签及其出现次数
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// Store 历史存储（SQLite 内核）
type Store struct {
	db         *sql.DB
	path       string // 旧 jsonl 路径：迁移源 + 只读留档
	mu         sync.Mutex
	maxEntries int
	count      int // 内存镜像计数，仅用于 Append 的压缩触发判断
}

// NewStore 创建历史存储实例
func NewStore(dir string) *Store {
	return openStore(dir, defaultMaxEntries)
}

// NewStoreWithLimit 创建带自定义最大条目数的存储实例
func NewStoreWithLimit(dir string, maxEntries int) *Store {
	return openStore(dir, maxEntries)
}

// openStore 打开（或创建）SQLite 历史库，建表并执行一次幂等 jsonl 迁移。
func openStore(dir string, maxEntries int) *Store {
	jsonlPath := filepath.Join(dir, "history.jsonl")
	s := &Store{
		path:       jsonlPath,
		maxEntries: maxEntries,
	}

	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL",
		filepath.Join(dir, "history.db"))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Printf("[WARN] history: open history.db failed: %v (history read-only)", err)
		return s
	}
	db.SetMaxOpenConns(1) // 单连接串行化，进程内杜绝 SQLITE_BUSY
	s.db = db

	if err := s.createSchema(); err != nil {
		log.Printf("[WARN] history: create schema failed: %v (history read-only)", err)
		s.db = nil
		return s
	}

	s.migrateFromJSONL()

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&s.count); err != nil {
		s.count = 0
	}
	return s
}

// createSchema 建表。tags 双写：entries.tags JSON 列保序（用户设置的标签顺序），
// entry_tags 归一化表仅作倒排索引服务 FindByTag/AllTags。
func (s *Store) createSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS entries (
	id              TEXT PRIMARY KEY,
	content         TEXT NOT NULL DEFAULT '',
	type            TEXT NOT NULL DEFAULT 'text',
	timestamp       INTEGER NOT NULL,
	nas_path        TEXT NOT NULL DEFAULT '',
	title           TEXT NOT NULL DEFAULT '',
	publish_title   TEXT NOT NULL DEFAULT '',
	publish_summary TEXT NOT NULL DEFAULT '',
	manual_edit     INTEGER NOT NULL DEFAULT 0,
	tags            TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_entries_timestamp ON entries(timestamp);
CREATE TABLE IF NOT EXISTS entry_tags (
	entry_id TEXT NOT NULL,
	tag      TEXT NOT NULL,
	PRIMARY KEY (entry_id, tag)
);
CREATE INDEX IF NOT EXISTS idx_entry_tags_tag ON entry_tags(tag);
`
	_, err := s.db.Exec(schema)
	return err
}

// migrateFromJSONL 首次打开时把旧 history.jsonl 一次性导入 SQLite（幂等：
// 表非空即跳过）。jsonl 保留在磁盘上作为迁移前的最后留档，不再写入。
func (s *Store) migrateFromJSONL() {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&n); err != nil || n > 0 {
		return // 已有数据，跳过
	}
	f, err := os.Open(s.path)
	if err != nil {
		return // 全新安装，无历史可导
	}
	defer f.Close()

	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("[WARN] history: migration begin failed: %v", err)
		return
	}
	defer tx.Rollback()

	insEntry, err := tx.Prepare(`INSERT OR IGNORE INTO entries
		(id, content, type, timestamp, nas_path, title, publish_title, publish_summary, manual_edit, tags)
		VALUES(?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		log.Printf("[WARN] history: migration prepare failed: %v", err)
		return
	}
	defer insEntry.Close()
	insTag, err := tx.Prepare(`INSERT OR IGNORE INTO entry_tags(entry_id, tag) VALUES(?,?)`)
	if err != nil {
		log.Printf("[WARN] history: migration prepare tags failed: %v", err)
		return
	}
	defer insTag.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 100*1024*1024)
	imported := 0
	for scanner.Scan() {
		var e Entry
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue // 跳过坏行，与旧 readAll 容错一致
		}
		tagsJSON, _ := json.Marshal(e.Tags)
		ts := e.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		if _, err := insEntry.Exec(e.ID, e.Content, e.Type, ts.UnixNano(), e.NASPath,
			e.Title, e.PublishTitle, e.PublishSummary, boolToInt(e.ManualEdit), string(tagsJSON)); err != nil {
			continue
		}
		for _, t := range e.Tags {
			insTag.Exec(e.ID, t)
		}
		imported++
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[WARN] history: migration scan error: %v", err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[WARN] history: migration commit failed: %v", err)
		return
	}
	if imported > 0 {
		log.Printf("[INFO] History migrated: %d entries imported from history.jsonl (SQLite)", imported)
	}
}

// ---- 基础读写 ----

// Append 线程安全地追加一条记录，返回生成的 entry ID。
// ID 格式与旧实现一致：时间戳 + uuid 前 8 位。
func (s *Store) Append(entry Entry) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return "", fmt.Errorf("history store unavailable")
	}

	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%s_%s",
			time.Now().Format("20060102150405"),
			uuid.New().String()[:8])
	}
	entry.Timestamp = time.Now()

	tagsJSON, err := json.Marshal(entry.Tags)
	if err != nil {
		return "", fmt.Errorf("marshal tags: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO entries
		(id, content, type, timestamp, nas_path, title, publish_title, publish_summary, manual_edit, tags)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		entry.ID, entry.Content, entry.Type, entry.Timestamp.UnixNano(), entry.NASPath,
		entry.Title, entry.PublishTitle, entry.PublishSummary, boolToInt(entry.ManualEdit), string(tagsJSON)); err != nil {
		return "", err
	}
	for _, t := range entry.Tags {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO entry_tags(entry_id, tag) VALUES(?,?)`, entry.ID, t); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}

	// 跟踪条目数，超过阈值时压缩（保留最新 maxEntries 条）
	s.count++
	if s.count > s.maxEntries*2 {
		if err := s.compactLocked(); err != nil {
			log.Printf("[WARN] History compaction failed: %v", err)
		}
	}

	return entry.ID, nil
}

// compactLocked 保留最近 maxEntries 条记录（调用方需持有 s.mu）
func (s *Store) compactLocked() error {
	if s.db == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM entries WHERE rowid NOT IN
		(SELECT rowid FROM entries ORDER BY rowid DESC LIMIT ?)`, s.maxEntries)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM entry_tags WHERE entry_id NOT IN (SELECT id FROM entries)`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	removed, _ := res.RowsAffected()
	s.count = s.maxEntries
	log.Printf("[INFO] History compacted: %d entries removed, %d remaining", removed, s.count)
	return nil
}

// TrimTo 保留最近 n 条记录，删除更早的条目
func (s *Store) TrimTo(n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("history store unavailable")
	}

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&total); err != nil {
		return err
	}
	if total <= n {
		return nil // 不需要截断
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM entries WHERE rowid NOT IN
		(SELECT rowid FROM entries ORDER BY rowid DESC LIMIT ?)`, n)
	if err != nil {
		return fmt.Errorf("trim: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM entry_tags WHERE entry_id NOT IN (SELECT id FROM entries)`); err != nil {
		return fmt.Errorf("trim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("trim: %w", err)
	}

	removed, _ := res.RowsAffected()
	s.count = n
	log.Printf("[INFO] History trimmed to %d entries (removed %d old entries)", n, removed)
	return nil
}

// ReadAll 读取所有条目（旧文件顺序：旧→新）
func (s *Store) ReadAll() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queryEntries(`SELECT `+entryCols+` FROM entries ORDER BY rowid ASC`, nil)
}

const entryCols = `id, content, type, timestamp, nas_path, title, publish_title, publish_summary, manual_edit, tags`

// queryEntries 执行查询并扫描为 Entry 切片（空结果返回 nil，与旧实现一致）
func (s *Store) queryEntries(query string, args []any) ([]Entry, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

// scanEntry 从一行还原 Entry（含 entry_tags 表还原标签集合）
func scanEntry(r rowScanner) (Entry, error) {
	var e Entry
	var ts int64
	var tagsJSON string
	var manual int
	if err := r.Scan(&e.ID, &e.Content, &e.Type, &ts, &e.NASPath,
		&e.Title, &e.PublishTitle, &e.PublishSummary, &manual, &tagsJSON); err != nil {
		return e, err
	}
	e.Timestamp = time.Unix(0, ts)
	e.ManualEdit = manual != 0
	_ = json.Unmarshal([]byte(tagsJSON), &e.Tags)
	return e, nil
}

// Recent 返回最近的 n 条记录（倒序：最近的在前面）
func (s *Store) Recent(n int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queryEntries(
		`SELECT `+entryCols+` FROM entries ORDER BY rowid DESC LIMIT ?`, []any{n})
}

// RecentAfter 返回指定时间戳之前的 n 条记录（倒序），用于分页加载
func (s *Store) RecentAfter(before time.Time, n int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queryEntries(
		`SELECT `+entryCols+` FROM entries WHERE timestamp < ? ORDER BY rowid DESC LIMIT ?`,
		[]any{before.UnixNano(), n})
}

// FindByTag 返回包含指定标签的最近 limit 条记录
func (s *Store) FindByTag(tag string, limit int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queryEntries(
		`SELECT `+entryCols+` FROM entries e
		 JOIN entry_tags t ON t.entry_id = e.id
		 WHERE t.tag = ? ORDER BY e.rowid DESC LIMIT ?`,
		[]any{tag, limit})
}

// Find 根据 ID 精确（或前缀）查找；未找到返回 nil,nil（与旧实现一致）
func (s *Store) Find(id string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, nil
	}
	row := s.db.QueryRow(
		`SELECT `+entryCols+` FROM entries WHERE id = ? OR substr(id, 1, ?) = ? ORDER BY rowid LIMIT 1`,
		id, len(id), id)
	e, err := scanEntry(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// GetByID 根据 ID 获取条目（严格匹配，未找到报错）
func (s *Store) GetByID(id string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, fmt.Errorf("history store unavailable")
	}
	row := s.db.QueryRow(`SELECT `+entryCols+` FROM entries WHERE id = ?`, id)
	e, err := scanEntry(row)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entry with id %s not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// Count 返回条目总数（O(1)）
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&n)
	return n
}

// ---- 标签 ----

// AllTags 返回所有去重标签及出现次数，按次数降序排列
func (s *Store) AllTags() ([]TagCount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]TagCount, 0, 64)
	if s.db == nil {
		return result, nil
	}
	rows, err := s.db.Query(`SELECT tag, COUNT(*) AS c FROM entry_tags GROUP BY tag ORDER BY c DESC`)
	if err != nil {
		return result, nil
	}
	defer rows.Close()
	for rows.Next() {
		var tc TagCount
		if rows.Scan(&tc.Tag, &tc.Count) == nil {
			result = append(result, tc)
		}
	}
	return result, nil
}

// replaceTags 重写一条条目的标签（JSON 列 + 倒排表，调用方在事务内）
func replaceTags(tx *sql.Tx, id string, tags []string, tagsJSON string) error {
	if _, err := tx.Exec(`UPDATE entries SET tags = ? WHERE id = ?`, tagsJSON, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM entry_tags WHERE entry_id = ?`, id); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO entry_tags(entry_id, tag) VALUES(?,?)`, id, t); err != nil {
			return err
		}
	}
	return nil
}

// UpdateTags 更新指定 ID 条目的标签（合并语义：旧标签保留，新标签去重追加）
func (s *Store) UpdateTags(id string, newTags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("history store unavailable")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var oldJSON string
	err = tx.QueryRow(`SELECT tags FROM entries WHERE id = ?`, id).Scan(&oldJSON)
	if err == sql.ErrNoRows {
		return fmt.Errorf("entry with id %s not found", id)
	}
	if err != nil {
		return err
	}
	var old []string
	_ = json.Unmarshal([]byte(oldJSON), &old)

	// 合并去重：旧标签顺序在前，新标签去重后追加（旧实现为 map 随机序，此处更稳定）
	seen := make(map[string]bool, len(old)+len(newTags))
	merged := make([]string, 0, len(old)+len(newTags))
	for _, t := range append(append([]string{}, old...), newTags...) {
		if !seen[t] {
			seen[t] = true
			merged = append(merged, t)
		}
	}
	tagsJSON, _ := json.Marshal(merged)
	if err := replaceTags(tx, id, merged, string(tagsJSON)); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdatePublishMeta 更新指定条目的预生成发布标题和摘要。
// 用户已手动编辑的条目跳过 AI 覆盖（静默成功，与旧实现一致）。
func (s *Store) UpdatePublishMeta(id, title, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("history store unavailable")
	}

	res, err := s.db.Exec(
		`UPDATE entries SET publish_title = ?, publish_summary = ? WHERE id = ? AND manual_edit = 0`,
		title, summary, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 不存在，或已被手动编辑（后者旧实现同样静默返回 nil）
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("entry with id %s not found", id)
		}
	}
	return nil
}

// UpdateEntry 更新指定条目的标题、标签、摘要。
// title/summary 为空则不修改；newTags 非 nil 时整体替换（保持用户顺序）；
// ManualEdit = !clearManualEdit。clearManualEdit=true 用于 AI 重新处理后。
func (s *Store) UpdateEntry(id, title string, newTags []string, summary string, clearManualEdit bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("history store unavailable")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var cur Entry
	var ts int64
	var tagsJSON string
	var manual int
	err = tx.QueryRow(`SELECT `+entryCols+` FROM entries WHERE id = ?`, id).
		Scan(&cur.ID, &cur.Content, &cur.Type, &ts, &cur.NASPath,
			&cur.Title, &cur.PublishTitle, &cur.PublishSummary, &manual, &tagsJSON)
	if err == sql.ErrNoRows {
		return fmt.Errorf("entry with id %s not found", id)
	}
	if err != nil {
		return err
	}

	if title != "" {
		cur.Title = title
	}
	if summary != "" {
		cur.PublishSummary = summary
	}
	if newTags != nil {
		cur.Tags = newTags // 替换标签（不去重，保持用户设置的顺序）
		tagsJSONb, _ := json.Marshal(cur.Tags)
		if err := replaceTags(tx, id, cur.Tags, string(tagsJSONb)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(
		`UPDATE entries SET title = ?, publish_summary = ?, manual_edit = ? WHERE id = ?`,
		cur.Title, cur.PublishSummary, boolToInt(!clearManualEdit), id); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- 统计 ----

// Stats 聚合统计历史数据。
// 时间分桶在 Go 侧按本地时区完成（SQLite 的 date() 只有 UTC，会错位一天），
// 聚合逻辑与旧实现逐字节一致：最近 30 天日趋势 + 最近 8 个 ISO 周趋势。
func (s *Store) Stats() (*Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := &Stats{
		WeeklyTrend: make([]WeekCount, 0, 8),
		DailyTrend:  make([]DayCount, 0, 30),
	}
	if s.db == nil {
		return stats, nil
	}

	rows, err := s.db.Query(`SELECT timestamp, type FROM entries`)
	if err != nil {
		return stats, nil
	}
	defer rows.Close()

	dayText := make(map[string]int)
	dayImage := make(map[string]int)
	for rows.Next() {
		var ts int64
		var typ string
		if rows.Scan(&ts, &typ) != nil {
			continue
		}
		day := time.Unix(0, ts).Format("2006-01-02")
		if typ == "text" {
			stats.TextCount++
			dayText[day]++
		} else {
			stats.ImageCount++
			dayImage[day]++
		}
	}
	stats.TotalSyncs = stats.TextCount + stats.ImageCount

	// 最近 30 天趋势
	now := time.Now()
	for i := 29; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		stats.DailyTrend = append(stats.DailyTrend, DayCount{
			Date:  d,
			Count: dayText[d] + dayImage[d],
		})
	}

	// 按周聚合（ISO 周）
	type weekKey struct{ year, week int }
	weekMap := make(map[weekKey]struct{ text, image int })
	for d, tc := range dayText {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		y, w := t.ISOWeek()
		s := weekMap[weekKey{y, w}]
		s.text += tc
		weekMap[weekKey{y, w}] = s
	}
	for d, ic := range dayImage {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		y, w := t.ISOWeek()
		s := weekMap[weekKey{y, w}]
		s.image += ic
		weekMap[weekKey{y, w}] = s
	}
	for i := 7; i >= 0; i-- {
		t := now.AddDate(0, 0, -7*i)
		y, w := t.ISOWeek()
		s := weekMap[weekKey{y, w}]
		stats.WeeklyTrend = append(stats.WeeklyTrend, WeekCount{
			Label:      fmt.Sprintf("%d-W%02d", y, w),
			Count:      s.text + s.image,
			TextCount:  s.text,
			ImageCount: s.image,
		})
	}

	return stats, nil
}

// Close 关闭底层 SQLite 连接
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
