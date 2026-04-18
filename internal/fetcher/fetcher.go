package fetcher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// URLRegex 匹配 HTTP/HTTPS URL
var URLRegex = regexp.MustCompile(`https?://[^\s]+`)

// 包级别正则，避免重复编译
var (
	titleRegex      = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	whitespaceRegex = regexp.MustCompile(`\s+`)
)

// FetchTitle 从 URL 抓取页面标题
func FetchTitle(ctx context.Context, url string) (string, error) {
	// 创建 HTTP 客户端
	// 超时控制完全由调用方传入的 context 负责
	client := &http.Client{
		// 不跟随重定向
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	// 设置 User-Agent，避免被某些网站拒绝
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch: %w", err)
	}
	defer resp.Body.Close()

	// 只处理成功响应
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// 限制读取大小，避免处理过大的页面
	limitedReader := io.LimitReader(resp.Body, 1024*1024) // 1MB
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", fmt.Errorf("failed to read body: %w", err)
	}

	// 提取标题
	title := extractTitle(string(body))
	if title == "" {
		return "", fmt.Errorf("no title found")
	}

	return title, nil
}

// extractTitle 从 HTML 中提取标题
func extractTitle(html string) string {
	matches := titleRegex.FindStringSubmatch(html)
	if len(matches) < 2 {
		return ""
	}

	title := strings.TrimSpace(matches[1])
	// 移除 HTML 实体和多余空白（包括换行符）
	title = whitespaceRegex.ReplaceAllString(title, " ")
	title = strings.TrimSpace(title)

	// 限制标题长度
	if len(title) > 100 {
		title = title[:97] + "..."
	}

	return title
}

// ExtractURL 从文本中提取第一个 URL
func ExtractURL(text string) string {
	matches := URLRegex.FindString(text)
	return matches
}

// IsURL 检查文本是否是 URL
func IsURL(text string) bool {
	trimmed := strings.TrimSpace(text)
	return URLRegex.MatchString(trimmed) && len(trimmed) < 200
}
