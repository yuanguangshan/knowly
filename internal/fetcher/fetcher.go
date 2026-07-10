package fetcher

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/yuanguangshan/knowly/internal/retry"
)

// URLRegex 匹配 HTTP/HTTPS URL
var URLRegex = regexp.MustCompile(`https?://[^\s]+`)

var knasyncEnabled bool

// SetKnasyncEnabled 设置 knasync 是否启用
func SetKnasyncEnabled(enabled bool) {
	knasyncEnabled = enabled
}

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

	// 微信专项提取正则
	wechatTitleRegex   = regexp.MustCompile(`(?is)title:\s*JsDecode\(['"](.*?)['"]\)`)
	wechatContentRegex = regexp.MustCompile(`(?is)content_noencode:\s*JsDecode\(['"](.*?)['"]\)`)
	wechatHexRegex     = regexp.MustCompile(`\\x([a-fA-F0-9]{2})`)

	// appmsg_type 9 帖子图的 content_noencode 是直接字符串赋值，没有 JsDecode 包装
	wechatContentDirectRegex = regexp.MustCompile(`(?is)content_noencode:\s*'(.*?)',\s`)

	// 微信 DOM 提取：#js_content 容器（仅用于定位起始标签）
	jsContentStartRegex = regexp.MustCompile(`(?i)<div[^>]*id=["']js_content["'][^>]*>`)

	// 微信环境检测页面（返回 200 但实际不允许外部访问）
	wechatEnvCheckRegex = regexp.MustCompile(`(?i)请在微信客户端打开|请在微信内打开|此内容被投诉|环境异常|访问过于频繁|请完成安全验证`)

	// cleanHTML 正则（包级别，避免每次调用重复编译）
	brRegex      = regexp.MustCompile(`(?i)<br\s*/?\s*>`)
	closePTag    = regexp.MustCompile(`(?i)</p>`)
	closeDivTag  = regexp.MustCompile(`(?i)</div>`)
	closeLITag   = regexp.MustCompile(`(?i)</li>`)
	openHTag     = regexp.MustCompile(`(?i)<h[1-6][^>]*>`)
	closeHTag    = regexp.MustCompile(`(?i)</h[1-6]>`)
	spaceNotNlRe = regexp.MustCompile(`[^\S\n]+`)
)

// PageInfo 包含页面标题和正文内容
type PageInfo struct {
	Title   string
	Content string
}

// isWeChatURL 检查 URL 是否为微信公众号文章
func isWeChatURL(url string) bool {
	return strings.Contains(strings.ToLower(url), "mp.weixin.qq.com") ||
		strings.Contains(strings.ToLower(url), "weixin.qq.com/s/")
}

