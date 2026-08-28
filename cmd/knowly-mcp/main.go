// knowly-mcp：knowly 知识库的 stdio MCP 服务器（JSONL JSON-RPC 2.0）。
//
// 把 /api/v1/* 快速查询接口（本地 SQLite FTS5 索引，毫秒级、中文子串友好）
// 包装成 MCP 工具，供 weclaw 等 MCP 宿主以 per-agent mcp_command 方式挂载，
// 替代老的「SSH 全树 grep」查询路径。
//
// 工具：
//
//	knowly_search(q, limit) — FTS5 全文检索（1-2 字符自动 LIKE 兜底）
//	knowly_entry(path)      — 按相对路径取条目全文（本地索引优先，回源 NAS）
//	knowly_tags()           — 聚合索引内全部标签及次数
//	knowly_status()         — 索引健康状态
//
// 配置（环境变量）：
//
//	KNOWLY_BASE_URL — 默认 http://127.0.0.1:8090
//	KNOWLY_TOKEN    — 可选；配置后请求带 Authorization: Bearer <token>
//
// 协议：initialize / tools/list / tools/call；通知（无 id）一律忽略；
// stdin EOF 退出。行协议读写，请求/响应各占一行。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	serverName    = "knowly-mcp"
	serverVersion = "1.0.0"
	// 单工具结果上限（字符）：保护模型上下文，超出截断并附提示。
	maxToolResultChars = 16000
	httpTimeout        = 15 * time.Second
)

type request struct {
	ID     *json.RawMessage `json:"id"`
	Method string           `json:"method"`
	Params json.RawMessage  `json:"params"`
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func strSchema(props string, required string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":%s,"required":%s}`, props, required))
}

var tools = []tool{
	{
		Name:        "knowly_search",
		Description: "在 knowly 知识库全文检索归档文章（毫秒级本地索引，中文子串友好）。返回标题、标签、路径、摘要与排名。多词查询零命中时会自动拆词重试并合并结果；建议优先用单个关键词或短语。",
		InputSchema: strSchema(
			`{"q":{"type":"string","description":"搜索关键词"},"limit":{"type":"integer","description":"返回条数，默认 10，最大 50"}}`,
			`["q"]`,
		),
	},
	{
		Name:        "knowly_entry",
		Description: "按相对路径读取 knowly 知识库条目全文（如 2026/08/28/180000_dna.md）。本地索引未命中（图片、历史文件）自动回源 NAS。",
		InputSchema: strSchema(
			`{"path":{"type":"string","description":"条目相对路径，来自 knowly_search 结果的 path 字段"}}`,
			`["path"]`,
		),
	},
	{
		Name:        "knowly_tags",
		Description: "列出 knowly 知识库索引内全部标签及出现次数（降序），用于了解知识库覆盖面。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "knowly_status",
		Description: "knowly 索引健康状态：API 版本、索引是否正常、条目总数。",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
}

var httpClient = &http.Client{Timeout: httpTimeout}

func baseURL() string {
	if v := strings.TrimSpace(os.Getenv("KNOWLY_BASE_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://127.0.0.1:8090"
}

// v1Get 调用 /api/v1/<path>?<query>，返回响应 body。
func v1Get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := baseURL() + path
	if enc := query.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if tok := strings.TrimSpace(os.Getenv("KNOWLY_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("knowly %s: HTTP %d: %s", path, resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// displayTitle 索引里 title 常为空：从路径文件名兜底（去时间戳前缀、去扩展名）。
func displayTitle(title, path string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	name := path
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, ".md")
	name = strings.TrimSuffix(name, ".txt")
	// 去掉 HHMMSS_ 这类时间戳前缀
	if len(name) > 7 && name[6] == '_' {
		name = name[7:]
	}
	return name
}

// searchResult 是 /api/v1/search 的单条结果（只取 MCP 工具需要的字段）。
type searchResult struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Tags    string `json:"tags"`
	Time    string `json:"time"`
	Snippet string `json:"snippet"`
}

// searchOnce 调一次 /api/v1/search 并解析结果数组。
func searchOnce(ctx context.Context, q string, limit int) ([]searchResult, error) {
	body, err := v1Get(ctx, "/api/v1/search", url.Values{"q": {q}, "limit": {fmt.Sprint(limit)}})
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []searchResult `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析搜索结果失败: %w", err)
	}
	return out.Results, nil
}

// splitQueryTerms 按空白与常见中英文标点拆词。
func splitQueryTerms(q string) []string {
	return strings.FieldsFunc(q, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', '，', '。', '、', '；', ';', '：', ':', '?', '？', '!', '！',
			'"', '\'', '「', '」', '『', '』', '(', ')', '（', '）', '《', '》':
			return true
		}
		return false
	})
}

// knowlySearchResults 带降级的搜索：整串查询优先；零命中时拆词逐词重试、按路径
// 去重合并。背景：FTS5 trigram 索引对带空格/标点的多词中文查询可能零命中，而
// 模型调工具时习惯把整句需求塞进 q（实测「双螺旋 结构」零命中、「双螺旋」3 条）。
func knowlySearchResults(ctx context.Context, q string, limit int) ([]searchResult, error) {
	results, err := searchOnce(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	if len(results) > 0 {
		return results, nil
	}
	seen := map[string]bool{}
	merged := make([]searchResult, 0, limit)
	for _, term := range splitQueryTerms(q) {
		if len([]rune(term)) < 2 {
			continue // 单字词走 trigram 索引噪声大，跳过
		}
		rs, err := searchOnce(ctx, term, limit)
		if err != nil {
			continue
		}
		for _, r := range rs {
			if seen[r.Path] {
				continue
			}
			seen[r.Path] = true
			merged = append(merged, r)
			if len(merged) >= limit {
				return merged, nil
			}
		}
	}
	return merged, nil
}

// clampResult 截断超长工具结果，保护模型上下文。
func clampResult(s string) string {
	r := []rune(s)
	if len(r) <= maxToolResultChars {
		return s
	}
	return string(r[:maxToolResultChars]) + fmt.Sprintf("\n\n（结果过长已截断：原文共 %d 字符，可细化关键词缩小范围）", len(r))
}

func callTool(ctx context.Context, name string, args map[string]interface{}) (string, bool, error) {
	switch name {
	case "knowly_search":
		q, _ := args["q"].(string)
		q = strings.TrimSpace(q)
		if q == "" {
			return "", true, fmt.Errorf("缺少必填参数 q")
		}
		limit := 10
		if v, ok := args["limit"].(float64); ok && v > 0 {
			limit = int(v)
			if limit > 50 {
				limit = 50
			}
		}
		results, err := knowlySearchResults(ctx, q, limit)
		if err != nil {
			return "", true, err
		}
		if len(results) == 0 {
			return "知识库中没有匹配「" + q + "」的内容。", false, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "共 %d 条结果：\n", len(results))
		for i, r := range results {
			fmt.Fprintf(&b, "\n%d. %s\n", i+1, truncate(displayTitle(r.Title, r.Path), 80))
			if r.Tags != "" {
				fmt.Fprintf(&b, "   标签: %s\n", r.Tags)
			}
			fmt.Fprintf(&b, "   时间: %s\n", r.Time)
			fmt.Fprintf(&b, "   路径: %s\n", r.Path)
			if r.Snippet != "" {
				snip := strings.ReplaceAll(r.Snippet, "<mark>", "「")
				snip = strings.ReplaceAll(snip, "</mark>", "」")
				fmt.Fprintf(&b, "   摘要: %s\n", truncate(snip, 200))
			}
		}
		b.WriteString("\n（用 knowly_entry + 路径可读取全文）")
		return b.String(), false, nil

	case "knowly_entry":
		p, _ := args["path"].(string)
		p = strings.TrimSpace(p)
		if p == "" {
			return "", true, fmt.Errorf("缺少必填参数 path")
		}
		body, err := v1Get(ctx, "/api/v1/entry", url.Values{"path": {p}})
		if err != nil {
			return "", true, err
		}
		var out struct {
			Path    string `json:"path"`
			Title   string `json:"title"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			// 非 JSON 响应：当作纯文本全文返回
			return clampResult(string(body)), false, nil
		}
		if out.Content == "" {
			return "", true, fmt.Errorf("条目 %s 内容为空", p)
		}
		header := ""
		if out.Title != "" {
			header = "标题: " + out.Title + "\n\n"
		}
		return clampResult(header + out.Content), false, nil

	case "knowly_tags":
		body, err := v1Get(ctx, "/api/v1/tags", nil)
		if err != nil {
			return "", true, err
		}
		return clampResult(string(body)), false, nil

	case "knowly_status":
		body, err := v1Get(ctx, "/api/v1/status", nil)
		if err != nil {
			return "", true, err
		}
		return string(body), false, nil
	}
	return "", false, fmt.Errorf("unknown tool: %s", name)
}

func writeLine(w io.Writer, v interface{}) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("mcp: marshal 响应失败: %v", err)
		return
	}
	w.Write(append(b, '\n'))
}

