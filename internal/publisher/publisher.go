package publisher

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/yuanguangshan/knowly/internal/config"
)

// extractTitle 从 Markdown 内容中提取标题
// 优先级：1. # 标题  2. 第一行有意义的文本
func extractTitle(content string) string {
	lines := strings.Split(content, "\n")

	// 跳过 YAML frontmatter（--- 包围的内容）
	startIdx := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				startIdx = i + 1
				break
			}
		}
	}

	// 查找 # 标题
	re := regexp.MustCompile(`^#\s+(.+)$`)
	for i := startIdx; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		// 跳过空行
		if line == "" {
			continue
		}
		// 跳过纯标签行（如 #202604）
		if regexp.MustCompile(`^#\d+$`).MatchString(line) {
			continue
		}
		// 查找 # 标题
		if matches := re.FindStringSubmatch(line); len(matches) > 1 {
			title := strings.TrimSpace(matches[1])
			if title != "" {
				return title
			}
		}
		// 找到第一个非空行，使用它作为标题
		// 跳过日期行（如 2026-04-24 00:38:47）
		if regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`).MatchString(line) ||
		   regexp.MustCompile(`^\d+分钟阅读`).MatchString(line) {
			continue
		}
		// 使用第一行有意义文本的前 50 个字符作为标题
		runes := []rune(line)
		if len(runes) > 50 {
			return string(runes[:50]) + "..."
		}
		return line
	}

	// 如果还是找不到，使用默认标题
	return time.Now().Format("2006-01-02 15:04:05")
}

// stripMarkdown 去除常见 Markdown 格式，生成纯文本
func stripMarkdown(md string) string {
	text := md
	// 去除标题标记
	text = regexp.MustCompile(`(?m)^#{1,6}\s+`).ReplaceAllString(text, "")
	// 去除粗体/斜体
	text = regexp.MustCompile(`\*{1,3}(.+?)\*{1,3}`).ReplaceAllString(text, "$1")
	text = regexp.MustCompile(`_{1,3}(.+?)_{1,3}`).ReplaceAllString(text, "$1")
	// 去除链接，保留文本
	text = regexp.MustCompile(`\[(.+?)\]\(.+?\)`).ReplaceAllString(text, "$1")
	// 去除图片
	text = regexp.MustCompile(`!\[.*?\]\(.+?\)`).ReplaceAllString(text, "")
	// 去除行内代码
	text = regexp.MustCompile("`(.+?)`").ReplaceAllString(text, "$1")
	// 去除代码块
	text = regexp.MustCompile("(?s)```.*?```").ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// formatForKindle 将 Markdown 内容转换为适合 Kindle 阅读的纯文本格式
// Kindle 对格式支持有限，使用简洁清晰的排版
func formatForKindle(md string) string {
	lines := strings.Split(md, "\n")
	var result strings.Builder
	inCodeBlock := false

	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		stripped := strings.TrimSpace(trimmed)

		// 跳过 YAML frontmatter
		if i == 0 && stripped == "---" {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "---" {
					i = j
					break
				}
			}
			continue
		}

		// 处理代码块
		if strings.HasPrefix(stripped, "```") {
			inCodeBlock = !inCodeBlock
			if inCodeBlock {
				result.WriteString("\n[代码块]\n")
			} else {
				result.WriteString("[代码块结束]\n\n")
			}
			continue
		}
		if inCodeBlock {
			result.WriteString(trimmed)
			result.WriteString("\n")
			continue
		}

		// 处理标题
		if strings.HasPrefix(stripped, "#") {
			level := 0
			for _, c := range stripped {
				if c == '#' {
					level++
				} else {
					break
				}
			}
			title := strings.TrimSpace(stripped[level:])
			if title != "" {
				result.WriteString("\n")
				// 根据级别用不同方式突出显示
				if level == 1 {
					// 一级标题：全大写 + 粗体 + 更多空行
					result.WriteString("\n*** ")
					result.WriteString(strings.ToUpper(title))
					result.WriteString(" ***\n\n")
				} else if level == 2 {
					// 二级标题：首字母大写 + 粗体
					result.WriteString("*** ")
					result.WriteString(title)
					result.WriteString(" ***\n\n")
				} else {
					// 三级及以下：正常显示
					result.WriteString(title)
					result.WriteString("\n\n")
				}
			}
			continue
		}

		// 处理引用块
		if strings.HasPrefix(stripped, ">") {
			quoteText := strings.TrimSpace(stripped[1:])
			result.WriteString("  | ")
			result.WriteString(quoteText)
			result.WriteString("\n")
			continue
		}

		// 处理无序列表
		if strings.HasPrefix(stripped, "-") || strings.HasPrefix(stripped, "*") {
			listText := strings.TrimSpace(stripped[1:])
			result.WriteString("  * ")
			result.WriteString(listText)
			result.WriteString("\n")
			continue
		}

		// 处理有序列表
		if matched, _ := regexp.MatchString(`^\d+\.\s`, stripped); matched {
			result.WriteString("  ")
			result.WriteString(stripped)
			result.WriteString("\n")
			continue
		}

		// 处理水平分隔线
		if stripped == "---" || stripped == "***" {
			result.WriteString("\n---\n\n")
			continue
		}

		// 处理空行
		if stripped == "" {
			result.WriteString("\n")
			continue
		}

		// 处理普通段落
		processed := trimmed
		// 粗体用 *** 包围（Kindle 支持）
		processed = regexp.MustCompile(`\*\*(.+?)\*\*`).ReplaceAllString(processed, "***$1***")
		processed = regexp.MustCompile(`__(.+?)__`).ReplaceAllString(processed, "***$1***")
		// 斜体用 _ 包围（Kindle 支持）
		processed = regexp.MustCompile(`(?m)^\*(.+?)\*$`).ReplaceAllString(processed, "_$1_")
		// 行内代码
		processed = regexp.MustCompile("`(.+?)`").ReplaceAllString(processed, "'$1'")
		// 链接
		processed = regexp.MustCompile(`\[(.+?)\]\(.+?\)`).ReplaceAllString(processed, "$1")

		result.WriteString(processed)
		result.WriteString("\n")
	}

	return strings.TrimSpace(result.String())
}