// FetchPage 从 URL 抓取页面标题和正文内容
func FetchPage(ctx context.Context, url string) (*PageInfo, error) {
	// 知乎链接：只走 knasync（Chrome 扩展），不走直连（知乎反爬 403）
	if isZhihuURL(url) {
		if knasyncEnabled {
			knCtx, knCancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := SubmitToKnasync(knCtx, url)
			knCancel()
			if err != nil {
				log.Printf("[WARN] failed to submit zhihu link to knasync: %v", err)
			} else {
				log.Printf("[INFO] zhihu link submitted to knasync: %s", url)
			}
		} else {
			log.Printf("[WARN] zhihu link skipped: knasync not enabled, direct fetch blocked by 403: %s", url)
		}
		// 无论 knasync 是否启用，知乎链接都不走直连
		return nil, nil
	}

	body, err := fetchHTML(ctx, url)

	// 微信降级：直连失败时，尝试 web_reader MCP（智谱 API，带真实浏览器渲染）
	if err != nil && isWeChatURL(url) && webReaderAPIKey != "" {
		log.Printf("[WARN] WeChat direct fetch failed, falling back to web_reader MCP: %v", err)
		info, wbErr := fetchViaWebReader(ctx, url)
		if wbErr == nil && info != nil {
			log.Printf("[INFO] WeChat web_reader fallback succeeded: %s", url)
			return info, nil
		}
		log.Printf("[WARN] WeChat web_reader fallback also failed: %v", wbErr)
		return nil, err // 返回原始错误
	}
	if err != nil {
		return nil, err
	}

	html := string(body)

	title := extractTitle(html)
	content := extractContent(html)

	// 微信降级：内容为空/命中环境检测页时，尝试 web_reader MCP
	if isWeChatURL(url) && webReaderAPIKey != "" && isWeChatContentEmpty(content) {
		log.Printf("[WARN] WeChat content empty/blocked, falling back to web_reader MCP: %s", url)
		info, wbErr := fetchViaWebReader(ctx, url)
		if wbErr == nil && info != nil && !isWeChatContentEmpty(info.Content) {
			log.Printf("[INFO] WeChat web_reader fallback succeeded (content was empty/blocked): %s", url)
			return info, nil
		}
		log.Printf("[WARN] WeChat web_reader fallback also returned empty content: %s", url)
	}

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

// fetchHTML 获取页面 HTML 内容（最多重试 2 次）
func fetchHTML(ctx context.Context, url string) ([]byte, error) {
	// 创建 HTTP 客户端（整个重试周期复用同一个 client）
	// 强制 HTTP/1.1 + 禁用连接复用，避免微信服务器 HTTP/2 unexpected EOF
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSNextProto:      make(map[string]func(string, *tls.Conn) http.RoundTripper),
		},
	}

	var body []byte
	var attempt int
	// 为整个抓取（含重试）创建独立的超时控制
	// 剥离调用方过短的 deadline（15s/30s），但保留取消信号
	fetchCtx, fetchCancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer fetchCancel()

	err := retry.Do(fetchCtx, retry.Config{
		MaxRetries: 2,
		BaseDelay:  2 * time.Second,
		MaxDelay:   10 * time.Second,
	}, func() error {
		attempt++
		if attempt > 1 {
			log.Printf("[WARN] Fetch failed (attempt %d/3), retrying: %s", attempt, url)
		}
		// 每次重试重新创建请求（body 已被消费）
		req, err := http.NewRequestWithContext(fetchCtx, "GET", url, nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		// 设置完整的 User-Agent 和极其逼真的请求头，模拟真实浏览器访问
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
		req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
		req.Header.Set("Upgrade-Insecure-Requests", "1")
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-User", "?1")
		req.Header.Set("Cache-Control", "max-age=0")

		// 针对严格的反爬虫平台：把 Referer 设置为它自己，假装是从站内点击进去的
		if isWeChatURL(url) {
			req.Header.Set("Referer", "https://mp.weixin.qq.com/")
		} else {
			req.Header.Set("Referer", url)
		}

		// 发送请求
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to fetch: %w", err)
		}
		defer resp.Body.Close()

		// 处理成功响应（包括 2xx）
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}

		// 限制读取大小，避免处理过大的页面
		limitedReader := io.LimitReader(resp.Body, 2*1024*1024) // 2MB
		body, err = io.ReadAll(limitedReader)
		if err != nil {
			return fmt.Errorf("failed to read body: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return body, nil
}

// extractTitle 从 HTML 中提取标题
func extractTitle(html string) string {
	matches := titleRegex.FindStringSubmatch(html)
	var title string
	if len(matches) >= 2 {
		title = matches[1]
	}

	// 微信专项：即使有了标题也检查下，微信的 <title> 经常是空的
	if strings.TrimSpace(title) == "" {
		if wm := wechatTitleRegex.FindStringSubmatch(html); len(wm) >= 2 {
			title = decodeWechatHex(wm[1])
		}
	}

	if title == "" {
		return ""
	}

	title = strings.TrimSpace(title)
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
	// 1. 微信 DOM 提取：#js_content 容器（计数法处理嵌套 div，更稳定）
	if content := extractWeChatJSContent(html); len(content) > 100 {
		return content
	}

	// 2. 尝试微信 JS 变量提取（备用方案，应对微信改版导致 DOM 结构变化）
	if wm := wechatContentRegex.FindStringSubmatch(html); len(wm) >= 2 {
		decoded := decodeWechatHex(wm[1])
		if len(decoded) > 100 {
			return cleanHTML(decoded)
		}
	}

	// 2.5 appmsg_type 9 帖子图：content_noencode 是直接字符串赋值（无 JsDecode 包装）
	if wm := wechatContentDirectRegex.FindStringSubmatch(html); len(wm) >= 2 {
		decoded := decodeWechatHex(wm[1])
		if len(decoded) > 100 {
			return cleanHTML(decoded)
		}
	}

	// 3. 尝试从语义化标签中提取正文
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

	// 限制内容长度（保留前 100000 个字符）
	runes := []rune(text)
	if len(runes) > 100000 {
		text = string(runes[:100000]) + "\n\n[内容已截断]"
	}

	return text
}

// cleanHTML 清理 HTML 标签，提取纯文本
func cleanHTML(html string) string {
	// 规范化换行符
	html = strings.ReplaceAll(html, "\r\n", "\n")
	html = strings.ReplaceAll(html, "\r", "\n")
	
	// 1. 移除 script, style, comment
	text := scriptRegex.ReplaceAllString(html, "")
	text = styleRegex.ReplaceAllString(text, "")
	text = commentRegex.ReplaceAllString(text, "")

	// 2. 段落和换行标签转换为换行符
	text = brRegex.ReplaceAllString(text, "\n")
	text = closePTag.ReplaceAllString(text, "\n\n")
	text = closeDivTag.ReplaceAllString(text, "\n")
	text = closeLITag.ReplaceAllString(text, "\n")
	text = openHTag.ReplaceAllString(text, "\n\n")
	text = closeHTag.ReplaceAllString(text, "\n\n")

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
	text = spaceNotNlRe.ReplaceAllString(text, " ")
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

// decodeWechatHex 解码微信 JS 中的 \xNN 转义字符
func decodeWechatHex(s string) string {
	return wechatHexRegex.ReplaceAllStringFunc(s, func(m string) string {
		var hexVal rune
		fmt.Sscanf(m[2:], "%x", &hexVal)
		return string(hexVal)
	})
}

// extractWeChatJSContent 通过计数法提取微信 #js_content 容器的完整内容
// 正则无法正确处理嵌套 div，所以采用标签计数方式定位完整的闭合标签
func extractWeChatJSContent(html string) string {
	loc := jsContentStartRegex.FindStringIndex(html)
	if loc == nil {
		return ""
	}

	start := loc[1] // 起始标签之后的位置
	depth := 1
	i := start
	n := len(html)

	for i < n && depth > 0 {
		nextDiv := strings.Index(html[i:], "<div")
		nextEnd := strings.Index(html[i:], "</div>")

		if nextEnd == -1 {
			break // 没有更多闭合标签，到末尾
		}

		if nextDiv != -1 && nextDiv < nextEnd {
			// 检查是新的开标签（<div 后面必须是空格、>、或换行，排除 <divider 等）
			afterDiv := i + nextDiv + 4
			if afterDiv >= n {
				i = i + nextEnd + 6
				depth--
				continue
			}
			ch := html[afterDiv]
			if ch == ' ' || ch == '>' || ch == '\n' || ch == '\r' || ch == '\t' {
				depth++
			}
			i = i + nextDiv + 4
		} else {
			depth--
			if depth == 0 {
				// 找到匹配的闭合标签
				return cleanHTML(html[start : i+nextEnd])
			}
			i = i + nextEnd + 6
		}
	}

	// 没找到匹配的闭合标签，回退到整个剩余内容
	return cleanHTML(html[start:])
}

// isWeChatContentEmpty 检测微信内容是否为空或命中环境检测页
func isWeChatContentEmpty(content string) bool {
	if len(content) < 50 {
		return true
	}
	if wechatEnvCheckRegex.MatchString(content) {
		return true
	}
	return false
}

// ExtractURL 从文本中提取第一个 URL
func ExtractURL(text string) string {
	matches := URLRegex.FindString(text)
	return matches
}

// IsPDFURL 通过 HEAD 请求检测 URL 是否返回 PDF（Content-Type 包含 application/pdf）
func IsPDFURL(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36")

	// 强制 HTTP/1.1 + 禁用连接复用，避免 HTTP/2 protocol error 和 idle channel 乱响应
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSNextProto:      make(map[string]func(string, *tls.Conn) http.RoundTripper),
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		// HEAD 失败时 fallback 到 URL 后缀判断
		return strings.HasSuffix(strings.ToLower(strings.SplitN(url, "?", 2)[0]), ".pdf")
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "application/pdf")
}

// FetchPDF 下载 PDF 文件二进制内容（限制 10MB）
func FetchPDF(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch PDF: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	limitedReader := io.LimitReader(resp.Body, 10*1024*1024) // 10MB
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read PDF: %w", err)
	}

	return body, nil
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
