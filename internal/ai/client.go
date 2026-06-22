package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yuanguangshan/knowly/internal/retry"
)

// openaiRequest OpenAI Chat Completions API 请求格式
type openaiRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openaiResponse OpenAI Chat Completions API 响应格式
type openaiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// callAPI 发送请求到 OpenAI 兼容的 API 端点（最多重试 1 次）
func (p *Processor) callAPI(ctx context.Context, sysPrompt, userPrompt string) (string, error) {
	reqBody := openaiRequest{
		Model: p.cfg.Model,
		Messages: []chatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	endpoint := strings.TrimRight(p.cfg.Endpoint, "/") + "/chat/completions"

	// 统一 retry：对限流(429)、5xx、网络错误最多重试 2 次；
	// 4xx（鉴权失败/参数错误等）标记为 Permanent，不浪费重试配额。
	var apiResp openaiResponse
	err = retry.Do(ctx, retry.Config{MaxRetries: 2, BaseDelay: time.Second, MaxDelay: 6 * time.Second}, func() error {
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Client-Id", "knowly")
		if p.cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}
	if resp.StatusCode != http.StatusOK {
		// 4xx（非 429）通常为鉴权/参数错误，重试无意义
		return retry.Permanent(fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody)))
	}

	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return fmt.Errorf("no choices in response")
	}
	return nil
	})

	if err != nil {
		log.Printf("[WARN] AI API call failed after retry: %v", err)
		return "", err
	}

	return apiResp.Choices[0].Message.Content, nil
}
