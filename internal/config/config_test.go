package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home directory: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "path with tilde",
			input:    "~/test/path",
			expected: filepath.Join(homeDir, "test/path"),
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
		{
			name:     "empty path",
			input:    "",
			expected: "",
		},
		{
			name:     "tilde only",
			input:    "~",
			expected: homeDir,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := expandPath(tt.input)
			if result != tt.expected {
				t.Errorf("expandPath() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.SSH.Port != "22" {
		t.Errorf("Default SSH port = %v, want 22", cfg.SSH.Port)
	}

	if cfg.SSH.User != "root" {
		t.Errorf("Default SSH user = %v, want root", cfg.SSH.User)
	}

	if cfg.Clipboard.MinLength != 100 {
		t.Errorf("Default MinLength = %v, want 100", cfg.Clipboard.MinLength)
	}

	if cfg.Clipboard.MaxLength != 1024*1024 {
		t.Errorf("Default MaxLength = %v, want %v", cfg.Clipboard.MaxLength, 1024*1024)
	}

	if !cfg.Sync.Enabled {
		t.Error("Default Sync.Enabled should be true")
	}

	if cfg.Sync.MaxRetries != 3 {
		t.Errorf("Default MaxRetries = %v, want 3", cfg.Sync.MaxRetries)
	}
}

func TestConfigSerialization(t *testing.T) {
	cfg := DefaultConfig()

	// 序列化为 JSON
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	// 从 JSON 反序列化
	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal config: %v", err)
	}

	// 验证关键字段
	if decoded.SSH.Port != cfg.SSH.Port {
		t.Errorf("Port mismatch: got %v, want %v", decoded.SSH.Port, cfg.SSH.Port)
	}

	if decoded.Clipboard.MinLength != cfg.Clipboard.MinLength {
		t.Errorf("MinLength mismatch: got %v, want %v", decoded.Clipboard.MinLength, cfg.Clipboard.MinLength)
	}

	if len(decoded.Clipboard.ExcludeWords) != len(cfg.Clipboard.ExcludeWords) {
		t.Errorf("ExcludeWords length mismatch: got %v, want %v",
			len(decoded.Clipboard.ExcludeWords), len(cfg.Clipboard.ExcludeWords))
	}
}

func TestConfigPaths(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home directory: %v", err)
	}

	expectedConfigDir := filepath.Join(homeDir, ".knowly")
	if GetConfigDir() != expectedConfigDir {
		t.Errorf("GetConfigDir() = %v, want %v", GetConfigDir(), expectedConfigDir)
	}

	expectedConfigPath := filepath.Join(expectedConfigDir, ConfigFileName)
	if GetConfigPath() != expectedConfigPath {
		t.Errorf("GetConfigPath() = %v, want %v", GetConfigPath(), expectedConfigPath)
	}

	expectedLogPath := filepath.Join(expectedConfigDir, LogFileName)
	if GetLogPath() != expectedLogPath {
		t.Errorf("GetLogPath() = %v, want %v", GetLogPath(), expectedLogPath)
	}

	expectedPidPath := filepath.Join(expectedConfigDir, PidFileName)
	if GetPidPath() != expectedPidPath {
		t.Errorf("GetPidPath() = %v, want %v", GetPidPath(), expectedPidPath)
	}
}

func TestSetConfigPath(t *testing.T) {
	originalPath := GetConfigPath()
	defer SetConfigPath(originalPath)

	customPath := "/custom/config.json"
	SetConfigPath(customPath)

	if GetConfigPath() != customPath {
		t.Errorf("GetConfigPath() = %v, want %v", GetConfigPath(), customPath)
	}
}
