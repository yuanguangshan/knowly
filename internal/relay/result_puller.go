package relay

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// ResultPuller 周期性拉取 Worker 的结果队列，归档到 NAS
type ResultPuller struct {
	baseURL  string
	secret   string
	interval time.Duration
	stopChan chan struct{}
	callback func(content string)
	cursor   int64
	cursorMu sync.Mutex
}

type resultsResponse struct {
	Cursor int64         `json:"cursor"`
	Items  []resultsItem `json:"items"`
}

type resultsItem struct {
	T int64  `json:"t"`
	C string `json:"c"`
}

// NewResultPuller 创建结果拉取器
func NewResultPuller(endpoint, secret string, interval time.Duration, callback func(string)) *ResultPuller {
	return &ResultPuller{
		baseURL:  endpoint,
		secret:   secret,
		interval: interval,
		stopChan: make(chan struct{}),
		callback: callback,
	}
}

func (rp *ResultPuller) Start() {
	go func() {
		ticker := time.NewTicker(rp.interval)
		defer ticker.Stop()
		log.Println("[INFO] Result puller started, endpoint:", rp.baseURL)

		for {
			select {
			case <-rp.stopChan:
				log.Println("[INFO] Result puller stopped")
				return
			case <-ticker.C:
				items, err := rp.pullResults()
				if err != nil {
					log.Printf("[DEBUG] Result pull failed: %v", err)
					continue
				}
				for _, item := range items {
					if item.C != "" {
						log.Printf("[INFO] Result pulled (len=%d)", len(item.C))
						rp.callback(item.C)
					}
				}
			}
		}
	}()
}

func (rp *ResultPuller) pullResults() ([]resultsItem, error) {
	rp.cursorMu.Lock()
	since := rp.cursor
	rp.cursorMu.Unlock()

	url := fmt.Sprintf("%s/results?since=%d&limit=10", rp.baseURL, since)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Auth-Key", rp.secret)

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
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var data resultsResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode results response: %w", err)
	}

	if len(data.Items) > 0 {
		rp.cursorMu.Lock()
		rp.cursor = data.Cursor
		rp.cursorMu.Unlock()
	}

	return data.Items, nil
}

func (rp *ResultPuller) Stop() {
	close(rp.stopChan)
}
