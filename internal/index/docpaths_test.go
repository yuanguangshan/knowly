package index

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Count / AllTags 必须走 doc_paths 普通表，且在 BulkIndex 路径下与 docs 保持一致。
func TestCountAndTagsUseDocPaths(t *testing.T) {
	ix := openTest(t)
	now := time.Now()

	docs := []Doc{
		{Path: "2026/07/01/100000_a.md", NasPath: "/d/a.md", Title: "A", Tags: "科学, 生物",
			Type: "text", Time: now, Content: "内容甲"},
		{Path: "2026/07/02/110000_b.md", NasPath: "/d/b.md", Title: "B", Tags: "科学, Go",
			Type: "text", Time: now, Content: "内容乙"},
		{Path: "2026/07/03/120000_c.md", NasPath: "/d/c.md", Title: "C", Tags: "",
			Type: "text", Time: now, Content: "内容丙"},
	}
	if err := ix.BulkIndex(docs); err != nil {
		t.Fatalf("bulk index: %v", err)
	}

	// 计数应与 docs 一致，且走 doc_paths。
	if n, err := ix.Count(); err != nil || n != 3 {
		t.Fatalf("Count = %d, err = %v; want 3", n, err)
	}

	tags, err := ix.AllTags()
	if err != nil {
		t.Fatalf("AllTags: %v", err)
	}
	got := map[string]int{}
	for _, tc := range tags {
		got[tc.Tag] = tc.Count
	}
	if got["科学"] != 2 || got["生物"] != 1 || got["Go"] != 1 {
		t.Fatalf("tags = %v; want 科学=2 生物=1 Go=1", got)
	}

	// 单条 Index 路径同样要写入 tags。
	if err := ix.Index("2026/07/04/130000_d.md", "/d/d.md", "D", "科学", "text", "内容丁", now); err != nil {
		t.Fatalf("index: %v", err)
	}
	if n, _ := ix.Count(); n != 4 {
		t.Fatalf("after single index Count = %d; want 4", n)
	}
	tags, _ = ix.AllTags()
	for _, tc := range tags {
		if tc.Tag == "科学" && tc.Count != 3 {
			t.Fatalf("科学 = %d after single index; want 3", tc.Count)
		}
	}
}

// 老库（doc_paths 只有 path 列）升级时应自动补 tags 列并从 docs 回填。
func TestLegacyDocPathsUpgradeBackfillsTags(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")

	// 1. 用旧 schema 手工建库：doc_paths(path)、docs 有 path/tags、无 tags 列。
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	stmts := []string{
		`CREATE VIRTUAL TABLE docs USING fts5(path UNINDEXED, nas_path UNINDEXED, title, tags, type UNINDEXED, time UNINDEXED, content, tokenize='trigram')`,
		`CREATE TABLE doc_paths(path TEXT PRIMARY KEY) WITHOUT ROWID`,
		`INSERT INTO docs(path, nas_path, title, tags, type, time, content) VALUES('2026/06/01/100000_x.md','/d/x.md','X','历史, 归档','text','2026-06-01T10:00:00Z','正文')`,
		`INSERT INTO doc_paths(path) VALUES('2026/06/01/100000_x.md')`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			raw.Close()
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	raw.Close()

	// 2. Open 应触发升级 + 回填。
	ix, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open legacy: %v", err)
	}
	defer ix.Close()

	if has, err := columnExists(ix.db, "doc_paths", "tags"); err != nil || !has {
		t.Fatalf("tags column not added (has=%v err=%v)", has, err)
	}
	tags, err := ix.AllTags()
	if err != nil {
		t.Fatalf("AllTags: %v", err)
	}
	got := map[string]int{}
	for _, tc := range tags {
		got[tc.Tag] = tc.Count
	}
	if got["历史"] != 1 || got["归档"] != 1 {
		t.Fatalf("migrated tags = %v; want 历史=1 归档=1", got)
	}
	if n, _ := ix.Count(); n != 1 {
		t.Fatalf("Count after upgrade = %d; want 1", n)
	}

	// 3. 再次 Open 不应重复全表扫（幂等、无副作用）。
	ix.Close()
	ix2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ix2.Close()
	if tags, _ := ix2.AllTags(); len(tags) != 2 {
		t.Fatalf("after reopen tags = %v; want 2 tags", tags)
	}
}

