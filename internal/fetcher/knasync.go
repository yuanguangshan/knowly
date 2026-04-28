package fetcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var (
	knasyncEndpoint string
	knasyncAuthKey  string
)

// SetKnasyncConfig 设置 knasync 配置
func SetKnasyncConfig(endpoint, authKey string) {
	knasyncEndpoint = endpoint
	knasyncAuthKey = authKey
}

// SetKnasyncAuthKey 设置 knasync 认证密钥（向后兼容）
func SetKnasyncAuthKey(key string) {
	knasyncAuthKey = key
}

// knasyncRequest knasync 提交请求
type knasyncRequest struct {
	Content string `json:"content,omitempty"`
	URL     string `json:"url,omitempty"`
}

// SubmitToKnasync 提交链接到 knasync 中继服务
func SubmitToKnasync(ctx context.Context, url string) error {
	if knasyncAuthKey == "" {
		return fmt.Errorf("knasync auth key not configured")
	}
	if knasyncEndpoint == "" {
		return fmt.Errorf("knasync endpoint not configured")
	}

	// 构造完整的端点 URL
	endpoint := knasyncEndpoint
	if !strings.HasSuffix(endpoint, "/") {
		endpoint += "/"
	}
	endpoint += "submit"

	// 构造请求体
	reqBody := knasyncRequest{
		URL: url,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Key", knasyncAuthKey)

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to submit to knasync: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("knasync returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// 解析响应
	respText := string(respBody)
	if !containsOK(respText) {
		return fmt.Errorf("knasync unexpected response: %s", respText)
	}

	return nil
}

// containsOK 检查响应是否包含 OK
func containsOK(text string) bool {
	return len(text) >= 2 && (text[0:2] == "OK" || text[0:2] == "ok")
}