// defaultTags 生成默认标签 YYYYMM
func defaultTags() string {
	return time.Now().Format("200601")
}

// PublishBlog 发布博客
func PublishBlog(cfg config.BlogConfig, contentMD string) error {
	title := extractTitle(contentMD)
	formattedText := stripMarkdown(contentMD)
	tags := cfg.Tags
	if tags == "" {
		tags = defaultTags()
	}

	body := map[string]any{
		"title":      title,
		"content":    formattedText,
		"content_md": contentMD,
		"tags":       tags,
		"targets":    []string{"blog"},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal blog request: %w", err)
	}

	url := cfg.APIURL + "/api/publish"
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create blog request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("blog request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("blog publish failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	log.Printf("[INFO] Blog published: %s", title)
	return nil
}

// PublishPodcast 发布播客
func PublishPodcast(cfg config.PodcastConfig, contentMD string) error {
	title := extractTitle(contentMD)
	formattedText := stripMarkdown(contentMD)

	body := map[string]any{
		"title":      title,
		"content":    formattedText,
		"content_md": contentMD,
		"targets":    []string{"nas"},
		"transform":  "read",
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal podcast request: %w", err)
	}

	url := cfg.APIURL + "/api/publish"
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create podcast request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-ID", cfg.AppID)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("podcast request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("podcast publish failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	log.Printf("[INFO] Podcast queued: %s", title)
	return nil
}

// PublishIMA 保存到 IMA 笔记
func PublishIMA(cfg config.IMAConfig, contentMD string) error {
	body := map[string]any{
		"content":        contentMD,
		"content_format": 1,
		"folder_id":      cfg.FolderID,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal IMA request: %w", err)
	}

	url := cfg.APIURL + "/import_doc"
	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("create IMA request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ima-openapi-clientid", cfg.ClientID)
	req.Header.Set("ima-openapi-apikey", cfg.APIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("IMA request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("IMA publish failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	// 解析响应获取 doc_id
	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err == nil {
		if code, ok := result["code"].(float64); ok && code != 0 {
			return fmt.Errorf("IMA API error (code %v): %v", code, result)
		}
		if docID, ok := result["doc_id"].(string); ok {
			log.Printf("[INFO] IMA note saved: %s", docID)
			return nil
		}
	}

	log.Printf("[INFO] IMA note saved")
	return nil
}

// PublishKindle 发送内容到 Kindle 个人文档服务
func PublishKindle(cfg config.KindleConfig, contentMD string) error {
	formattedText := formatForKindle(contentMD)

	// 用内容前 50 个字符作为标题，去除特殊字符
	titleRunes := []rune(formattedText)
	if len(titleRunes) > 50 {
		titleRunes = titleRunes[:50]
	}
	title := string(titleRunes)
	// 标点符号转为下划线，其他不安全字符去除
	title = regexp.MustCompile(`[，。、；：？！""''【】（）《》—…·,\.;:?!'"()\[\]{}\-]`).ReplaceAllString(title, "_")
	title = regexp.MustCompile(`[\\/*?:"<>|]`).ReplaceAllString(title, "")
	title = regexp.MustCompile(`_+`).ReplaceAllString(title, "_")
	title = strings.Trim(title, "_ ")
	title = strings.TrimSpace(title)
	if title == "" {
		title = time.Now().Format("20060102-150405")
	}
	filename := fmt.Sprintf("雨轩-%s.txt", title)

	// RFC 2231 编码文件名
	encodedFilename := fmt.Sprintf("utf-8''%s", url.PathEscape(filename))

	// RFC 2047 编码 Subject
	subject := mime.BEncoding.Encode("utf-8", strings.TrimSuffix(filename, ".txt"))

	// 构建 MIME 邮件
	boundary := fmt.Sprintf("----=_Part_%d", time.Now().UnixNano())
	var buf bytes.Buffer

	// 邮件头
	fmt.Fprintf(&buf, "From: %s\r\n", cfg.SenderEmail)
	fmt.Fprintf(&buf, "To: %s\r\n", cfg.KindleEmail)
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", boundary)

	// 文本正文部分
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: 7bit\r\n\r\n")
	fmt.Fprintf(&buf, "Sent by knowly.\r\n")

	// 附件部分（application/octet-stream，与 Python 版一致）
	fmt.Fprintf(&buf, "\r\n--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: application/octet-stream; name=\"%s\"\r\n", encodedFilename)
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: base64\r\n")
	fmt.Fprintf(&buf, "Content-Disposition: attachment; filename*=%s\r\n\r\n", encodedFilename)

	// Base64 编码附件内容
	encoded := base64.StdEncoding.EncodeToString([]byte(formattedText))
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		fmt.Fprintf(&buf, "%s\r\n", encoded[i:end])
	}

	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)

	// 通过 SMTP SSL 发送
	addr := fmt.Sprintf("%s:%d", cfg.SMTPServer, cfg.SMTPPort)
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: cfg.SMTPServer})
	if err != nil {
		return fmt.Errorf("kindle TLS connect failed: %w", err)
	}

	client, err := smtp.NewClient(conn, cfg.SMTPServer)
	if err != nil {
		conn.Close()
		return fmt.Errorf("kindle SMTP client failed: %w", err)
	}

	auth := smtp.PlainAuth("", cfg.SenderEmail, cfg.SenderPassword, cfg.SMTPServer)
	if err := client.Auth(auth); err != nil {
		client.Close()
		return fmt.Errorf("kindle SMTP auth failed: %w", err)
	}

	if err := client.Mail(cfg.SenderEmail); err != nil {
		client.Close()
		return fmt.Errorf("kindle SMTP mail failed: %w", err)
	}
	if err := client.Rcpt(cfg.KindleEmail); err != nil {
		client.Close()
		return fmt.Errorf("kindle SMTP rcpt failed: %w", err)
	}

	wc, err := client.Data()
	if err != nil {
		client.Close()
		return fmt.Errorf("kindle SMTP data failed: %w", err)
	}
	if _, err := wc.Write(buf.Bytes()); err != nil {
		wc.Close()
		client.Close()
		return fmt.Errorf("kindle SMTP write failed: %w", err)
	}
	wc.Close()
	client.Quit()

	log.Printf("[INFO] Kindle sent: %s", filename)
	return nil
}

// PublishIfNeeded 根据配置异步发布到所有已启用的渠道
func PublishIfNeeded(cfg *config.Config, content string) {
	if cfg.Blog.Enabled {
		go func() {
			if err := PublishBlog(cfg.Blog, content); err != nil {
				log.Printf("[ERROR] Blog publish failed: %v", err)
			}
		}()
	}

	if cfg.Podcast.Enabled {
		go func() {
			if err := PublishPodcast(cfg.Podcast, content); err != nil {
				log.Printf("[ERROR] Podcast publish failed: %v", err)
			}
		}()
	}

	if cfg.IMA.Enabled && cfg.IMA.ClientID != "" && cfg.IMA.APIKey != "" {
		go func() {
			if err := PublishIMA(cfg.IMA, content); err != nil {
				log.Printf("[ERROR] IMA publish failed: %v", err)
			}
		}()
	}

	if cfg.Kindle.Enabled && cfg.Kindle.SenderEmail != "" && cfg.Kindle.SenderPassword != "" {
		go func() {
			if err := PublishKindle(cfg.Kindle, content); err != nil {
				log.Printf("[ERROR] Kindle publish failed: %v", err)
			}
		}()
	}
}
