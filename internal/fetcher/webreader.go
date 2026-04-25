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

// webReaderResult web_reader 返回的 JSON 结构
type webReaderResult struct {
	Title   string `json:"title"`
	Content string `json:"content"`
	URL     string `json:"url"`
}

// fetchViaWebReader 通过智谱 web_reader MCP 获取页面内容
func fetchViaWebReader(ctx context.Context, url string) (*PageInfo, error) {
	// Step 1: Initialize（获取 session ID）
	sessionID, err := mcpInitialize(ctx)
	if err != nil {
		return nil, fmt.Errorf("MCP initialize failed: %w", err)
	}

	// Step 2: Send initialized notification
	mcpNotify(ctx, sessionID)

	// Step 3: Call webReader tool
	resp, err := mcpToolCall(ctx, sessionID, "webReader", map[string]interface{}{
		"url": url,
	})
	if err != nil {
		return nil, fmt.Errorf("MCP tools/call failed: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("MCP error: %s", resp.Error.Message)
	}

	// 提取返回的文本
	var text string
	for _, c := range resp.Result.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}

	if text == "" {
		return nil, fmt.Errorf("no content returned from web_reader")
	}

	// web_reader 返回的是 JSON 字符串，需要解析
	return parseWebReaderResponse(text)
}

// mcpInitialize 初始化 MCP 连接，返回 session ID
func mcpInitialize(ctx context.Context) (string, error) {
	body, _ := json.Marshal(mcpRequest{
		JSONRPC: "2.0",
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]interface{}{
				"name":    "knowly",
				"version": "1.0",
			},
		},
		ID: 1,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", webReaderEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+webReaderAPIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// 提取 session ID
	sessionID := resp.Header.Get("mcp-session-id")

	return sessionID, nil
}

// mcpNotify 发送 MCP 通知（无需解析响应）
func mcpNotify(ctx context.Context, sessionID string) {
	body, _ := json.Marshal(mcpRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	req, err := http.NewRequestWithContext(ctx, "POST", webReaderEndpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+webReaderAPIKey)
	if sessionID != "" {
		req.Header.Set("mcp-session-id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// mcpToolCall 调用 MCP 工具
func mcpToolCall(ctx context.Context, sessionID, toolName string, arguments map[string]interface{}) (*mcpResponse, error) {
	body, _ := json.Marshal(mcpRequest{
		JSONRPC: "2.0",
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      toolName,
			"arguments": arguments,
		},
		ID: 2,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", webReaderEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+webReaderAPIKey)
	if sessionID != "" {
		req.Header.Set("mcp-session-id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, err
	}

	// 解析 SSE 响应，提取 data 行
	respText := extractSSEData(string(respBody))

	var mcpResp mcpResponse
	if err := json.Unmarshal([]byte(respText), &mcpResp); err != nil {
		return nil, fmt.Errorf("failed to parse MCP response: %w", err)
	}

	return &mcpResp, nil
}

// extractSSEData 从 SSE 格式中提取最后一个 data 行的 JSON
func extractSSEData(sse string) string {
	var lastData string
	for _, line := range strings.Split(sse, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			lastData = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if lastData != "" {
		return lastData
	}
	return sse
}

// parseWebReaderResponse 解析 web_reader 返回的 JSON 内容
func parseWebReaderResponse(text string) (*PageInfo, error) {
	// web_reader 返回的 text 可能是带引号的 JSON 字符串
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "\"")
	// 处理转义字符
	text = strings.ReplaceAll(text, `\"`, `"`)
	text = strings.ReplaceAll(text, `\\`, `\`)

	var result webReaderResult
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		// 如果 JSON 解析失败，当作纯文本处理
		return extractFromMarkdown(text), nil
	}

	// 检查是否有错误
	if result.Content == "" && result.Title == "" {
		return nil, fmt.Errorf("web_reader returned empty content")
	}

	content := result.Content
	runes := []rune(content)
	if len(runes) > 5000 {
		content = string(runes[:5000]) + "\n\n[内容已截断]"
	}

	title := result.Title
	if len(title) > 200 {
		title = title[:197] + "..."
	}

	return &PageInfo{
		Title:   title,
		Content: content,
	}, nil
}

// extractFromMarkdown 从 markdown 文本中提取标题和正文（备用）
func extractFromMarkdown(markdown string) *PageInfo {
	lines := strings.Split(markdown, "\n")
	var title string
	var contentLines []string
	titleFound := false

	for _, line := range lines {
		if !titleFound && (strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ")) {
			title = strings.TrimSpace(strings.TrimLeft(line, "# "))
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
