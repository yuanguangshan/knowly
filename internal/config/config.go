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
	Kindle       KindleConfig       `json:"kindle"`
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

type KindleConfig struct {
	Enabled        bool   `json:"enabled"`
	SenderEmail    string `json:"sender_email"`
	SenderPassword string `json:"sender_password"`
	SMTPServer     string `json:"smtp_server"`
	SMTPPort       int    `json:"smtp_port"`
	KindleEmail    string `json:"kindle_email"`
}

type AIConfig struct {
	Enabled       bool   `json:"enabled"`
	Preset        string `json:"preset"`           // 服务商预设：openrouter/ollama/deepseek/openai/custom
	Endpoint      string `json:"endpoint"`          // OpenAI 兼容 API 地址，如 http://localhost:11434/v1
	APIKey        string `json:"api_key"`           // 留空用于 Ollama 等本地模型
	Model         string `json:"model"`             // 模型名称，如 deepseek-chat、gpt-4o-mini
	MinContentLen int    `json:"min_content_len"`   // 跳过 AI 的最小内容长度，默认 100
	MaxContentLen int    `json:"max_content_len"`   // 跳过 AI 的最大内容长度，默认 10000
	Timeout        int    `json:"timeout_sec"`       // HTTP 请求超时秒数，默认 60
	Prompt         string `json:"prompt"`            // 自定义系统提示词，留空使用默认
	PromptTemplate string `json:"prompt_template"`   // 提示词模板名称：通用模式/代码模式/学术模式/极简模式
}

// AIPresetOption 服务商预设选项
type AIPresetOption struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	Label    string `json:"label"`
}

// AIPresets 内置服务商预设
var AIPresets = map[string]AIPresetOption{
	"openrouter": {"https://openrouter.ai/api/v1", "openrouter/free", "OpenRouter（推荐）"},
	"ollama":     {"http://localhost:11434/v1", "llama3", "Ollama（本地，无需 Key）"},
	"deepseek":   {"https://api.deepseek.com/v1", "deepseek-chat", "DeepSeek"},
	"openai":     {"https://api.openai.com/v1", "gpt-4o-mini", "OpenAI"},
}

// AIPromptTemplates 内置提示词模板
var AIPromptTemplates = map[string]string{
	"通用模式": "",
	"代码模式": `你是一个专注于代码分析的助手。用户会给你一段文本内容，你需要：
1. 为内容生成 3-5 个标签（tags），侧重编程语言、框架、技术概念
2. 用一句话生成中文摘要（summary，不超过50字）
3. 给内容质量打分（score，0-10分，10分最高）
4. 将代码内容整理成带语法高亮标记的格式（organized_content），使用 Markdown 格式

注意：
- 如果内容是日志、配置文件、系统输出、错误堆栈等机器生成内容，打低分（0-3分），并在 tags 中加入 "system_log"
- 如果内容是人类思考、笔记、文章、代码片段等有价值的信息，正常打分

你必须严格以 JSON 格式回复，不要包含任何其他文字：
{"tags":["tag1","tag2"],"summary":"一句话摘要","score":8,"organized_content":"整理后的内容"}`,
	"学术模式": `你是一个学术研究助手。用户会给你一段文本内容，你需要：
1. 为内容生成 3-5 个标签（tags），侧重研究方法、核心结论、学科领域
2. 用一句话生成中文摘要（summary，不超过50字），突出研究贡献
3. 给内容质量打分（score，0-10分，10分最高）
4. 整理内容结构（organized_content），提取研究背景、方法、结论，使用 Markdown 格式

注意：
- 识别并标注引用信息、数据来源
- 区分观点和事实

你必须严格以 JSON 格式回复，不要包含任何其他文字：
{"tags":["tag1","tag2"],"summary":"一句话摘要","score":8,"organized_content":"整理后的内容"}`,
	"极简模式": `简短分析内容，生成标签和摘要。

你必须严格以 JSON 格式回复，不要包含任何其他文字：
{"tags":["tag1","tag2"],"summary":"一句话摘要","score":8,"organized_content":"整理后的内容"}`,
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
	if config.Kindle.SMTPServer == "" {
		config.Kindle.SMTPServer = "smtp.qq.com"
	}
	if config.Kindle.SMTPPort == 0 {
		config.Kindle.SMTPPort = 465
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

	// 预设解析：当 Preset 非 custom 且非空时，覆盖默认 endpoint 和 model
	if config.AI.Preset != "" && config.AI.Preset != "custom" {
		if p, ok := AIPresets[config.AI.Preset]; ok {
			if config.AI.Endpoint == "" || config.AI.Endpoint == "https://aiproxy.want.biz/v1" {
				config.AI.Endpoint = p.Endpoint
			}
			if config.AI.Model == "" || config.AI.Model == "Assisant" {
				config.AI.Model = p.Model
			}
		}
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
		Kindle: KindleConfig{
			Enabled:        false,
			SenderEmail:    "",
			SenderPassword: "",
			SMTPServer:     "smtp.qq.com",
			SMTPPort:       465,
			KindleEmail:    "",
		},
		AI: AIConfig{
			Enabled:       false,
			Preset:        "custom",
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
