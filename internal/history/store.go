package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

const defaultMaxEntries = 1000

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
	path       string
	mu         sync.Mutex
	maxEntries int
	count      int  // 已追踪的条目数
	counted    bool // 是否已统计过
}

// NewStore 创建历史存储实例
func NewStore(dir string) *Store {
	return &Store{
		path:       filepath.Join(dir, "history.jsonl"),
		maxEntries: defaultMaxEntries,
	}
}

// NewStoreWithLimit 创建带自定义最大条目数的存储实例
func NewStoreWithLimit(dir string, maxEntries int) *Store {
	return &Store{
		path:       filepath.Join(dir, "history.jsonl"),
		maxEntries: maxEntries,
	}
}

// ensureCount 在首次需要时统计文件行数
// 注意：调用方必须持有 s.mu
func (s *Store) ensureCount() {
	if s.counted {
		return
	}
	s.counted = true

	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)
	for scanner.Scan() {
		s.count++
	}
}

// Append 线程安全地追加一条记录
func (s *Store) Append(entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 在写入前统计已有条目数（避免与刚写入的条目重复计数）
	s.ensureCount()

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

	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}

	// 跟踪条目数，超过阈值时压缩
	s.count++
	if s.count > s.maxEntries*2 {
		if err := s.compact(); err != nil {
			log.Printf("[WARN] History compaction failed: %v", err)
		}
	}

	return nil
}

// compact 保留最近 maxEntries 条记录，原子写回
func (s *Store) compact() error {
	entries, err := s.readAll()
	if err != nil {
		return err
	}

	if len(entries) <= s.maxEntries {
		return nil
	}

	// 保留最近 maxEntries 条
	keep := entries[len(entries)-s.maxEntries:]

	// 写入临时文件
	tmpPath := s.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	writer := bufio.NewWriter(f)
	for _, e := range keep {
		data, err := json.Marshal(e)
		if err != nil {
			f.Close()
			os.Remove(tmpPath)
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			f.Close()
			os.Remove(tmpPath)
			return err
		}
	}

	if err := writer.Flush(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	f.Close()

	// 原子替换
	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		return err
	}

	s.count = len(keep)
	log.Printf("[INFO] History compacted: %d entries remaining", len(keep))
	return nil
}

// readAll 读取所有条目（不加锁，调用方需持有锁）
func (s *Store) readAll() ([]Entry, error) {
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
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err == nil {
			entries = append(entries, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read history error: %w", err)
	}
	return entries, nil
}

// Recent 返回最近的 n 条记录（倒序：最近的在前面）
func (s *Store) Recent(n int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readAll()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)
	for scanner.Scan() {
		var e Entry
		if json.Unmarshal(scanner.Bytes(), &e) == nil && e.ID == id {
			return &e, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("find history error: %w", err)
	}
	return nil, nil
}
