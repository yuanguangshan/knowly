package relay

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type Puller struct {
	endpoint string
	secret   string
	interval time.Duration
	stopChan chan struct{}
	callback func(string)
}

func NewPuller(endpoint, secret string, interval time.Duration, callback func(string)) *Puller {
	return &Puller{
		endpoint: endpoint + "/pull",
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
		log.Println("[INFO] Relay puller started, endpoint:", p.endpoint)

		for {
			select {
			case <-p.stopChan:
				log.Println("[INFO] Relay puller stopped")
				return
			case <-ticker.C:
				content, err := p.pull()
				if err != nil {
					log.Printf("[DEBUG] Relay pull failed: %v", err)
					continue
				}
				if content != "" {
					log.Printf("[INFO] Relay content received (length: %d)", len(content))
					p.callback(content)
				}
			}
		}
	}()
}

func (p *Puller) pull() (string, error) {
	req, err := http.NewRequest("GET", p.endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Auth-Key", p.secret)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (p *Puller) Stop() {
	close(p.stopChan)
}
