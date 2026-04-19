package publisher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/yuanguangshan/knas/internal/config"
)

// extractTitle 从 Markdown 内容中提取标题（首行 # 标题），若无则用时间戳
func extractTitle(content string) string {
	re := regexp.MustCompile(`(?m)^#\s+(.+)$`)
	matches := re.FindStringSubmatch(content)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
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

// defaultTags 生成默认标签 YYYYMM
func defaultTags() string {
	return time.Now().Format("200601")
}

// PublishBlog 发布博客
func PublishBlog(cfg config.BlogConfig, contentMD string) error {
	title := extractTitle(contentMD)
	plainText := stripMarkdown(contentMD)
	tags := cfg.Tags
	if tags == "" {
		tags = defaultTags()
	}

	body := map[string]any{
		"title":      title,
		"content":    plainText,
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
	plainText := stripMarkdown(contentMD)

	body := map[string]any{
		"title":      title,
		"content":    plainText,
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
}
