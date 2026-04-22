package outbox

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Item 待同步的条目（SSH 连不上时本地暂存）
type Item struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"` // "text" 或 "image"
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	// AI 元数据（仅文本）
	Tags             []string `json:"tags,omitempty"`
	Summary          string   `json:"summary,omitempty"`
	Score            int      `json:"score,omitempty"`
	OrganizedContent string   `json:"organized_content,omitempty"`
	Processed        bool     `json:"processed,omitempty"`
}

// SyncFunc 将 Item 同步到远端的函数签名，返回远端路径或错误
type SyncFunc func(item Item) (string, error)

// Store 本地暂存队列
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore 创建暂存队列
func NewStore(configDir string) *Store {
	dir := filepath.Join(configDir, "outbox")
	os.MkdirAll(dir, 0755)
	return &Store{
		path: filepath.Join(dir, "pending.jsonl"),
	}
}

// Push 将一个条目加入暂存队列
func (s *Store) Push(item Item) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if item.ID == "" {
		item.ID = fmt.Sprintf("%s_%s",
			time.Now().Format("20060102150405"),
			uuid.New().String()[:8])
	}

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("outbox: failed to open: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("outbox: marshal failed: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("outbox: write failed: %w", err)
	}

	log.Printf("[INFO] Outbox: saved %s item (%s), will sync when SSH recovers", item.Type, item.ID[:14])
	return nil
}

// PendingCount 返回待同步条目数量
func (s *Store) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		if len(scanner.Bytes()) > 0 {
			count++
		}
	}
	return count
}

// Drain 尝试将所有暂存条目同步到远端
// syncFn: 实际执行同步的回调
// 成功的条目会被移除，遇到错误时停止并保留剩余条目
func (s *Store) Drain(syncFn SyncFunc) (synced int, drainErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("outbox: open failed: %w", err)
	}

	var items []Item
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var item Item
		if err := json.Unmarshal(scanner.Bytes(), &item); err == nil {
			items = append(items, item)
		}
	}
	f.Close()

	if len(items) == 0 {
		return 0, nil
	}

	var remaining []Item
	for i, item := range items {
		nasPath, syncErr := syncFn(item)
		if syncErr != nil {
			// 同步失败，保留该条目及后续所有条目
			remaining = append(remaining, items[i:]...)
			log.Printf("[WARN] Outbox: drain stopped at item %s: %v", item.ID[:14], syncErr)
			drainErr = syncErr
			break
		}
		synced++
		if nasPath != "" {
			log.Printf("[INFO] Outbox: synced pending %s -> %s", item.Type, nasPath)
		} else {
			log.Printf("[INFO] Outbox: synced pending %s (duplicate skipped)", item.Type)
		}
	}

	return synced, s.writeRemaining(remaining)
}

// DecodeImageContent 解码 base64 编码的图片数据
func DecodeImageContent(content string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(content)
}

func (s *Store) writeRemaining(items []Item) error {
	tmpPath := s.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("outbox: create tmp failed: %w", err)
	}

	writer := bufio.NewWriter(f)
	for _, item := range items {
		data, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("outbox: marshal failed: %w", marshalErr)
		}
		if _, writeErr := writer.Write(append(data, '\n')); writeErr != nil {
			f.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("outbox: write failed: %w", writeErr)
		}
	}

	if flushErr := writer.Flush(); flushErr != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("outbox: flush failed: %w", flushErr)
	}
	f.Close()

	if len(items) == 0 {
		// 全部同步完成，删除文件
		os.Remove(s.path)
		os.Remove(tmpPath)
		return nil
	}

	return os.Rename(tmpPath, s.path)
}