// 幂等重写必须靠 rowid 精确删除旧 FTS5 行：既不能留重复行，也不能留旧内容。
// 这直接守护「FTS5 WHERE path=? 全表扫 2.15s」这个 bug 不被改回去。
func TestReindexDeletesOldRowByRowid(t *testing.T) {
	ix := openTest(t)
	now := time.Now()
	p := "2026/07/09/100000_dup.md"

	if err := ix.Index(p, "/d/dup.md", "旧标题", "旧标签", "text", "苹果香蕉", now); err != nil {
		t.Fatalf("index v1: %v", err)
	}
	if n, _ := ix.Count(); n != 1 {
		t.Fatalf("Count = %d; want 1", n)
	}
	// 同 path 重写：必须替换而非追加。
	if err := ix.Index(p, "/d/dup.md", "新标题", "新标签", "text", "橘子柚子", now); err != nil {
		t.Fatalf("index v2: %v", err)
	}
	if n, _ := ix.Count(); n != 1 {
		t.Fatalf("Count after reindex = %d; want 1 (重复行 = rowid 删除失效)", n)
	}
	// 旧内容必须搜不到，新内容必须搜得到（证明旧 FTS5 行被真删了）。
	if hits, err := ix.Search("苹果", 10); err != nil || len(hits) != 0 {
		t.Fatalf("旧内容仍可搜到: hits=%d err=%v", len(hits), err)
	}
	if hits, err := ix.Search("橘子", 10); err != nil || len(hits) != 1 {
		t.Fatalf("新内容搜不到: hits=%d err=%v", len(hits), err)
	}
	// GetByPath 走 rowid 命中，内容应为最新。
	d, err := ix.GetByPath(p)
	if err != nil || d == nil {
		t.Fatalf("GetByPath: d=%v err=%v", d, err)
	}
	if d.Title != "新标题" || d.Content != "橘子柚子" {
		t.Fatalf("GetByPath 返回旧数据: title=%q content=%q", d.Title, d.Content)
	}
	// BulkIndex 路径同样要按 rowid 覆盖。
	if err := ix.BulkIndex([]Doc{{Path: p, NasPath: "/d/dup.md", Title: "批量版", Tags: "新标签",
		Type: "text", Time: now, Content: "西瓜哈密瓜"}}); err != nil {
		t.Fatalf("bulk reindex: %v", err)
	}
	if n, _ := ix.Count(); n != 1 {
		t.Fatalf("Count after bulk reindex = %d; want 1", n)
	}
	if hits, _ := ix.Search("橘子", 10); len(hits) != 0 {
		t.Fatalf("BulkIndex 未删除旧行: hits=%d", len(hits))
	}
}

// 老库升级时必须一并回填 fts_rowid，否则后续重写会退化成全表扫或产生重复行。
func TestLegacyUpgradeBackfillsFtsRowid(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy_rowid.db")

	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	stmts := []string{
		`CREATE VIRTUAL TABLE docs USING fts5(path UNINDEXED, nas_path UNINDEXED, title, tags, type UNINDEXED, time UNINDEXED, content, tokenize='trigram')`,
		`CREATE TABLE doc_paths(path TEXT PRIMARY KEY) WITHOUT ROWID`,
		`INSERT INTO docs(path, nas_path, title, tags, type, time, content) VALUES('2026/06/01/100000_x.md','/d/x.md','X','历史','text','2026-06-01T10:00:00Z','原始正文')`,
		`INSERT INTO doc_paths(path) VALUES('2026/06/01/100000_x.md')`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			raw.Close()
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	raw.Close()

	ix, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open legacy: %v", err)
	}
	defer ix.Close()

	if has, err := columnExists(ix.db, "doc_paths", "fts_rowid"); err != nil || !has {
		t.Fatalf("fts_rowid column not added (has=%v err=%v)", has, err)
	}
	var rid int64
	if err := ix.db.QueryRow(`SELECT fts_rowid FROM doc_paths WHERE path = ?`,
		"2026/06/01/100000_x.md").Scan(&rid); err != nil {
		t.Fatalf("read fts_rowid: %v", err)
	}
	if rid <= 0 {
		t.Fatalf("fts_rowid 未回填: rid=%d（重写将退化为全表扫）", rid)
	}
	// 升级后重写同 path：必须替换而非追加。
	if err := ix.Index("2026/06/01/100000_x.md", "/d/x.md", "X2", "历史", "text", "改写正文", time.Now()); err != nil {
		t.Fatalf("reindex after upgrade: %v", err)
	}
	if n, _ := ix.Count(); n != 1 {
		t.Fatalf("Count after reindex = %d; want 1", n)
	}
	if hits, _ := ix.Search("原始正文", 10); len(hits) != 0 {
		t.Fatalf("旧行未被删除: hits=%d", len(hits))
	}
}
