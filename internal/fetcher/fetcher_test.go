package fetcher

import (
	"strings"
	"testing"
)

func TestExtractURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple URL",
			input:    "Check this out: https://example.com",
			expected: "https://example.com",
		},
		{
			name:     "URL at start",
			input:    "https://github.com/yuanguangshan/knowly",
			expected: "https://github.com/yuanguangshan/knowly",
		},
		{
			name:     "HTTP URL",
			input:    "http://example.com/test",
			expected: "http://example.com/test",
		},
		{
			name:     "no URL",
			input:    "This is just text",
			expected: "",
		},
		{
			name:     "multiple URLs",
			input:    "First: https://first.com Second: https://second.com",
			expected: "https://first.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractURL(tt.input)
			if result != tt.expected {
				t.Errorf("ExtractURL() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestIsURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid HTTPS URL",
			input:    "https://example.com",
			expected: true,
		},
		{
			name:     "valid HTTP URL",
			input:    "http://example.com",
			expected: true,
		},
		{
			name:     "URL with path",
			input:    "https://github.com/yuanguangshan/knowly",
			expected: true,
		},
		{
			name:     "plain text",
			input:    "This is just text",
			expected: false,
		},
		{
			name:     "URL with spaces",
			input:    "https://example.com with text",
			expected: false, // 现在要求完全匹配 URL，带其他文本应该返回 false
		},
		{
			name:     "too long to be URL",
			input:    "https://example.com/" + strings.Repeat("a", 2001),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsURL(tt.input)
			if result != tt.expected {
				t.Errorf("IsURL() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractTitle(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple title",
			input:    `<html><head><title>Test Title</title></head></html>`,
			expected: "Test Title",
		},
		{
			name:     "title with attributes",
			input:    `<html><head><title id="main">Test Title</title></head></html>`,
			expected: "Test Title",
		},
		{
			name:     "title with extra whitespace",
			input:    `<html><head><title>  Test   Title  </title></head></html>`,
			expected: "Test Title",
		},
		{
			name:     "no title",
			input:    `<html><head></head></html>`,
			expected: "",
		},
		{
			name:     "title with newlines",
			input:    "<html><head><title>Line1\nLine2</title></head></html>",
			expected: "Line1 Line2", // 换行符应该被替换为空格
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTitle(tt.input)
			if result != tt.expected {
				t.Errorf("extractTitle() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractContent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string // 验证结果中包含此字符串
	}{
		{
			name:     "article tag",
			input:    `<html><body><article><p>Article content here</p></article></body></html>`,
			contains: "Article content here",
		},
		{
			name:     "main tag",
			input:    `<html><body><main><p>Main content here</p></main></body></html>`,
			contains: "Main content here",
		},
		{
			name:     "content div",
			input:    `<html><body><div class="post-content"><p>Post content</p></div></body></html>`,
			contains: "Post content",
		},
		{
			name:     "strips scripts and styles",
			input:    `<html><body><article><script>alert('x')</script><p>Real content</p><style>.x{color:red}</style></article></body></html>`,
			contains: "Real content",
		},
		{
			name:     "fallback to body",
			input:    `<html><body><p>Body content</p></body></html>`,
			contains: "Body content",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractContent(tt.input)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("extractContent() = %q, want it to contain %q", result, tt.contains)
			}
		})
	}
}

func TestCleanHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
		excludes string
	}{
		{
			name:     "removes script tags",
			input:    `<p>Hello</p><script>var x = 1;</script><p>World</p>`,
			contains: "Hello",
			excludes: "var x",
		},
		{
			name:     "removes style tags",
			input:    `<p>Hello</p><style>.x { color: red; }</style><p>World</p>`,
			contains: "Hello",
			excludes: "color",
		},
		{
			name:     "decodes HTML entities",
			input:    `<p>Tom &amp; Jerry &lt;3&gt;</p>`,
			contains: "Tom & Jerry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanHTML(tt.input)
			if !strings.Contains(result, tt.contains) {
				t.Errorf("cleanHTML() = %q, want it to contain %q", result, tt.contains)
			}
			if tt.excludes != "" && strings.Contains(result, tt.excludes) {
				t.Errorf("cleanHTML() = %q, should NOT contain %q", result, tt.excludes)
			}
		})
	}
}

func TestWechatExtraction(t *testing.T) {
	wechatHTML := `
		<script>
			window.cgiDataNew = {
				title: JsDecode('WeChat Title'),
				content_noencode: JsDecode('\x3cp\x3eHello WeChat! This is a long enough content to pass the 100 characters threshold that we set in the extractContent function for the WeChat specific extraction logic. Repeating to make it longer: Hello WeChat! This is a long enough content to pass the 100 characters threshold.\x3c/p\x3e')
			};
		</script>
	`
	t.Run("wechat title", func(t *testing.T) {
		result := extractTitle(wechatHTML)
		if result != "WeChat Title" {
			t.Errorf("extractTitle() = %v, want WeChat Title", result)
		}
	})

	t.Run("wechat content", func(t *testing.T) {
		result := extractContent(wechatHTML)
		if !strings.Contains(result, "Hello WeChat") {
			t.Errorf("extractContent() = %v, want it to contain Hello WeChat", result)
		}
	})
}

func TestIsZhihuURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"zhihu question", "https://www.zhihu.com/question/123/answer/456", true},
		{"zhihu zhuanlan", "https://zhuanlan.zhihu.com/p/123456", true},
		{"zhihu column", "https://www.zhihu.com/column/test", true},
		{"weixin", "https://mp.weixin.qq.com/s/xxx", false},
		{"github", "https://github.com/test/repo", false},
		{"bilibili", "https://www.bilibili.com/video/xxx", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isZhihuURL(tt.url); got != tt.want {
				t.Errorf("isZhihuURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestExtractFromMarkdown(t *testing.T) {
	markdown := "# 如何学习 Go 语言\n\nGo 语言是一门现代化的编程语言。\n\n## 安装\n\n首先下载 Go...\n\n## 总结\n\n开始学习吧。"

	info := extractFromMarkdown(markdown)

	if info.Title != "如何学习 Go 语言" {
		t.Errorf("Title = %q, want %q", info.Title, "如何学习 Go 语言")
	}
	if !strings.Contains(info.Content, "Go 语言是一门现代化的编程语言") {
		t.Errorf("Content should contain body text")
	}
	if strings.Contains(info.Content, "# 如何学习 Go 语言") {
		t.Errorf("Content should not contain the title heading")
	}
}
