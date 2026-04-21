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
	Web          WebConfig          `json:"web"`
	Relay        RelayConfig        `json:"relay"`
	Blog         BlogConfig         `json:"blog"`
	Podcast      PodcastConfig      `json:"podcast"`
	IMA          IMAConfig          `json:"ima"`
	AI           AIConfig           `json:"ai"`
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

type WebConfig struct {
	Enabled *bool  `json:"enabled"` // 是否启用 Web 管理界面，nil 或 true 表示启用
	Port    int    `json:"port"`    // 监听端口，默认 8090
	Auth    string `json:"auth"`    // HTTP Basic Auth 凭证，格式 "user:password"，留空则不启用认证
}

func (w *WebConfig) IsEnabled() bool {
	return w.Enabled == nil || *w.Enabled
}

type RelayConfig struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret"`
	Interval int    `json:"pull_interval_sec"`
}

type BlogConfig struct {
	Enabled bool   `json:"enabled"`
	APIURL  string `json:"api_url"`
	Tags    string `json:"tags"`
}

type PodcastConfig struct {
	Enabled bool   `json:"enabled"`
	APIURL  string `json:"api_url"`
	AppID   string `json:"app_id"`
}

type IMAConfig struct {
	Enabled  bool   `json:"enabled"`
	APIURL   string `json:"api_url"`
	ClientID string `json:"client_id"`
	APIKey   string `json:"api_key"`
	FolderID string `json:"folder_id"`
}

type AIConfig struct {
	Enabled       bool   `json:"enabled"`
	Endpoint      string `json:"endpoint"`        // OpenAI 兼容 API 地址，如 http://localhost:11434/v1
	APIKey        string `json:"api_key"`          // 留空用于 Ollama 等本地模型
	Model         string `json:"model"`            // 模型名称，如 deepseek-chat、gpt-4o-mini
	MinContentLen int    `json:"min_content_len"`  // 跳过 AI 的最小内容长度，默认 50
	MaxContentLen int    `json:"max_content_len"`  // 跳过 AI 的最大内容长度，默认 10000
	Timeout       int    `json:"timeout_sec"`      // HTTP 请求超时秒数，默认 30
}

const (
	DefaultConfigDir  = "~/.knowly"
	ConfigFileName    = "config.json"
	LogFileName       = "knowly.log"
	PidFileName       = "knowly.pid"
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

	newDir := filepath.Join(homeDir, ".knowly")
	oldDir := filepath.Join(homeDir, ".knas")

	// 自动迁移：~/.knas → ~/.knowly
	if _, err := os.Stat(newDir); os.IsNotExist(err) {
		if _, err := os.Stat(oldDir); err == nil {
			os.Rename(oldDir, newDir)
		}
	}

	configPath = filepath.Join(newDir, ConfigFileName)
	logPath = filepath.Join(newDir, LogFileName)
	pidPath = filepath.Join(newDir, PidFileName)
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

	// 补全默认 API URL
	if config.Blog.APIURL == "" {
		config.Blog.APIURL = "https://api.yuangs.cc"
	}
	if config.Podcast.APIURL == "" {
		config.Podcast.APIURL = "https://api.yuangs.cc"
	}
	if config.Podcast.AppID == "" {
		config.Podcast.AppID = "nanobot-podcast-publisher"
	}
	if config.IMA.APIURL == "" {
		config.IMA.APIURL = "https://ima.qq.com/openapi/note/v1"
	}

	// 补全 AI 默认值
	if config.AI.Endpoint == "" {
		config.AI.Endpoint = "https://aiproxy.want.biz/v1"
	}
	if config.AI.Model == "" {
		config.AI.Model = "Assisant"
	}
	if config.AI.MinContentLen == 0 {
		config.AI.MinContentLen = 100
	}
	if config.AI.MaxContentLen == 0 {
		config.AI.MaxContentLen = 10000
	}
	if config.AI.Timeout == 0 {
		config.AI.Timeout = 60
	}

	// 补全 Web 默认值
	if config.Web.Port == 0 {
		config.Web.Port = 8090
	}

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
			BasePath:             "~/knowly_archive",
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
		Relay: RelayConfig{
			Enabled:  false,
			Endpoint: "",
			Secret:   "",
			Interval: 5,
		},
		Blog: BlogConfig{
			Enabled: false,
			APIURL:  "https://api.yuangs.cc",
			Tags:    "",
		},
		Podcast: PodcastConfig{
			Enabled: false,
			APIURL:  "https://api.yuangs.cc",
			AppID:   "nanobot-podcast-publisher",
		},
		IMA: IMAConfig{
			Enabled:  false,
			APIURL:   "https://ima.qq.com/openapi/note/v1",
			ClientID: "",
			APIKey:   "",
			FolderID: "",
		},
		AI: AIConfig{
			Enabled:       false,
			Endpoint:      "https://aiproxy.want.biz/v1",
			Model:         "Assisant",
			MinContentLen: 100,
			MaxContentLen: 10000,
			Timeout:       60,
		},
		Web: WebConfig{
			Port: 8090,
		},
	}
}

func IsConfigured() bool {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return false
	}
	return true
}
