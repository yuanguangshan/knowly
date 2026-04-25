package clipboard

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yuanguangshan/knowly/internal/fetcher"
	xclip "golang.design/x/clipboard"
)

// 统一载荷接口
type Payload interface {
	isPayload()
	Hash() string
	Type() string
	Preview() string
}

type TextPayload struct {
	Content   string
	Timestamp time.Time
	hash      string
}

type ImagePayload struct {
	Data      []byte
	Timestamp time.Time
	hash      string
}

func (TextPayload) isPayload()         {}
func (ImagePayload) isPayload()        {}
func (t TextPayload) Hash() string     { return t.hash }
func (i ImagePayload) Hash() string    { return i.hash }
func (TextPayload) Type() string       { return "text" }
func (ImagePayload) Type() string      { return "image" }
func (t TextPayload) Preview() string  { return t.Content }
func (i ImagePayload) Preview() string { return "[IMAGE]" }

// hash 辅助函数
func hashBytes(b []byte) string {
	h := md5.Sum(b)
	return hex.EncodeToString(h[:])
}

func hashStr(s string) string {
	return hashBytes([]byte(s))
}

// FilterReason 记录过滤的具体原因
type FilterReason struct {
	Filtered    bool
	Reason      string // "length_too_short", "length_too_long", "exclude_word"
	MatchedWord string // 仅 exclude_word 时有值
}

// ShouldFilter 检查内容是否应被过滤（长度不足、超出上限、包含敏感词）
// 导出此函数以便 Relay 等外部路径复用同一过滤逻辑
func ShouldFilter(content string, minLength, maxLength int, excludeWords []string) bool {
	r := ShouldFilterDetail(content, minLength, maxLength, excludeWords)
	return r.Filtered
}

// ShouldFilterDetail 返回详细的过滤结果，包含具体原因
func ShouldFilterDetail(content string, minLength, maxLength int, excludeWords []string) FilterReason {
	// 如果是符合条件的短链接，不受最小长度限制
	if !fetcher.IsURL(content) && len(content) < minLength {
		return FilterReason{Filtered: true, Reason: "length_too_short"}
	}
	if len(content) > maxLength {
		return FilterReason{Filtered: true, Reason: "length_too_long"}
	}
	for _, word := range excludeWords {
		if strings.Contains(content, word) {
			return FilterReason{Filtered: true, Reason: "exclude_word", MatchedWord: word}
		}
	}
	return FilterReason{Filtered: false}
}

type Monitor struct {
	mu           sync.RWMutex
	lastHash     string
	lastContent  string
	lastType     string
	statusPath   string
	minLength    int
	maxLength    int
	stopChan     chan struct{}
	wg           sync.WaitGroup
	itemChan     chan Payload
	pollInterval time.Duration
	excludeWords []string
}

type MonitorConfig struct {
	MinLength    int
	MaxLength    int
	PollInterval time.Duration
	ExcludeWords []string
	BufferSize   int
}

func NewMonitor(config MonitorConfig, statusPath string) *Monitor {
	if config.MinLength == 0 {
		config.MinLength = 100
	}
	if config.MaxLength == 0 {
		config.MaxLength = 1024 * 1024
	}
	if config.PollInterval == 0 {
		config.PollInterval = 500 * time.Millisecond
	}
	if config.BufferSize == 0 {
		config.BufferSize = 10
	}

	m := &Monitor{
		minLength:    config.MinLength,
		maxLength:    config.MaxLength,
		pollInterval: config.PollInterval,
		excludeWords: config.ExcludeWords,
		statusPath:   statusPath,
		stopChan:     make(chan struct{}),
		itemChan:     make(chan Payload, config.BufferSize),
	}

	// 从 status.json 恢复 lastHash，防止重启后重复同步
	m.loadStatus()

	// 关键：x/clipboard 必须在程序启动时 Init 一次
	if err := xclip.Init(); err != nil {
		log.Printf("[WARN] x/clipboard init failed: %v", err)
	}

	return m
}

// loadStatus 从 status.json 恢复上次的 hash 和状态
func (m *Monitor) loadStatus() {
	data, err := os.ReadFile(m.statusPath)
	if err != nil {
		return
	}
	var status map[string]any
	if err := json.Unmarshal(data, &status); err != nil {
		return
	}
	if hash, ok := status["hash"].(string); ok && hash != "" {
		m.lastHash = hash
		log.Printf("[INFO] Restored lastHash from status: %s", hash[:8]+"...")
	}
	if preview, ok := status["preview"].(string); ok {
		m.lastContent = preview
	}
	if typ, ok := status["last_type"].(string); ok {
		m.lastType = typ
	}
}

