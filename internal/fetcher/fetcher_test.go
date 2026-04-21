package fetcher

import (
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
			expected: true, // IsURL 只检查是否包含 URL，不检查前后文本
		},
		{
			name:     "too long to be URL",
			input:    "https://example.com/" + string(make([]byte, 300)),
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
