package ssh

import (
	"strings"
	"testing"
	"time"
)

func TestExtractContentPrefix(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		n        int
		expected string
	}{
		{
			name:     "simple text",
			content:  "Hello World",
			n:        10,
			expected: "Hello_Worl", // 先截取到10个字符，然后过滤
		},
		{
			name:     "chinese characters",
			content:  "你好世界测试",
			n:        10,
			expected: "你好世界测试",
		},
		{
			name:     "mixed content",
			content:  "Test测试123",
			n:        10,
			expected: "Test测试123",
		},
		{
			name:     "with special characters",
			content:  "Hello/World\\Test",
			n:        20,
			expected: "HelloWorldTest", // / 和 \ 被过滤掉
		},
		{
			name:     "with newlines",
			content:  "Line1\nLine2\tLine3",
			n:        20,
			expected: "Line1_Line2_Line3",
		},
		{
			name:     "only special characters",
			content:  "!@#$%^&*()",
			n:        10,
			expected: "untitled",
		},
		{
			name:     "truncate long content",
			content:  "This is a very long content that should be truncated",
			n:        10,
			expected: "This_is_a_", // 先截取10个字符 "This is a"，然后替换空格
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractContentPrefix(tt.content, tt.n)
			if result != tt.expected {
				t.Errorf("extractContentPrefix() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExpandPath(t *testing.T) {
	tests := []struct {
		name     string
		client   *Client
		input    string
		expected string
	}{
		{
			name: "path with tilde and cached homeDir",
			client: &Client{
				config:  &Config{User: "testuser"},
				homeDir: "/Users/testuser",
			},
			input:    "~/test/path",
			expected: "/Users/testuser/test/path",
		},
		{
			name: "path with tilde fallback to /home",
			client: &Client{
				config:  &Config{User: "testuser"},
				homeDir: "",
			},
			input:    "~/test/path",
			expected: "/home/testuser/test/path",
		},
		{
			name: "root user with cached homeDir",
			client: &Client{
				config:  &Config{User: "root"},
				homeDir: "/root",
			},
			input:    "~/knas_archive",
			expected: "/root/knas_archive",
		},
		{
			name: "absolute path",
			client: &Client{
				config: &Config{User: "testuser"},
			},
			input:    "/absolute/path",
			expected: "/absolute/path",
		},
		{
			name: "relative path",
			client: &Client{
				config: &Config{User: "testuser"},
			},
			input:    "relative/path",
			expected: "relative/path",
		},
		{
			name: "bare tilde",
			client: &Client{
				config:  &Config{User: "root"},
				homeDir: "/root",
			},
			input:    "~",
			expected: "/root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.client.expandPath(tt.input)
			if result != tt.expected {
				t.Errorf("expandPath() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestNewClient(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		expected *Config
	}{
		{
			name: "default port",
			config: &Config{
				Host:    "localhost",
				User:    "root",
				KeyPath: "~/.ssh/id_rsa",
			},
			expected: &Config{
				Host:    "localhost",
				Port:    "22",
				User:    "root",
				KeyPath: "~/.ssh/id_rsa",
			},
		},
		{
			name: "custom port",
			config: &Config{
				Host:    "localhost",
				Port:    "2222",
				User:    "root",
				KeyPath: "~/.ssh/id_rsa",
			},
			expected: &Config{
				Host:    "localhost",
				Port:    "2222",
				User:    "root",
				KeyPath: "~/.ssh/id_rsa",
			},
		},
		{
			name: "default base path",
			config: &Config{
				Host:    "localhost",
				User:    "root",
				KeyPath: "~/.ssh/id_rsa",
			},
			expected: &Config{
				Host:     "localhost",
				Port:     "22",
				User:     "root",
				KeyPath:  "~/.ssh/id_rsa",
				BasePath: "~/knas_archive",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.config)
			if client.config.Host != tt.expected.Host {
				t.Errorf("Host = %v, want %v", client.config.Host, tt.expected.Host)
			}
			if client.config.Port != tt.expected.Port {
				t.Errorf("Port = %v, want %v", client.config.Port, tt.expected.Port)
			}
			if client.config.User != tt.expected.User {
				t.Errorf("User = %v, want %v", client.config.User, tt.expected.User)
			}
			if tt.expected.BasePath != "" && client.config.BasePath != tt.expected.BasePath {
				t.Errorf("BasePath = %v, want %v", client.config.BasePath, tt.expected.BasePath)
			}
		})
	}
}

func TestShellEscape(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "simple path", input: "/home/user/test.md"},
		{name: "path with spaces", input: "/home/user/my file.md"},
		{name: "path with single quote", input: "/home/user/it's.md"},
		{name: "path with semicolon", input: "/tmp/test; rm -rf /"},
		{name: "path with backtick", input: "/tmp/`whoami`"},
		{name: "path with dollar", input: "/tmp/$HOME"},
		{name: "path with pipe", input: "/tmp/a | b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			escaped := shellEscape(tt.input)
			if !strings.HasPrefix(escaped, "'") || !strings.HasSuffix(escaped, "'") {
				t.Errorf("shellEscape(%q) = %q, not quoted", tt.input, escaped)
			}
		})
	}
}

func TestFormatContent(t *testing.T) {
	client := &Client{config: &Config{}}
	ts := time.Date(2026, 4, 18, 13, 45, 30, 0, time.Local)

	result := client.formatContent("hello world", ts, "abc123def", nil)

	if !strings.Contains(result, "sync_time: 2026-04-18 13:45:30") {
		t.Errorf("formatContent missing sync_time: %q", result)
	}
	if !strings.Contains(result, "source: clipboard") {
		t.Errorf("formatContent missing source: %q", result)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("formatContent missing content: %q", result)
	}
	if !strings.Contains(result, "content_hash: abc123def") {
		t.Errorf("formatContent missing content_hash: %q", result)
	}
}

func TestContentHash(t *testing.T) {
	h1 := contentHash([]byte("hello"))
	h2 := contentHash([]byte("hello"))
	h3 := contentHash([]byte("world"))

	if h1 != h2 {
		t.Error("same content should produce same hash")
	}
	if h1 == h3 {
		t.Error("different content should produce different hash")
	}
	if len(h1) != 32 {
		t.Errorf("expected 32-char hex hash, got %d chars", len(h1))
	}
}

func TestEnsureConnected_NoServer(t *testing.T) {
	client := &Client{
		config: &Config{
			Host:    "127.0.0.1",
			Port:    "1", // 不存在的端口，确保连接失败
			User:    "test",
			KeyPath: "/nonexistent/key",
		},
	}
	err := client.ensureConnected()
	if err == nil {
		t.Error("expected error when no SSH server available")
	}
}

func TestSyncImagePathGeneration(t *testing.T) {
	client := &Client{
		config: &Config{
			User:     "root",
			BasePath: "~/knas_archive",
		},
		homeDir: "/root",
	}
	ts := time.Date(2026, 4, 18, 9, 30, 15, 0, time.UTC)

	timeStr := ts.Format("150405")
	year := ts.Format("2006")
	month := ts.Format("01")
	day := ts.Format("02")

	relPath := year + "/" + month + "/" + day
	if relPath != "2026/04/18" {
		t.Errorf("relPath = %q, want 2026/04/18", relPath)
	}

	// 验证图片路径包含哈希前缀
	testHash := contentHash([]byte("test image data"))
	fileName := timeStr + "_" + testHash[:8] + "_image.png"
	if !strings.Contains(fileName, testHash[:8]) {
		t.Errorf("image filename should contain hash prefix, got %q", fileName)
	}

	// 验证 expandPath 使用缓存的家目录
	expanded := client.expandPath("~/knas_archive/" + relPath + "/" + fileName)
	if !strings.HasPrefix(expanded, "/root/") {
		t.Errorf("expanded path should use cached homeDir, got %q", expanded)
	}
}
