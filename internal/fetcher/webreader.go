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

const webReaderEndpoint = "https://open.bigmodel.cn/api/mcp/web_reader/mcp"

var webReaderAPIKey string

// SetWebReaderAPIKey 设置 web_reader API Key
func SetWebReaderAPIKey(key string) {
	webReaderAPIKey = key
}

// isZhihuURL 检查 URL 是否来自知乎
func isZhihuURL(url string) bool {
	u := strings.ToLower(url)
	return strings.Contains(u, "zhihu.com")
}

// mcpRequest MCP JSON-RPC 请求
type mcpRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      int         `json:"id,omitempty"`
}

// mcpResponse MCP JSON-RPC 响应
type mcpResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// fetchViaWebReader 通过智谱 web_reader MCP 获取页面内容
func fetchViaWebReader(ctx context.Context, url string) (*PageInfo, error) {
	// Step 1: Initialize
	if _, err := mcpCall(ctx, mcpRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "knowly",
				"version": "1.0",
			},
		},
		ID: 1,
	}); err != nil {
		return nil, fmt.Errorf("MCP initialize failed: %w", err)
	}

	// Step 2: Send initialized notification
	mcpCall(ctx, mcpRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	// Step 3: Call webReader tool
	resp, err := mcpCall(ctx, mcpRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name": "webReader",
			"arguments": map[string]interface{}{
				"url":           url,
				"return_format": "markdown",
			},
		},
		ID: 2,
	})
	if err != nil {
		return nil, fmt.Errorf("MCP tools/call failed: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("MCP error: %s", resp.Error.Message)
	}

	// 提取返回的文本内容
	var markdown string
	for _, c := range resp.Result.Content {
		if c.Type == "text" {
			markdown += c.Text
		}
	}

	if markdown == "" {
		return nil, fmt.Errorf("no content returned from web_reader")
	}

	return extractFromMarkdown(markdown), nil
}

// mcpCall 发送 MCP JSON-RPC 请求
func mcpCall(ctx context.Context, req mcpRequest) (*mcpResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", webReaderEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+webReaderAPIKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	// 对于通知（无 ID），无需解析响应
	if req.ID == 0 {
		return nil, nil
	}

	// 处理 SSE 响应格式
	respText := string(respBody)

	// 提取最后一个 data: 行（最终结果）
	if strings.Contains(respText, "data:") {
		var lastData string
		for _, line := range strings.Split(respText, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				lastData = strings.TrimPrefix(line, "data:")
				lastData = strings.TrimSpace(lastData)
			}
		}
		if lastData != "" {
			respText = lastData
		}
	}

	var mcpResp mcpResponse
	if err := json.Unmarshal([]byte(respText), &mcpResp); err != nil {
		return nil, fmt.Errorf("failed to parse MCP response: %w", err)
	}

	return &mcpResp, nil
}

// extractFromMarkdown 从 markdown 文本中提取标题和正文
func extractFromMarkdown(markdown string) *PageInfo {
	lines := strings.Split(markdown, "\n")
	var title string
	var contentLines []string
	titleFound := false

	for _, line := range lines {
		if !titleFound && strings.HasPrefix(line, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			titleFound = true
			continue
		}
		contentLines = append(contentLines, line)
	}

	content := strings.TrimSpace(strings.Join(contentLines, "\n"))

	// 限制内容长度
	runes := []rune(content)
	if len(runes) > 5000 {
		content = string(runes[:5000]) + "\n\n[内容已截断]"
	}

	if len(title) > 200 {
		title = title[:197] + "..."
	}

	return &PageInfo{
		Title:   title,
		Content: content,
	}
}
