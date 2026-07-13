package ai

import (
	"context"
	"log"
	"net/http"
	"strings"

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
		// 超时完全交给调用方传入的 ctx 控制（main.go 用 cfg.Timeout 构造）。
		// 不在 http.Client 设 Timeout，避免单次慢请求既撞 client 超时又耗尽 ctx 预算，
		// 导致 retry.Do 拿不到重试机会。
		client: &http.Client{},
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

// ShouldSendToAI 检查内容是否应发送到外部 AI API。
// 若内容包含 AIExcludeWords 中的任意关键词，返回 false（同步到 NAS，但不送 AI）。
// 永远放行不包含排除词的内容（nil-safe）。
func (p *Processor) ShouldSendToAI(content string) bool {
	if p == nil {
		return false
	}
	for _, word := range p.cfg.AIExcludeWords {
		if strings.Contains(content, word) {
			log.Printf("[INFO] AI excluded by keyword %q: content contains %q (len=%d, will sync but skip AI)", word, word, len(content))
			return false
		}
	}
	return true
}

const defaultSystemPrompt = `你是一个内容分析助手。用户会给你一段文本内容，你需要：
1. 提炼一个精炼的中文标题（title）
2. 为内容生成 3-5 个标签（tags）
3. 用一句话生成中文摘要（summary，不超过50字）
4. 给内容质量打分（score，0-10分，10分最高）
5. 将内容重新整理为清晰、可读的 Markdown 文章（organized_content），要求：
   - 保留原文的完整信息、数据、案例、作者观点和分析脉络
   - 去除广告、推广、重复、导航栏等噪音内容
   - 结构清晰，段落分明，使用合适的 Markdown 标题、列表、表格
   - 不要过度压缩或简化，保持原文的深度和细节
   - 如果原文包含多段不同来源的内容（如原文+解读），保持这种层次结构

注意：
- 如果内容是日志、配置文件、系统输出、错误堆栈等机器生成内容，打低分（0-3分），并在 tags 中加入 "system_log"
- 如果内容是人类思考、笔记、文章、代码片段等有价值的信息，正常打分

你必须严格以 JSON 格式回复，不要包含任何其他文字：
{"title":"文章标题","tags":["tag1","tag2"],"summary":"一句话摘要","score":8,"organized_content":"整理后的内容"}`

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
	log.Printf("[INFO] AI processed: title=%q score=%d tags=%v summary=%q", result.Title, result.Score, result.Tags, result.Summary)
	return result
}

// TitleAndSummary 用于 AI 返回标题和摘要
type TitleAndSummary struct {
	Title           string   `json:"title"`
	CandidateTitles []string `json:"candidate_titles,omitempty"` // 解析后的候选标题列表
	Summary         string   `json:"summary"`
}

const titlePrompt = `你是一个专业的科技编辑。我将给你一段文本，你需要完成两项任务：

1. **标题 (title)**：生成1-3个候选标题（用 | 分隔），要求：
   - **核心价值**：突出内容最大的创新点、解决方案或利益。
   - **具体生动**：避免空泛词汇，尽量包含关键名词（如 Kindle、NAS、知识管道）。
   - **引人注目**：适合公众号、博客发表，但不要标题党。
   - **风格参考**："一次复制，终身阅读：Knowly 如何将你的剪贴板直通 Kindle"、"Kindle 自动推送上线！让你的每个碎片灵感都能沉淀成册"。

2. **摘要 (summary)**：控制在150字以内，用一句话概括核心亮点和用户价值。

你必须严格以 JSON 格式回复，不要包含任何其他文字：
{"title":"候选标题1 | 候选标题2 | 候选标题3", "summary":"内容摘要"}`

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

	// 解析候选标题（用 | 分隔）
	if result.Title != "" {
		result.CandidateTitles = splitTitles(result.Title)
		// 默认使用第一个标题
		result.Title = result.CandidateTitles[0]
	}

	log.Printf("[INFO] AI generated title: %q, summary: %q", result.Title, result.Summary)
	return result
}

// splitTitles 将用 | 分隔的标题字符串分割成列表
func splitTitles(titleStr string) []string {
	titles := strings.Split(titleStr, "|")
	var result []string
	for _, t := range titles {
		trimmed := strings.TrimSpace(t)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	// 如果解析失败，至少返回原字符串
	if len(result) == 0 && titleStr != "" {
		return []string{titleStr}
	}
	return result
}