func (m *Monitor) isDuplicate(hash string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return hash == m.lastHash
}

// IsDuplicate 公开的去重检查，供 relay 等外部调用者使用
func (m *Monitor) IsDuplicate(content string) bool {
	return m.isDuplicate(hashStr(content))
}

func (m *Monitor) readPayload() (Payload, error) {
	// 1. 优先图片
	img := xclip.Read(xclip.FmtImage)
	if len(img) > 0 {
		return ImagePayload{
			Data:      img,
			Timestamp: time.Now(),
			hash:      hashBytes(img),
		}, nil
	}

	// 2. 回退文本
	txt := xclip.Read(xclip.FmtText)
	content := string(txt)
	if content == "" {
		return nil, fmt.Errorf("empty clipboard")
	}
	return TextPayload{
		Content:   content,
		Timestamp: time.Now(),
		hash:      hashStr(content),
	}, nil
}

func (m *Monitor) Start() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		log.Println("[INFO] Clipboard monitor started (Text + Image)")

		ticker := time.NewTicker(m.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-m.stopChan:
				log.Println("[INFO] Clipboard monitor stopped")
				return
			case <-ticker.C:
				payload, err := m.readPayload()
				if err != nil {
					continue
				}

				switch v := payload.(type) {
				case TextPayload:
					// 使用统一的过滤函数
					if ShouldFilter(v.Content, m.minLength, m.maxLength, m.excludeWords) || m.isDuplicate(v.Hash()) {
						continue
					}

					m.updateState(v.Hash(), v.Preview(), v.Type())
					go m.enhanceAndSend(v.Content, v.Hash())

				case ImagePayload:
					if !m.isDuplicate(v.Hash()) {
						m.updateState(v.Hash(), v.Preview(), v.Type())
						go m.archiveImage(v)
					}
				}
			}
		}
	}()
}

func (m *Monitor) updateState(hash, preview, typ string) {
	m.mu.Lock()
	m.lastHash = hash
	m.lastContent = preview
	m.lastType = typ
	m.mu.Unlock()
	m.saveStatus()
}

func (m *Monitor) saveStatus() {
	m.mu.RLock()
	preview := m.lastContent
	if len(preview) > 50 {
		preview = preview[:50] + "..."
	}
	status := map[string]any{
		"last_sync": time.Now().Format("2006-01-02 15:04:05"),
		"last_type": m.lastType,
		"preview":   preview,
		"hash":      m.lastHash,
	}
	m.mu.RUnlock()

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		log.Printf("[WARN] Failed to marshal status: %v", err)
		return
	}
	if err := os.WriteFile(m.statusPath, data, 0644); err != nil {
		log.Printf("[WARN] Failed to write status file: %v", err)
	}
}

// 独立的增强逻辑，包含超时控制
func (m *Monitor) enhanceAndSend(content string, hash string) {
	enhanced := content
	if fetcher.IsURL(content) {
		urlStr := fetcher.ExtractURL(content)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// 获取页面标题和内容
		info, err := fetcher.FetchPage(ctx, urlStr)
		if err == nil && info != nil {
			if info.Title != "" && info.Content != "" {
				enhanced = fmt.Sprintf("%s\n\n# %s\n\n%s", content, info.Title, info.Content)
				log.Printf("[INFO] Fetched title and content for URL")
			} else if info.Title != "" {
				enhanced = fmt.Sprintf("%s\n\n# %s", content, info.Title)
				log.Printf("[INFO] Fetched title for URL")
			} else if info.Content != "" {
				enhanced = fmt.Sprintf("%s\n\n%s", content, info.Content)
				log.Printf("[INFO] Fetched content for URL")
			}
		} else {
			log.Printf("[DEBUG] Failed to fetch page: %v", err)
		}
	}

	item := TextPayload{
		Content:   enhanced,
		Timestamp: time.Now(),
		hash:      hash,
	}

	select {
	case m.itemChan <- item:
	default:
		log.Printf("[WARN] Item channel full, dropping item")
	}
}

func (m *Monitor) archiveImage(img ImagePayload) {
	log.Printf("[INFO] Image detected (%d bytes), sending to sync", len(img.Data))

	select {
	case m.itemChan <- img:
	default:
		log.Printf("[WARN] Image channel full, dropping")
	}
}

func (m *Monitor) Stop() {
	close(m.stopChan)
	m.wg.Wait()
	close(m.itemChan)
}

func (m *Monitor) Items() <-chan Payload {
	return m.itemChan
}

// 线程安全的读取
func (m *Monitor) GetLastContent() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastContent
}
