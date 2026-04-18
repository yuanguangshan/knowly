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

	xclip "golang.design/x/clipboard"
	"github.com/yuanguangshan/knas/internal/fetcher"
	"github.com/yuanguangshan/knas/internal/history"
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

func (TextPayload) isPayload()  {}
func (ImagePayload) isPayload() {}
func (t TextPayload) Hash() string { return t.hash }
func (i ImagePayload) Hash() string { return i.hash }
func (TextPayload) Type() string  { return "text" }
func (ImagePayload) Type() string { return "image" }
func (t TextPayload) Preview() string { return t.Content }
func (i ImagePayload) Preview() string { return "[IMAGE]" }

// hash 辅助函数
func hashBytes(b []byte) string {
	h := md5.Sum(b)
	return hex.EncodeToString(h[:])
}

func hashStr(s string) string {
	return hashBytes([]byte(s))
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
	historyStore *history.Store
}

type MonitorConfig struct {
	MinLength    int
	MaxLength    int
	PollInterval time.Duration
	ExcludeWords []string
	BufferSize   int
}

func NewMonitor(config MonitorConfig, statusPath string, histStore *history.Store) *Monitor {
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
		historyStore: histStore,
	}

	// 关键：x/clipboard 必须在程序启动时 Init 一次
	if err := xclip.Init(); err != nil {
		log.Printf("[WARN] x/clipboard init failed: %v", err)
	}

	return m
}

func (m *Monitor) isDuplicate(hash string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return hash == m.lastHash
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
					// Inline check: length, exclude words, duplicate
					if len(v.Content) < m.minLength || len(v.Content) > m.maxLength {
						continue
					}
					skip := false
					for _, word := range m.excludeWords {
						if strings.Contains(v.Content, word) {
							skip = true
							break
						}
					}
					if skip || m.isDuplicate(v.Hash()) {
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

	data, _ := json.MarshalIndent(status, "", "  ")
	os.WriteFile(m.statusPath, data, 0644)
}

// 独立的增强逻辑，包含超时控制
func (m *Monitor) enhanceAndSend(content string, hash string) {
	enhanced := content
	if fetcher.IsURL(content) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		// 直接调用，FetchTitle 已支持 context
		title, err := fetcher.FetchTitle(ctx, content)
		if err == nil && title != "" {
			enhanced = fmt.Sprintf("%s\n\n%s", content, title)
			log.Printf("[INFO] Fetched title for URL")
		} else {
			log.Printf("[DEBUG] Failed to fetch title: %v", err)
		}
	}

	item := TextPayload{
		Content:   enhanced,
		Timestamp: time.Now(),
		hash:      hash,
	}

	select {
	case m.itemChan <- item:
		// 发送成功后异步记录历史
		if m.historyStore != nil {
			preview := content
			if len(preview) > 200 {
				preview = preview[:200] + "..."
			}
			go m.historyStore.Append(history.Entry{
				Content: preview,
				Type:    "text",
			})
		}
	default:
		log.Printf("[WARN] Item channel full, dropping item")
	}
}

func (m *Monitor) archiveImage(img ImagePayload) {
	log.Printf("[INFO] Image detected (%d bytes), sending to sync", len(img.Data))
	
	// 发送到 Channel 供主程序消费
	select {
	case m.itemChan <- img:
		// 发送成功后异步记录历史
		if m.historyStore != nil {
			go m.historyStore.Append(history.Entry{
				Content:   fmt.Sprintf("[IMAGE] %d bytes", len(img.Data)),
				Type:      "image",
				Timestamp: img.Timestamp,
			})
		}
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