func main() {
	log.SetFlags(0)
	// 日志走 stderr（stdout 是协议通道，不能污染）
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 64<<20) // entry 全文可能很大
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			log.Printf("mcp: 跳过非法行: %v", err)
			continue
		}
		if req.ID == nil {
			continue // 通知（notifications/initialized 等）无需响应
		}
		id := *req.ID
		switch req.Method {
		case "initialize":
			writeLine(out, map[string]interface{}{
				"jsonrpc": "2.0", "id": id,
				"result": map[string]interface{}{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
					"serverInfo":      map[string]string{"name": serverName, "version": serverVersion},
				},
			})
		case "ping":
			writeLine(out, map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": map[string]interface{}{}})
		case "tools/list":
			writeLine(out, map[string]interface{}{"jsonrpc": "2.0", "id": id, "result": map[string]interface{}{"tools": tools}})
		case "tools/call":
			var p struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil {
				writeLine(out, map[string]interface{}{"jsonrpc": "2.0", "id": id,
					"error": map[string]interface{}{"code": -32602, "message": "非法 params: " + err.Error()}})
				break
			}
			text, isErr, err := callTool(context.Background(), p.Name, p.Arguments)
			if err != nil {
				// 工具级失败走 MCP isError 约定（模型可读到原因并自我纠正），而非 JSON-RPC error
				text = "工具执行失败: " + err.Error()
				isErr = true
			}
			writeLine(out, map[string]interface{}{"jsonrpc": "2.0", "id": id,
				"result": map[string]interface{}{
					"content": []map[string]string{{"type": "text", "text": clampResult(text)}},
					"isError": isErr,
				}})
		default:
			writeLine(out, map[string]interface{}{"jsonrpc": "2.0", "id": id,
				"error": map[string]interface{}{"code": -32601, "message": "method not found: " + req.Method}})
		}
		out.Flush()
	}
}
