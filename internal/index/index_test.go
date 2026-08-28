package index

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Index {
	t.Helper()
	ix, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func TestIndexAndSearchChinese(t *testing.T) {
	ix := openTest(t)
	now := time.Now()

	docs := []Doc{
		{Path: "2026/08/28/180000_dna.md", NasPath: "/data/archive/2026/08/28/180000_dna.md",
			Title: "DNA双螺旋与成长", Tags: "科学, 生物", Type: "text", Time: now,
			Content: "牛顿的棱镜把白光分解为七色，DNA 双螺旋则把生命写成四个字母的诗。"},
		{Path: "2026/08/28/190000_golang.md", NasPath: "/data/archive/2026/08/28/190000_golang.md",
			Title: "Go 并发模型", Tags: "编程, Go", Type: "text", Time: now,
			Content: "Goroutine 是轻量级协程，channel 用来在协程之间传递数据，不要通过共享内存来通信。"},
		{Path: "2026/08/27/090000_ai.md", NasPath: "/data/archive/2026/08/27/090000_ai.md",
			Title: "剪贴板归档", Tags: "工具", Type: "text", Time: now,
			Content: "把每一次复制都沉淀为知识资产，私有化存储到 NAS。"},
	}
	for _, d := range docs {
		if err := ix.Index(d.Path, d.NasPath, d.Title, d.Tags, d.Type, d.Content, d.Time); err != nil {
			t.Fatalf("index %s: %v", d.Path, err)
		}
	}

	if n, _ := ix.Count(); n != 3 {
		t.Fatalf("count = %d, want 3", n)
	}

	// 中文子串（>=3 字符）走 FTS5 trigram
	hits, err := ix.Search("双螺旋", 10)
	if err != nil {
		t.Fatalf("search 双螺旋: %v", err)
	}
	if len(hits) != 1 || hits[0].Path != "2026/08/28/180000_dna.md" {
		t.Fatalf("search 双螺旋 hits = %+v", hits)
	}
	if hits[0].Snippet == "" {
		t.Fatal("snippet should not be empty")
	}

	// 英文子串大小写不敏感
	hits, err = ix.Search("goroutine", 10)
	if err != nil {
		t.Fatalf("search goroutine: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("search goroutine hits = %d", len(hits))
	}

	// 标题命中
	hits, err = ix.Search("剪贴板归档", 10)
	if err != nil {
		t.Fatalf("search title: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("search title hits = %d", len(hits))
	}

	// 短词（2 字符）走 LIKE 兜底
	hits, err = ix.Search("沉淀", 10)
	if err != nil {
		t.Fatalf("search 沉淀: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("search 沉淀 hits = %d", len(hits))
	}

	// 无命中
	hits, err = ix.Search("不存在的关键词xyz", 10)
	if err != nil {
		t.Fatalf("search miss: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 hits, got %d", len(hits))
	}
}

func TestIdempotentIndex(t *testing.T) {
	ix := openTest(t)
	now := time.Now()
	d := Doc{Path: "2026/08/28/a.md", NasPath: "/x/a.md", Title: "t", Type: "text", Time: now, Content: "内容内容"}
	if err := ix.Index(d.Path, d.NasPath, d.Title, d.Tags, d.Type, d.Content, d.Time); err != nil {
		t.Fatal(err)
	}
	if err := ix.Index(d.Path, d.NasPath, d.Title, d.Tags, d.Type, d.Content, d.Time); err != nil {
		t.Fatal(err)
	}
	if n, _ := ix.Count(); n != 1 {
		t.Fatalf("count = %d, want 1 (幂等)", n)
	}
}

func TestGetByPathAndTags(t *testing.T) {
	ix := openTest(t)
	now := time.Now()
	if err := ix.Index("2026/08/28/a.md", "/x/a.md", "标题A", "科学, 生物", "text", "正文A", now); err != nil {
		t.Fatal(err)
	}
	if err := ix.Index("2026/08/28/b.md", "/x/b.md", "标题B", "科学", "text", "正文B", now); err != nil {
		t.Fatal(err)
	}

	doc, err := ix.GetByPath("2026/08/28/a.md")
	if err != nil || doc == nil {
		t.Fatalf("get: %v %v", doc, err)
	}
	if doc.Content != "正文A" || doc.Tags != "科学, 生物" || doc.NasPath != "/x/a.md" {
		t.Fatalf("doc = %+v", doc)
	}
	if miss, _ := ix.GetByPath("nope.md"); miss != nil {
		t.Fatalf("expected nil for missing path, got %+v", miss)
	}

	tags, err := ix.AllTags()
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 {
		t.Fatalf("tags = %+v, want 2", tags)
	}
	if tags[0].Tag != "科学" || tags[0].Count != 2 {
		t.Fatalf("top tag = %+v, want 科学=2", tags[0])
	}
}
