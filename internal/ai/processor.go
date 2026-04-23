package ai

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/yuanguangshan/knowly/internal/config"
)

// Result AI 处理后的结构化结果
type Result struct {
	Tags             []string `json:"tags"`
	Summary          string   `json:"summary"`
	Score            int      `json:"score"`
	OrganizedContent string   `json:"organized_content"`
	Processed        bool     `json:"processed"`
	Title            string   `json:"title,omitempty"`
}

// Processor 处理 AI API 调用
type Processor struct {
	cfg    *config.AIConfig
	client *http.Client
}

// NewProcessor 创建 AI 处理器，disabled 时返回 nil
func NewProcessor(cfg *config.AIConfig) *Processor {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	return &Processor{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
		},
	}
}

// ShouldProcess 检查内容是否满足 AI 处理的长度要求（nil-safe）
func (p *Processor) ShouldProcess(content string) bool {
	if p == nil {
		return false
	}
	length := len(content)
	return length >= p.cfg.MinContentLen && length <= p.cfg.MaxContentLen
}

const defaultSystemPrompt = `你是一个内容分析助手。用户会给你一段文本内容，你需要：
1. 为内容生成 3-5 个标签（tags）
2. 用一句话生成中文摘要（summary，不超过50字）
3. 给内容质量打分（score，0-10分，10分最高）
4. 将内容整理组织成更清晰的格式（organized_content），使用 Markdown 格式

注意：
- 如果内容是日志、配置文件、系统输出、错误堆栈等机器生成内容，打低分（0-3分），并在 tags 中加入 "system_log"
- 如果内容是人类思考、笔记、文章、代码片段等有价值的信息，正常打分

你必须严格以 JSON 格式回复，不要包含任何其他文字：
{"tags":["tag1","tag2"],"summary":"一句话摘要","score":8,"organized_content":"整理后的内容"}`

// effectiveSystemPrompt 返回有效的系统提示词
func (p *Processor) effectiveSystemPrompt() string {
	if p.cfg.Prompt != "" {
		return p.cfg.Prompt
	}
	return defaultSystemPrompt
}

// Process 发送内容到 AI 进行处理，失败返回 nil（调用方应使用原始内容）
func (p *Processor) Process(ctx context.Context, content string) *Result {
	aiResponse, err := p.callAPI(ctx, p.effectiveSystemPrompt(), content)
	if err != nil {
		log.Printf("[WARN] AI processing failed: %v", err)
		return nil
	}

	result, err := parseAIResponse(aiResponse)
	if err != nil {
		log.Printf("[WARN] AI response parse failed: %v", err)
		return nil
	}

	result.Processed = true
	log.Printf("[INFO] AI processed: score=%d, tags=%v, summary=%q", result.Score, result.Tags, result.Summary)
	return result
}

// TitleAndSummary 用于 AI 返回标题和摘要
type TitleAndSummary struct {
	Title    string `json:"title"`
	Summary  string `json:"summary"`
}

const titlePrompt = `你是一个内容标题和摘要生成助手。用户会给你一段文本内容，你需要：
1. 为内容生成一个简洁、吸引人的标题（title，不超过30字）
2. 为内容生成一段摘要（summary，150字以内）

注意：
- 标题应该简洁明了，能够准确概括内容核心
- 摘要应该突出内容的重点和价值
- 避免使用日期、时间等作为标题

你必须严格以 JSON 格式回复，不要包含任何其他文字：
{"title":"简洁标题","summary":"内容摘要"}`

// GenerateTitleAndSummary 生成标题和摘要，失败返回 nil
func (p *Processor) GenerateTitleAndSummary(ctx context.Context, content string) *TitleAndSummary {
	aiResponse, err := p.callAPI(ctx, titlePrompt, content)
	if err != nil {
		log.Printf("[WARN] AI title generation failed: %v", err)
		return nil
	}

	result, err := parseTitleAndSummary(aiResponse)
	if err != nil {
		log.Printf("[WARN] AI title response parse failed: %v", err)
		return nil
	}

	log.Printf("[INFO] AI generated title: %q, summary: %q", result.Title, result.Summary)
	return result
}
