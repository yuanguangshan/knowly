package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yuanguangshan/knowly/internal/history"
)

// Engine performs periodic offline clustering of history entries.
type Engine struct {
	histStore    *history.Store
	aiCfg        AIAPIConfig
	cfg          Config
	clustersPath string
	mu           sync.RWMutex
	current      *ClusterResult
	lastRun      time.Time
}

// AIAPIConfig holds the connection parameters for the AI API (subset of ai config).
type AIAPIConfig struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
	Timeout  int    `json:"timeout_sec"`
}

// NewEngine creates a clustering engine.
func NewEngine(histStore *history.Store, aiCfg AIAPIConfig, cfg Config, configDir string) *Engine {
	return &Engine{
		histStore:    histStore,
		aiCfg:        aiCfg,
		cfg:          cfg,
		clustersPath: filepath.Join(configDir, "clusters.json"),
	}
}

// Start begins the periodic clustering loop.
// Runs once after a brief startup delay, then repeats on the configured interval.
func (e *Engine) Start(ctx context.Context) {
	if !e.cfg.Enabled || !e.aiCfg.Enabled {
		log.Println("[INFO] Clustering: disabled")
		return
	}

	go func() {
		// Brief delay so the daemon can fully initialise
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}

		e.runOnce(ctx)

		if e.cfg.IntervalH <= 0 {
			return
		}
		ticker := time.NewTicker(time.Duration(e.cfg.IntervalH) * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.runOnce(ctx)
			}
		}
	}()
}

// GetResult returns the current cluster result (thread-safe).
func (e *Engine) GetResult() *ClusterResult {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.current
}

// ForceRun triggers an immediate clustering run.
func (e *Engine) ForceRun() error {
	return e.runOnce(context.Background())
}

// runOnce performs a single clustering pass.
func (e *Engine) runOnce(ctx context.Context) error {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[ERROR] Clustering: panicked: %v", r)
		}
	}()

	log.Println("[INFO] Clustering: starting run")
	start := time.Now()

	// 1. Fetch recent entries from history
	limit := e.cfg.MaxEntries
	if limit <= 0 {
		limit = 500
	}
	entries, err := e.histStore.Recent(limit)
	if err != nil {
		return fmt.Errorf("fetch entries: %w", err)
	}

	// 2. Filter to text entries with AI tags
	var candidates []clusterEntry
	for _, entry := range entries {
		if entry.Type != "text" {
			continue
		}
		if len(entry.Tags) == 0 {
			continue
		}

		preview := entry.Content
		runes := []rune(preview)
		if len(runes) > 150 {
			preview = string(runes[:147]) + "..."
		}

		candidates = append(candidates, clusterEntry{
			ID:      entry.ID,
			Tags:    entry.Tags,
			Summary: entry.PublishSummary,
			Preview: preview,
		})
	}

	if len(candidates) < 3 {
		log.Printf("[INFO] Clustering: too few candidates (%d), skipping", len(candidates))
		return nil
	}

	// 3. Cluster via AI
	clusters, err := e.clusterViaAI(ctx, candidates)
	if err != nil {
		return fmt.Errorf("AI clustering failed: %w", err)
	}

	// 4. Build result
	clusteredIDs := make(map[string]bool)
	for _, cl := range clusters {
		for _, id := range cl.EntryIDs {
			clusteredIDs[id] = true
		}
	}

	var unclustered []string
	for _, ce := range candidates {
		if !clusteredIDs[ce.ID] {
			unclustered = append(unclustered, ce.ID)
		}
	}

	result := &ClusterResult{
		Clusters:       clusters,
		UnclusteredIDs: unclustered,
		GeneratedAt:    time.Now(),
		PeriodEnd:      time.Now(),
		TotalEntries:   len(candidates),
		ClusteredCount: len(clusteredIDs),
	}

	// 5. Store in memory + persist
	e.mu.Lock()
	e.current = result
	e.lastRun = time.Now()
	e.mu.Unlock()

	if err := e.saveClusters(result); err != nil {
		log.Printf("[WARN] Clustering: failed to persist: %v", err)
	}

	log.Printf("[INFO] Clustering: done (%d entries -> %d clusters in %.1fs)",
		len(candidates), len(clusters), time.Since(start).Seconds())

	return nil
}

