package history

import (
	"testing"
)

func TestAppend(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	id, err := s.Append(Entry{Content: "hello world", Type: "text"})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if id == "" {
		t.Error("Append should return non-empty ID")
	}

	entries, err := s.Recent(10)
	if err != nil {
		t.Fatalf("Recent failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Content != "hello world" {
		t.Errorf("content = %q, want %q", entries[0].Content, "hello world")
	}
	if entries[0].ID == "" {
		t.Error("ID should be auto-generated")
	}
}

func TestRecent(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// 追加 10 条
	for i := 0; i < 10; i++ {
		_, _ = s.Append(Entry{Content: string(rune('a' + i)), Type: "text"})
	}

	// 取最近 3 条
	entries, err := s.Recent(3)
	if err != nil {
		t.Fatalf("Recent failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	// 应该是倒序，最近的在前面
	if entries[0].Content != "j" {
		t.Errorf("first entry = %q, want %q", entries[0].Content, "j")
	}
	if entries[2].Content != "h" {
		t.Errorf("last entry = %q, want %q", entries[2].Content, "h")
	}
}

func TestRecentEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	entries, err := s.Recent(10)
	if err != nil {
		t.Fatalf("Recent on empty store should not error: %v", err)
	}
	if entries != nil {
		t.Fatalf("expected nil entries, got %v", entries)
	}
}

func TestFind(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	_, _ = s.Append(Entry{Content: "first", Type: "text"})
	_, _ = s.Append(Entry{Content: "second", Type: "text"})
	_, _ = s.Append(Entry{Content: "third", Type: "text"})

	// 获取所有条目来拿到 ID
	entries, _ := s.Recent(10)
	if len(entries) < 2 {
		t.Fatal("need at least 2 entries")
	}

	// 找第二条（Recent 返回倒序，所以 entries[1] 是第二条插入的）
	secondID := entries[1].ID
	found, err := s.Find(secondID)
	if err != nil {
		t.Fatalf("Find failed: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find entry, got nil")
	}
	if found.Content != "second" {
		t.Errorf("found content = %q, want %q", found.Content, "second")
	}

	// 查不存在的 ID
	notFound, err := s.Find("nonexistent_id")
	if err != nil {
		t.Fatalf("Find nonexistent should not error: %v", err)
	}
	if notFound != nil {
		t.Error("expected nil for nonexistent ID")
	}
}


func TestCompaction(t *testing.T) {
	dir := t.TempDir()
	// 设置小的 maxEntries 以便快速触发压缩
	s := NewStoreWithLimit(dir, 10)

	// 追加 21 条（第 21 条触发 compact，保留 10 条）
	for i := 0; i < 21; i++ {
		if _, err := s.Append(Entry{Content: string(rune('A' + i)), Type: "text"}); err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
	}

	// 压缩后应保留 maxEntries 条
	entries, err := s.Recent(20)
	if err != nil {
		t.Fatalf("Recent failed: %v", err)
	}
	if len(entries) != 10 {
		t.Errorf("expected 10 entries after compaction, got %d", len(entries))
	}

	// 应该是最新的 10 条（L..U，Recent 倒序所以最新的 U 在前面）
	if entries[0].Content != "U" {
		t.Errorf("first entry after compaction = %q, want %q", entries[0].Content, "U")
	}
	if entries[9].Content != "L" {
		t.Errorf("last entry after compaction = %q, want %q", entries[9].Content, "L")
	}
}
