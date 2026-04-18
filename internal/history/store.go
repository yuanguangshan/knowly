package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Entry 历史条目
type Entry struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`   // 截断后的预览，完整内容从 NAS 反查
	Type      string    `json:"type"`      // "text" 或 "image"
	Timestamp time.Time `json:"timestamp"`
	NASPath   string    `json:"nas_path"`  // 可选，指向完整归档文件路径
}

// Store 历史存储
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore 创建历史存储实例
func NewStore(dir string) *Store {
	return &Store{path: filepath.Join(dir, "history.jsonl")}
}

// Append 线程安全地追加一条记录
func (s *Store) Append(entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// 生成更可靠的唯一 ID：时间戳 + uuid 前8位
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%s_%s",
			time.Now().Format("20060102150405"),
			uuid.New().String()[:8])
	}
	entry.Timestamp = time.Now()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	_, err = f.Write(append(data, '\n'))
	return err
}

// Recent 返回最近的 n 条记录（倒序：最近的在前面）
func (s *Store) Recent(n int) ([]Entry, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err == nil {
			entries = append(entries, e)
		}
	}

	// 取最后 n 条
	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}

	// 反转顺序，最近的在前面
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

// Find 根据 ID 精确查找
func (s *Store) Find(id string) (*Entry, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var e Entry
		if json.Unmarshal(scanner.Bytes(), &e) == nil && e.ID == id {
			return &e, nil
		}
	}
	return nil, nil
}
