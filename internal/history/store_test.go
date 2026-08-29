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

func TestStatsIncremental(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	// 追加 8 条文本 + 2 条图片
	for i := 0; i < 8; i++ {
		_, _ = s.Append(Entry{Content: "text", Type: "text"})
	}
	for i := 0; i < 2; i++ {
		_, _ = s.Append(Entry{Content: "[IMAGE]", Type: "image"})
	}

	st, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if st.TotalSyncs != 10 {
		t.Errorf("TotalSyncs = %d, want 10", st.TotalSyncs)
	}
	if st.TextCount != 8 {
		t.Errorf("TextCount = %d, want 8", st.TextCount)
	}
	if st.ImageCount != 2 {
		t.Errorf("ImageCount = %d, want 2", st.ImageCount)
	}
	if len(st.DailyTrend) != 30 {
		t.Errorf("DailyTrend len = %d, want 30", len(st.DailyTrend))
	}
	// 今天的条目数应为 10（全部是刚刚追加的）
	today := st.DailyTrend[len(st.DailyTrend)-1]
	if today.Count != 10 {
		t.Errorf("today count = %d, want 10", today.Count)
	}

	// 追加一条后缓存未过期时总数应立即反映（增量计数不受 60s 缓存影响，
	// 但缓存 60s 内返回旧值——这里强制走缓存路径，验证不 panic 且值来自增量）
	if _, err := s.Append(Entry{Content: "more", Type: "text"}); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Count() 应与实际条目数一致（O(1) 路径）
	if got := s.Count(); got != 11 {
		t.Errorf("Count() = %d, want 11", got)
	}

	// TrimTo 触发增量统计重建，TotalSyncs 应与新文件一致
	if err := s.TrimTo(5); err != nil {
		t.Fatalf("TrimTo failed: %v", err)
	}
	st2, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats after TrimTo failed: %v", err)
	}
	if st2.TotalSyncs != 5 {
		t.Errorf("TotalSyncs after TrimTo = %d, want 5", st2.TotalSyncs)
	}
}
