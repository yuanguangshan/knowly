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

	// 内容提取相关正则
	scriptRegex  = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleRegex   = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	commentRegex = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagRegex     = regexp.MustCompile(`<[^>]+>`)
	entityRegex  = regexp.MustCompile(`&[a-zA-Z]+;|&#\d+;|&#x[0-9a-fA-F]+;`)

	// 正文区域优先提取（按优先级排列）
	articleRegex = regexp.MustCompile(`(?is)<article[^>]*>(.*?)</article>`)
	mainRegex    = regexp.MustCompile(`(?is)<main[^>]*>(.*?)</main>`)
	contentRegex = regexp.MustCompile(`(?is)<div[^>]*(?:class|id)\s*=\s*["'][^"']*(?:content|article|post|entry|text|body)[^"']*["'][^>]*>(.*?)</div>`)
)

// PageInfo 包含页面标题和正文内容
type PageInfo struct {
	Title   string
	Content string
}

// FetchPage 从 URL 抓取页面标题和正文内容
func FetchPage(ctx context.Context, url string) (*PageInfo, error) {
	body, err := fetchHTML(ctx, url)
	if err != nil {
		return nil, err
	}

	html := string(body)

	title := extractTitle(html)
	content := extractContent(html)

	if title == "" && content == "" {
		return nil, fmt.Errorf("no title or content found")
	}

	return &PageInfo{
		Title:   title,
		Content: content,
	}, nil
}

// FetchTitle 从 URL 抓取页面标题（保留向后兼容）
func FetchTitle(ctx context.Context, url string) (string, error) {
	info, err := FetchPage(ctx, url)
	if err != nil {
		return "", err
	}
	if info.Title == "" {
		return "", fmt.Errorf("no title found")
	}
	return info.Title, nil
}

// fetchHTML 获取页面 HTML 内容
func fetchHTML(ctx context.Context, url string) ([]byte, error) {
	// 创建 HTTP 客户端
	// 允许跟随重定向（最多 10 次，Go 默认行为）
	client := &http.Client{}

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 设置完整的 User-Agent 和极其逼真的请求头，模拟真实浏览器访问
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Cache-Control", "max-age=0")

	// 针对严格的反爬虫平台：把 Referer 设置为它自己，假装是从站内点击进去的
	req.Header.Set("Referer", url)

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch: %w", err)
	}
	defer resp.Body.Close()

	// 处理成功响应（包括 2xx）
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// 限制读取大小，避免处理过大的页面
	limitedReader := io.LimitReader(resp.Body, 2*1024*1024) // 2MB
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	return body, nil
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
	if len(title) > 200 {
		title = title[:197] + "..."
	}

	return title
}

// extractContent 从 HTML 中提取正文内容
func extractContent(html string) string {
	// 尝试从语义化标签中提取正文
	var bodyHTML string

	// 按优先级尝试提取正文区域
	if matches := articleRegex.FindStringSubmatch(html); len(matches) >= 2 {
		bodyHTML = matches[1]
	} else if matches := mainRegex.FindStringSubmatch(html); len(matches) >= 2 {
		bodyHTML = matches[1]
	} else if matches := contentRegex.FindStringSubmatch(html); len(matches) >= 2 {
		bodyHTML = matches[1]
	} else {
		// 回退：提取 <body> 内容
		bodyRegex := regexp.MustCompile(`(?is)<body[^>]*>(.*?)</body>`)
		if matches := bodyRegex.FindStringSubmatch(html); len(matches) >= 2 {
			bodyHTML = matches[1]
		} else {
			bodyHTML = html
		}
	}

	// 清理 HTML
	text := cleanHTML(bodyHTML)

	// 限制内容长度（保留前 5000 个字符）
	runes := []rune(text)
	if len(runes) > 5000 {
		text = string(runes[:5000]) + "\n\n[内容已截断]"
	}

	return text
}

// cleanHTML 清理 HTML 标签，提取纯文本
func cleanHTML(html string) string {
	// 1. 移除 script, style, comment
	text := scriptRegex.ReplaceAllString(html, "")
	text = styleRegex.ReplaceAllString(text, "")
	text = commentRegex.ReplaceAllString(text, "")

	// 2. 段落和换行标签转换为换行符
	text = regexp.MustCompile(`(?i)<br\s*/?\s*>`).ReplaceAllString(text, "\n")
	text = regexp.MustCompile(`(?i)</p>`).ReplaceAllString(text, "\n\n")
	text = regexp.MustCompile(`(?i)</div>`).ReplaceAllString(text, "\n")
	text = regexp.MustCompile(`(?i)</li>`).ReplaceAllString(text, "\n")
	text = regexp.MustCompile(`(?i)<h[1-6][^>]*>`).ReplaceAllString(text, "\n\n")
	text = regexp.MustCompile(`(?i)</h[1-6]>`).ReplaceAllString(text, "\n\n")

	// 3. 移除所有其他 HTML 标签
	text = tagRegex.ReplaceAllString(text, "")

	// 4. 处理常见 HTML 实体
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = entityRegex.ReplaceAllString(text, "")

	// 5. 清理多余空白
	// 先把连续空格（非换行）压缩
	text = regexp.MustCompile(`[^\S\n]+`).ReplaceAllString(text, " ")
	// 清理每行首尾空白
	lines := strings.Split(text, "\n")
	var cleaned []string
	emptyCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			emptyCount++
			if emptyCount <= 2 {
				cleaned = append(cleaned, "")
			}
		} else {
			emptyCount = 0
			cleaned = append(cleaned, line)
		}
	}

	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

// ExtractURL 从文本中提取第一个 URL
func ExtractURL(text string) string {
	matches := URLRegex.FindString(text)
	return matches
}

// IsURL 检查文本本身是否是一个纯粹的 URL
func IsURL(text string) bool {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) > 2000 {
		return false
	}
	matched := URLRegex.FindString(trimmed)
	return matched != "" && matched == trimmed
}