// clusterViaAI sends candidate entries to AI and parses the cluster response.
func (e *Engine) clusterViaAI(ctx context.Context, entries []clusterEntry) ([]Cluster, error) {
	// Build prompt
	var sb strings.Builder
	sb.WriteString("你是一个知识聚类助手。以下是一组知识条目，每个包含 ID、标签、摘要和内容预览。")
	sb.WriteString("请将主题相关的条目归为同一个聚类，并给每个聚类起一个简洁的名字（2-5个中文字），写一段简短的描述。\n\n")
	sb.WriteString("要求：\n")
	sb.WriteString("- 聚类数量合理（不要太多也不要太少），合并相似主题\n")
	sb.WriteString("- 聚类名称要简洁清晰，能准确概括主题\n")
	sb.WriteString("- 如果某个条目不属于任何聚类，忽略它（不要强行归类）\n")
	sb.WriteString("- 用标签来辅助判断主题关联\n\n")
	sb.WriteString("条目列表：\n\n")

	for i, entry := range entries {
		tags := strings.Join(entry.Tags, ", ")
		if tags == "" {
			tags = "无标签"
		}
		summary := entry.Summary
		if summary == "" {
			summary = "无摘要"
		}
		sb.WriteString(fmt.Sprintf("[%d] ID: %s\n", i+1, entry.ID))
		sb.WriteString(fmt.Sprintf("    标签: %s\n", tags))
		sb.WriteString(fmt.Sprintf("    摘要: %s\n", summary))
		sb.WriteString(fmt.Sprintf("    预览: %s\n\n", entry.Preview))
	}

	sb.WriteString(`请以 JSON 格式返回结果（只输出 JSON，不要其他文字）：
[
  {
    "name": "聚类名称",
    "description": "聚类描述",
    "entry_ids": ["entry_id_1", "entry_id_2"]
  }
]`)

	prompt := sb.String()

	// Call AI API
	timeout := e.aiCfg.Timeout
	if timeout <= 0 {
		timeout = 90
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	respText, err := e.callAI(callCtx, prompt)
	if err != nil {
		return nil, err
	}

	// Parse response
	clusters, err := parseClustersResponse(respText)
	if err != nil {
		log.Printf("[WARN] Clustering: parse failed, raw response (first 500 chars): %s",
			respText[:min(len(respText), 500)])
		return nil, fmt.Errorf("parse AI response: %w", err)
	}

	// Build a lookup map for entries
	entryMap := make(map[string]clusterEntry)
	for _, ce := range entries {
		entryMap[ce.ID] = ce
	}

	// Compute common tags per cluster and entry count
	for i := range clusters {
		tagFreq := make(map[string]int)
		for _, eid := range clusters[i].EntryIDs {
			if ce, ok := entryMap[eid]; ok {
				for _, tag := range ce.Tags {
					tagFreq[tag]++
				}
			}
		}
		// Sort by frequency
		type pair struct {
			tag string
			n   int
		}
		var sorted []pair
		for tag, n := range tagFreq {
			sorted = append(sorted, pair{tag, n})
		}
		sort.Slice(sorted, func(a, b int) bool {
			return sorted[b].n < sorted[a].n
		})
		topN := 5
		if len(sorted) < topN {
			topN = len(sorted)
		}
		for _, p := range sorted[:topN] {
			clusters[i].CommonTags = append(clusters[i].CommonTags, p.tag)
		}
		clusters[i].EntryCount = len(clusters[i].EntryIDs)
	}

	return clusters, nil
}

// callAI sends a single-turn chat request to the configured OpenAI-compatible API.
func (e *Engine) callAI(ctx context.Context, prompt string) (string, error) {
	body := map[string]any{
		"model": e.aiCfg.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.3,
		"max_tokens":  4096,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	endpoint := strings.TrimRight(e.aiCfg.Endpoint, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Id", "knowly-cluster")
	if e.aiCfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.aiCfg.APIKey)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return result.Choices[0].Message.Content, nil
}

// parseClustersResponse extracts a cluster array from the AI response text,
// handling JSON, code fences, and raw text.
func parseClustersResponse(text string) ([]Cluster, error) {
	// Try direct parse first
	text = strings.TrimSpace(text)

	// Extract from markdown code fence if present
	if idx := strings.Index(text, "```json"); idx >= 0 {
		end := strings.Index(text[idx+7:], "```")
		if end >= 0 {
			text = strings.TrimSpace(text[idx+7 : idx+7+end])
		}
	} else if idx := strings.Index(text, "```"); idx >= 0 {
		end := strings.Index(text[idx+3:], "```")
		if end >= 0 {
			text = strings.TrimSpace(text[idx+3 : idx+3+end])
		}
	}

	// Find first '[' and last ']'
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}

	var raw []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		EntryIDs    []string `json:"entry_ids"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	clusters := make([]Cluster, 0, len(raw))
	for _, r := range raw {
		if r.Name == "" || len(r.EntryIDs) == 0 {
			continue
		}
		clusters = append(clusters, Cluster{
			Name:        r.Name,
			Description: r.Description,
			EntryIDs:    r.EntryIDs,
		})
	}

	if len(clusters) == 0 {
		return nil, fmt.Errorf("no valid clusters found in response")
	}

	return clusters, nil
}

// saveClusters persists the cluster result to disk as JSON.
func (e *Engine) saveClusters(result *ClusterResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal clusters: %w", err)
	}
	return os.WriteFile(e.clustersPath, data, 0644)
}

// LoadClusters reads a previously persisted cluster result from disk.
func (e *Engine) LoadClusters() *ClusterResult {
	data, err := os.ReadFile(e.clustersPath)
	if err != nil {
		return nil
	}
	var result ClusterResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	e.mu.Lock()
	e.current = &result
	e.mu.Unlock()
	return &result
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
