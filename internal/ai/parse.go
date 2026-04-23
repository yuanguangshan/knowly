package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseAIResponse 从 AI 响应中提取 JSON 结果（容错处理）
func parseAIResponse(raw string) (*Result, error) {
	// 1. 直接解析 JSON
	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err == nil {
		return validateResult(&result)
	}

	// 2. 从 markdown code fence 中提取 JSON
	jsonStr := extractJSONBlock(raw)
	if jsonStr != "" {
		if err := json.Unmarshal([]byte(jsonStr), &result); err == nil {
			return validateResult(&result)
		}
	}

	// 3. 查找第一个 { 到最后一个 } 之间的内容
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err == nil {
			return validateResult(&result)
		}
	}

	return nil, fmt.Errorf("could not extract JSON from AI response")
}

// extractJSONBlock 从 ```json ... ``` 围栏中提取内容
func extractJSONBlock(s string) string {
	marker := "```json"
	start := strings.Index(s, marker)
	if start < 0 {
		marker = "```"
		start = strings.Index(s, marker)
	}
	if start < 0 {
		return ""
	}
	start += len(marker)
	end := strings.Index(s[start:], "```")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(s[start : start+end])
}

// validateResult 确保解析结果的值合理
func validateResult(r *Result) (*Result, error) {
	if r.Score < 0 {
		r.Score = 0
	}
	if r.Score > 10 {
		r.Score = 10
	}
	if len(r.Tags) == 0 {
		r.Tags = []string{"untagged"}
	}
	if r.Summary == "" {
		r.Summary = "无法生成摘要"
	}
	if r.OrganizedContent == "" {
		return nil, fmt.Errorf("organized_content is empty")
	}
	return r, nil
}

// parseTitleAndSummary 从 AI 响应中提取标题和摘要（容错处理）
func parseTitleAndSummary(raw string) (*TitleAndSummary, error) {
	// 1. 直接解析 JSON
	var result TitleAndSummary
	if err := json.Unmarshal([]byte(raw), &result); err == nil {
		return validateTitleAndSummary(&result)
	}

	// 2. 从 markdown code fence 中提取 JSON
	jsonStr := extractJSONBlock(raw)
	if jsonStr != "" {
		if err := json.Unmarshal([]byte(jsonStr), &result); err == nil {
			return validateTitleAndSummary(&result)
		}
	}

	// 3. 查找第一个 { 到最后一个 } 之间的内容
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err == nil {
			return validateTitleAndSummary(&result)
		}
	}

	return nil, fmt.Errorf("could not extract JSON from AI response")
}

// validateTitleAndSummary 确保解析结果的值合理
func validateTitleAndSummary(r *TitleAndSummary) (*TitleAndSummary, error) {
	if r.Title == "" {
		return nil, fmt.Errorf("title is empty")
	}
	if r.Summary == "" {
		r.Summary = "暂无摘要"
	}
	return r, nil
}
