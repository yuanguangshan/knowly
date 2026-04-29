package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type Puller struct {
	baseURL  string
	secret   string
	interval time.Duration
	stopChan chan struct{}
	callback func(string)
}

func NewPuller(endpoint, secret string, interval time.Duration, callback func(string)) *Puller {
	return &Puller{
		baseURL:  endpoint,
		secret:   secret,
		interval: interval,
		stopChan: make(chan struct{}),
		callback: callback,
	}
}

func (p *Puller) Start() {
	go func() {
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		log.Println("[INFO] Relay puller started, endpoint:", p.baseURL)

		for {
			select {
			case <-p.stopChan:
				log.Println("[INFO] Relay puller stopped")
				return
			case <-ticker.C:
				contents, err := p.pull()
				if err != nil {
					log.Printf("[DEBUG] Relay pull failed: %v", err)
					continue
				}
				for _, content := range contents {
					if content != "" {
						log.Printf("[INFO] Relay content received (length: %d)", len(content))
						p.callback(content)
					}
				}
			}
		}
	}()
}

func (p *Puller) pull() ([]string, error) {
	// 只拉取 general 队列，zhihu 队列由 Chrome 扩展处理
	req, err := http.NewRequest("GET", p.baseURL+"/pull?queue=general", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Auth-Key", p.secret)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	raw := string(body)
	if raw == "" {
		return nil, nil
	}

	// 队列模式：服务端返回 JSON 数组 ["content1", "content2", ...]
	var result []string
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	return result, nil
}

// Push 将处理后的内容推送到 relay 结果队列（广播给所有客户端）
func (p *Puller) Push(content string) error {
	payload, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return fmt.Errorf("marshal push payload: %w", err)
	}

	req, err := http.NewRequest("POST", p.baseURL+"/push", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("X-Auth-Key", p.secret)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("push failed with status: %d", resp.StatusCode)
	}

	log.Printf("[INFO] Relay push OK (length: %d)", len(content))
	return nil
}

func (p *Puller) Stop() {
	close(p.stopChan)
}
