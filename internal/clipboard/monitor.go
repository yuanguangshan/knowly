package clipboard

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/yuanguangshan/knas/internal/fetcher"
	"golang.design/x/clipboard"
)

type ClipboardItem struct {
	Content   string
	Timestamp time.Time
	Hash      string
	IsImage   bool
	ImageData []byte
}

type Monitor struct {
	mu            sync.RWMutex // v1.3.0: 并发安全锁
	lastHash      string
	lastContent   string
	minLength     int
	maxLength     int
	stopChan      chan struct{}
	wg            sync.WaitGroup
	itemChan      chan ClipboardItem
	pollInterval  time.Duration
	excludeWords  []string
}

type MonitorConfig struct {
	MinLength     int
	MaxLength     int
	PollInterval  time.Duration
	ExcludeWords  []string
	BufferSize    int
}

func NewMonitor(config MonitorConfig) *Monitor {
	if config.MinLength == 0 {
		config.MinLength = 100
	}
	if config.MaxLength == 0 {
		config.MaxLength = 1024 * 1024 // 1MB 默认值
	}
	if config.PollInterval == 0 {
		config.PollInterval = 500 * time.Millisecond
	}
	if config.BufferSize == 0 {
		config.BufferSize = 10
	}

	if err := clipboard.Init(); err != nil {
		log.Printf("[ERROR] Failed to init clipboard: %v", err)
	}

	return &Monitor{
		minLength:    config.MinLength,
		maxLength:    config.MaxLength,
		pollInterval: config.PollInterval,
		excludeWords: config.ExcludeWords,
		stopChan:     make(chan struct{}),
		itemChan:     make(chan ClipboardItem, config.BufferSize),
	}
}

func (m *Monitor) hashContent(content string) string {
	hash := md5.Sum([]byte(content))
	return hex.EncodeToString(hash[:])
}

func (m *Monitor) hashImage(data []byte) string {
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:])
}

func (m *Monitor) shouldSync(content string) (bool, string) {
	// 检查长度
	if len(content) < m.minLength || len(content) > m.maxLength {
		return false, ""
	}

	// 检查是否包含排除词
	for _, word := range m.excludeWords {
		if strings.Contains(content, word) {
			return false, ""
		}
	}

	// 检查是否是重复内容 (v1.3.0: 加锁保护)
	hash := m.hashContent(content)
	m.mu.RLock()
	isDup := hash == m.lastHash
	m.mu.RUnlock()

	return !isDup, hash
}

func (m *Monitor) readClipboard() (*ClipboardItem, error) {
	// 1. 优先尝试读取图片
	imgData := clipboard.Read(clipboard.FmtImage)
	if len(imgData) > 0 {
		return &ClipboardItem{
			IsImage:   true,
			ImageData: imgData,
			Timestamp: time.Now(),
			Hash:      m.hashImage(imgData),
		}, nil
	}

	// 2. 回退读取文本
	txtData := clipboard.Read(clipboard.FmtText)
	if len(txtData) == 0 {
		return nil, fmt.Errorf("clipboard empty")
	}

	content := string(txtData)
	return &ClipboardItem{
		Content:   content,
		Timestamp: time.Now(),
		Hash:      m.hashContent(content),
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
				item, err := m.readClipboard()
				if err != nil {
					continue
				}

				// 检查重复
				m.mu.RLock()
				isDup := item.Hash == m.lastHash
				m.mu.RUnlock()

				if isDup {
					continue
				}

				// 更新状态
				m.mu.Lock()
				m.lastHash = item.Hash
				if !item.IsImage {
					m.lastContent = item.Content
				}
				m.mu.Unlock()

				log.Printf("[INFO] New item detected: %s (type: %v)", item.Hash[:8], item.IsImage)

				// 分发处理
				if item.IsImage {
					go m.emitItem(*item)
				} else {
					// 文本需要增强（抓标题）
					go m.enhanceAndSend(item.Content, item.Hash)
				}
			}
		}
	}()
}

func (m *Monitor) emitItem(item ClipboardItem) {
	select {
	case m.itemChan <- item:
	default:
		log.Printf("[WARN] Item channel full, dropping item")
	}
}

// v1.3.0: 独立的增强逻辑，包含超时控制
func (m *Monitor) enhanceAndSend(content string, hash string) {
	enhanced := content
	if fetcher.IsURL(content) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		done := make(chan string, 1)
		go func() {
			title, err := fetcher.FetchTitle(ctx, content)
			if err == nil && title != "" {
				done <- fmt.Sprintf("%s\n\n%s", content, title)
			} else {
				done <- ""
			}
		}()

		select {
		case res := <-done:
			if res != "" {
				enhanced = res
				log.Printf("[INFO] Fetched title for URL")
			}
		case <-ctx.Done():
			log.Printf("[WARN] Title fetch timed out")
		}
	}

	item := ClipboardItem{
		Content:   enhanced,
		Timestamp: time.Now(),
		Hash:      hash,
	}

	select {
	case m.itemChan <- item:
	default:
		log.Printf("[WARN] Item channel full, dropping item")
	}
}

func (m *Monitor) Stop() {
	close(m.stopChan)
	m.wg.Wait()
	close(m.itemChan)
}

func (m *Monitor) Items() <-chan ClipboardItem {
	return m.itemChan
}

// v1.3.0: 线程安全的读取
func (m *Monitor) GetLastContent() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastContent
}
