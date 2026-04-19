package clipboard

import (
	"testing"
)

func TestHashContent(t *testing.T) {
	hash1 := hashStr("hello")
	hash2 := hashStr("hello")
	hash3 := hashStr("world")

	if hash1 != hash2 {
		t.Error("same content should produce same hash")
	}
	if hash1 == hash3 {
		t.Error("different content should produce different hash")
	}
	if len(hash1) != 32 {
		t.Errorf("expected 32-char hex hash, got %d chars", len(hash1))
	}
}

func TestHashImage(t *testing.T) {
	data1 := []byte{1, 2, 3}
	data2 := []byte{1, 2, 3}
	data3 := []byte{4, 5, 6}

	hash1 := hashBytes(data1)
	hash2 := hashBytes(data2)
	hash3 := hashBytes(data3)

	if hash1 != hash2 {
		t.Error("same data should produce same hash")
	}
	if hash1 == hash3 {
		t.Error("different data should produce different hash")
	}
}

func TestNewMonitorDefaults(t *testing.T) {
	m := NewMonitor(MonitorConfig{}, "")
	if m.minLength != 100 {
		t.Errorf("expected default minLength 100, got %d", m.minLength)
	}
	if m.maxLength != 1024*1024 {
		t.Errorf("expected default maxLength 1MB, got %d", m.maxLength)
	}
	if m.pollInterval != 500*1e6 {
		t.Errorf("expected default pollInterval 500ms, got %v", m.pollInterval)
	}
}

func TestShouldFilter(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		minLength    int
		maxLength    int
		excludeWords []string
		want         bool
	}{
		{
			name:         "content too short",
			content:      "hi",
			minLength:    100,
			maxLength:    1024 * 1024,
			excludeWords: nil,
			want:         true,
		},
		{
			name:         "content too long",
			content:      string(make([]byte, 1025)),
			minLength:    0,
			maxLength:    1024,
			excludeWords: nil,
			want:         true,
		},
		{
			name:         "contains exclude word password",
			content:      "this is my password secret",
			minLength:    0,
			maxLength:    1024 * 1024,
			excludeWords: []string{"password", "密码", "token"},
			want:         true,
		},
		{
			name:         "contains exclude word 密码",
			content:      "这是我的密码",
			minLength:    0,
			maxLength:    1024 * 1024,
			excludeWords: []string{"password", "密码", "token"},
			want:         true,
		},
		{
			name:         "content passes all filters",
			content:      "this is normal content that passes",
			minLength:    0,
			maxLength:    1024 * 1024,
			excludeWords: []string{"password", "密码", "token"},
			want:         false,
		},
		{
			name:         "empty exclude list",
			content:      "password is here",
			minLength:    0,
			maxLength:    1024 * 1024,
			excludeWords: nil,
			want:         false,
		},
		{
			name:         "exactly at min length",
			content:      string(make([]byte, 100)),
			minLength:    100,
			maxLength:    1024 * 1024,
			excludeWords: nil,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldFilter(tt.content, tt.minLength, tt.maxLength, tt.excludeWords)
			if got != tt.want {
				t.Errorf("ShouldFilter() = %v, want %v", got, tt.want)
			}
		})
	}
}
