package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	SSH          SSHConfig          `json:"ssh"`
	Clipboard    ClipboardConfig    `json:"clipboard"`
	Sync         SyncConfig         `json:"sync"`
}

type SSHConfig struct {
	Host                 string `json:"host"`
	Port                 string `json:"port"`
	User                 string `json:"user"`
	KeyPath              string `json:"key_path"`
	BasePath             string `json:"base_path"`
	FilenamePrefixLength int    `json:"filename_prefix_length"`
}

type ClipboardConfig struct {
	MinLength     int      `json:"min_length"`
	MaxLength     int      `json:"max_length"`
	PollInterval  int      `json:"poll_interval_ms"`
	ExcludeWords  []string `json:"exclude_words"`
}

type SyncConfig struct {
	Enabled     bool `json:"enabled"`
	MaxRetries  int  `json:"max_retries"`
	RetryDelay  int  `json:"retry_delay_ms"`
}

const (
	DefaultConfigDir  = "~/.knas"
	ConfigFileName    = "config.json"
	LogFileName       = "knas.log"
	PidFileName       = "knas.pid"
)

var (
	configPath string
	logPath    string
	pidPath    string
)

func init() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	configPath = filepath.Join(homeDir, ".knas", ConfigFileName)
	logPath = filepath.Join(homeDir, ".knas", LogFileName)
	pidPath = filepath.Join(homeDir, ".knas", PidFileName)
}

func GetConfigPath() string {
	return configPath
}

func SetConfigPath(path string) {
	configPath = expandPath(path)
}

func GetLogPath() string {
	return logPath
}

func GetPidPath() string {
	return pidPath
}

func GetConfigDir() string {
	return filepath.Dir(configPath)
}

func expandPath(path string) string {
	if len(path) > 0 && path[0] == '~' {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(homeDir, path[1:])
		}
	}
	return path
}

func Load() (*Config, error) {
	// 检查配置文件是否存在
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("config file not found: %s", configPath)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// 展开路径
	config.SSH.KeyPath = expandPath(config.SSH.KeyPath)
	config.SSH.BasePath = expandPath(config.SSH.BasePath)

	return &config, nil
}

func Save(config *Config) error {
	// 确保配置目录存在
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

func DefaultConfig() *Config {
	return &Config{
		SSH: SSHConfig{
			Host:                 "",
			Port:                 "22",
			User:                 "root",
			KeyPath:              "~/.ssh/id_rsa",
			BasePath:             "~/knas_archive",
			FilenamePrefixLength: 20, // 默认使用前 20 个字符
		},
		Clipboard: ClipboardConfig{
			MinLength:     100,
			MaxLength:     1024 * 1024, // 1MB
			PollInterval:  500,
			ExcludeWords:  []string{"password", "密码", "token"},
		},
		Sync: SyncConfig{
			Enabled:     true,
			MaxRetries:  3,
			RetryDelay:  5000,
		},
	}
}

func IsConfigured() bool {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return false
	}
	return true
}
