package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const defaultMaxEntries = 1000

// readBlockSize 逆向读取的块大小
const readBlockSize = 4096

// chunkPool 复用 readRecent 的读取缓冲区，减少高频调用时的 GC 压力
var chunkPool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, readBlockSize)
		return &buf
	},
}

// Entry 历史条目
type Entry struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`   // 截断后的预览，完整内容从 NAS 反查
	Type      string    `json:"type"`      // "text" 或 "image"
	Timestamp time.Time `json:"timestamp"`
	NASPath   string    `json:"nas_path"`  // 可选，指向完整归档文件路径
	Tags      []string  `json:"tags"`      // AI 生成的标签
}

// Store 历史存储
type Store struct {
	path       string
	mu         sync.Mutex
	maxEntries int
	count      int  // 已追踪的条目数
	counted    bool // 是否已统计过

	tagCache      map[string]int // 标签计数缓存，避免每次 AllTags 全量扫描
	tagCacheBuilt bool            // 标签缓存是否已构建
}

// NewStore 创建历史存储实例
func NewStore(dir string) *Store {
	return &Store{
		path:       filepath.Join(dir, "history.jsonl"),
		maxEntries: defaultMaxEntries,
		tagCache:   make(map[string]int),
	}
}

// NewStoreWithLimit 创建带自定义最大条目数的存储实例
func NewStoreWithLimit(dir string, maxEntries int) *Store {
	return &Store{
		path:       filepath.Join(dir, "history.jsonl"),
		maxEntries: maxEntries,
		tagCache:   make(map[string]int),
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

	// 增量更新标签缓存
	if s.tagCacheBuilt {
		for _, tag := range entry.Tags {
			s.tagCache[tag]++
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

	// compact 后重建标签缓存
	s.tagCache = make(map[string]int)
	for _, e := range keep {
		for _, tag := range e.Tags {
			s.tagCache[tag]++
		}
	}
	s.tagCacheBuilt = true

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
// 使用逆向读取避免加载整个 JSONL 文件
func (s *Store) Recent(n int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readRecent(n)
	if err != nil {
		// 回退到全量读取
		return s.recentFallback(n)
	}
	return entries, nil
}

// readRecent 从文件末尾逆向读取最近 n 条记录
func (s *Store) readRecent(n int) ([]Entry, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := fi.Size()
	if fileSize == 0 {
		return nil, nil
	}

	// 从文件末尾按块逆向读取，收集完整行
	var lines []string
	var tailBuf []byte
	remaining := fileSize
	atTail := true // 文件末尾可能没有换行，跳过尾部空行

	for remaining > 0 && len(lines) <= n {
		readSize := int64(readBlockSize)
		if remaining < readSize {
			readSize = remaining
		}
		remaining -= readSize

		// 从池中复用缓冲区
		chunkPtr := chunkPool.Get().(*[]byte)
		chunk := (*chunkPtr)[:readSize]

		if _, err := f.Seek(remaining, 0); err != nil {
			chunkPool.Put(chunkPtr)
			return nil, err
		}
		if _, err := f.Read(chunk); err != nil {
			chunkPool.Put(chunkPtr)
			return nil, err
		}

		// 将 chunk 和之前未完成的行首拼接
		// 注意：这里 chunk 来自池，容量固定，拼接需要新切片
		combined := make([]byte, len(chunk)+len(tailBuf))
		copy(combined, chunk)
		copy(combined[len(chunk):], tailBuf)
		tailBuf = nil

		// 归还缓冲区到池
		chunkPool.Put(chunkPtr)

		// 从后向前拆分行
		data := combined
		for i := len(data) - 1; i >= 0; i-- {
			if data[i] == '\n' {
				if atTail {
					// 跳过末尾的空行
					if i < len(data)-1 {
						lines = append(lines, string(data[i+1:]))
					}
				} else {
					if i < len(data)-1 {
						lines = append(lines, string(data[i+1:]))
					}
				}
				atTail = false
				data = data[:i]

				if len(lines) >= n {
					break
				}
			}
		}
		if len(data) > 0 {
			tailBuf = data
		}
	}

	// 处理文件最开头没有换行的剩余数据
	if len(tailBuf) > 0 && len(lines) < n {
		lines = append(lines, string(tailBuf))
	}

	// lines 中顺序是从文件末尾到开头，所以 entries[0] 是最新的
	var entries []Entry
	for i := 0; i < len(lines) && len(entries) < n; i++ {
		line := strings.TrimRight(lines[i], "\r")
		if line == "" {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(line), &e) == nil {
			entries = append(entries, e)
		}
	}

	return entries, nil
}

// recentFallback 回退到全量读取方式
func (s *Store) recentFallback(n int) ([]Entry, error) {
	entries, err := s.readAll()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}

	if len(entries) > n {
		entries = entries[len(entries)-n:]
	}

	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}

// RecentAfter 返回指定时间戳之前的 n 条记录（倒序），用于分页加载
func (s *Store) RecentAfter(before time.Time, n int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 需要读取足够多的条目来过滤出 before 之前的
	// 读取 maxEntries 条以确保有足够数据
	entries, err := s.readRecent(s.maxEntries)
	if err != nil {
		entries, err = s.recentFallback(s.maxEntries)
		if err != nil {
			return nil, err
		}
	}

	var result []Entry
	for _, e := range entries {
		if !e.Timestamp.After(before) && !e.Timestamp.Equal(before) {
			result = append(result, e)
			if len(result) >= n {
				break
			}
		}
	}
	return result, nil
}

// WeekCount 每周统计
type WeekCount struct {
	Label      string `json:"label"`
	Count      int    `json:"count"`
	TextCount  int    `json:"text_count"`
	ImageCount int    `json:"image_count"`
}

// DayCount 每日统计
type DayCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// Stats 统计数据
type Stats struct {
	TotalSyncs  int         `json:"total_syncs"`
	TextCount   int         `json:"text_count"`
	ImageCount  int         `json:"image_count"`
	WeeklyTrend []WeekCount `json:"weekly_trend"`
	DailyTrend  []DayCount  `json:"daily_trend"`
}

// Stats 聚合统计历史数据
func (s *Store) Stats() (*Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readAll()
	if err != nil {
		return nil, err
	}

	stats := &Stats{
		TotalSyncs:  len(entries),
		WeeklyTrend: make([]WeekCount, 0, 8),
		DailyTrend:  make([]DayCount, 0, 30),
	}

	// 计算类型计数
	textCount := 0
	imageCount := 0
	for _, e := range entries {
		if e.Type == "text" {
			textCount++
		} else {
			imageCount++
		}
	}
	stats.TextCount = textCount
	stats.ImageCount = imageCount

	// 按天聚合
	dayMap := make(map[string]int)
	for _, e := range entries {
		day := e.Timestamp.Format("2006-01-02")
		dayMap[day]++
	}

	// 最近 30 天趋势
	now := time.Now()
	for i := 29; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		stats.DailyTrend = append(stats.DailyTrend, DayCount{
			Date:  d,
			Count: dayMap[d],
		})
	}

	// 按周聚合（ISO 周）
	type weekKey struct {
		year, week int
	}
	weekMap := make(map[weekKey]struct {
		text, image int
	})
	for _, e := range entries {
		y, w := e.Timestamp.ISOWeek()
		k := weekKey{y, w}
		s := weekMap[k]
		if e.Type == "text" {
			s.text++
		} else {
			s.image++
		}
		weekMap[k] = s
	}

	// 最近 8 周
	for i := 7; i >= 0; i-- {
		t := now.AddDate(0, 0, -7*i)
		y, w := t.ISOWeek()
		k := weekKey{y, w}
		s := weekMap[k]
		label := fmt.Sprintf("%d-W%02d", y, w)
		stats.WeeklyTrend = append(stats.WeeklyTrend, WeekCount{
			Label:      label,
			Count:      s.text + s.image,
			TextCount:  s.text,
			ImageCount: s.image,
		})
	}

	return stats, nil
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
		if json.Unmarshal(scanner.Bytes(), &e) == nil && (e.ID == id || strings.HasPrefix(e.ID, id)) {
			return &e, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("find history error: %w", err)
	}
	return nil, nil
}

// TagCount 标签及其出现次数
type TagCount struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// buildTagCache 构建标签缓存（调用方需持有 s.mu）
func (s *Store) buildTagCache() error {
	entries, err := s.readAll()
	if err != nil {
		return err
	}

	s.tagCache = make(map[string]int)
	for _, e := range entries {
		for _, tag := range e.Tags {
			s.tagCache[tag]++
		}
	}
	s.tagCacheBuilt = true
	return nil
}

// AllTags 返回所有去重标签及出现次数，按次数降序排列
// 使用内存缓存，首次调用后不再全量扫描文件
func (s *Store) AllTags() ([]TagCount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.tagCacheBuilt {
		if err := s.buildTagCache(); err != nil {
			return nil, err
		}
	}

	result := make([]TagCount, 0, len(s.tagCache))
	for tag, count := range s.tagCache {
		result = append(result, TagCount{Tag: tag, Count: count})
	}

	// 按次数降序
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})

	return result, nil
}

// UpdateTags 更新指定 ID 条目的标签
func (s *Store) UpdateTags(id string, newTags []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readAll()
	if err != nil {
		return err
	}

	// 查找并更新指定 ID 的条目
	found := false
	for i := range entries {
		if entries[i].ID == id {
			// 合并标签，去重
			tagMap := make(map[string]bool)
			for _, tag := range entries[i].Tags {
				tagMap[tag] = true
			}

			// 记录旧标签用于缓存更新
			oldTags := make(map[string]bool)
			for _, tag := range entries[i].Tags {
				oldTags[tag] = true
			}

			for _, tag := range newTags {
				tagMap[tag] = true
			}
			entries[i].Tags = make([]string, 0, len(tagMap))
			for tag := range tagMap {
				entries[i].Tags = append(entries[i].Tags, tag)
			}
			found = true

			// 增量更新标签缓存
			if s.tagCacheBuilt {
				for _, tag := range newTags {
					if !oldTags[tag] {
						s.tagCache[tag]++
					}
				}
			}
			break
		}
	}

	if !found {
		return fmt.Errorf("entry with id %s not found", id)
	}

	// 写入临时文件
	tmpPath := s.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	writer := bufio.NewWriter(f)
	for _, e := range entries {
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

	return nil
}

// GetByID 根据 ID 获取条目
func (s *Store) GetByID(id string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readAll()
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		if e.ID == id {
			return &e, nil
		}
	}

	return nil, fmt.Errorf("entry with id %s not found", id)
}
