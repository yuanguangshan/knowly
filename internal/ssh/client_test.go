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
	client := &Client{
		config: &Config{
			User: "testuser",
		},
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "path with tilde",
			input:    "~/test/path",
			expected: "/home/testuser/test/path",
		},
		{
			name:     "absolute path",
			input:    "/absolute/path",
			expected: "/absolute/path",
		},
		{
			name:     "relative path",
			input:    "relative/path",
			expected: "relative/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := client.expandPath(tt.input)
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

	result := client.formatContent("hello world", ts)

	if !strings.Contains(result, "sync_time: 2026-04-18 13:45:30") {
		t.Errorf("formatContent missing sync_time: %q", result)
	}
	if !strings.Contains(result, "source: clipboard") {
		t.Errorf("formatContent missing source: %q", result)
	}
	if !strings.Contains(result, "hello world") {
		t.Errorf("formatContent missing content: %q", result)
	}
}

func TestSyncImagePathGeneration(t *testing.T) {
	client := &Client{
		config: &Config{
			User:     "root",
			BasePath: "~/knas_archive",
		},
	}
	ts := time.Date(2026, 4, 18, 9, 30, 15, 0, time.UTC)

	timeStr := ts.Format("150405")
	year := ts.Format("2006")
	month := ts.Format("01")
	day := ts.Format("02")

	fileName := timeStr + "_image.png"
	if fileName != "093015_image.png" {
		t.Errorf("image filename = %q, want 093015_image.png", fileName)
	}

	relPath := year + "/" + month + "/" + day
	if relPath != "2026/04/18" {
		t.Errorf("relPath = %q, want 2026/04/18", relPath)
	}

	expanded := client.expandPath("~/knas_archive/" + relPath + "/" + fileName)
	if expanded != "/home/root/knas_archive/2026/04/18/093015_image.png" {
		t.Errorf("expanded path = %q", expanded)
	}
}
