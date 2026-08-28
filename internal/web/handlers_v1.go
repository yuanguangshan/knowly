package web

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yuanguangshan/knowly/internal/index"
)

// requireAPIToken 校验 /api/v1 的 Bearer token。
// 未配置 token 时信任网络层隔离（WireGuard 内网），直接放行。
func (s *Server) requireAPIToken(r *http.Request) bool {
	if s.cfg.API.Token == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	return auth == "Bearer "+s.cfg.API.Token
}

// handleV1Search GET /api/v1/search?q=&limit=
// 毫秒级本地全文检索（SQLite FTS5 trigram，中文子串友好）。
func (s *Server) handleV1Search(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPIToken(r) {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.indexer == nil {
		jsonError(w, "index not available", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query().Get("q")
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	hits, err := s.indexer.Search(q, limit)
	if err != nil {
		jsonError(w, "search failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if hits == nil {
		hits = []index.Hit{}
	}
	jsonResp(w, map[string]interface{}{
		"query":   q,
		"count":   len(hits),
		"results": hits,
	})
}

// handleV1Entry GET /api/v1/entry?path=
// 返回条目全文。优先走本地索引（无 SSH 往返）；未命中时回源 NAS 读取。
func (s *Server) handleV1Entry(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPIToken(r) {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		jsonError(w, "缺少 path 参数", http.StatusBadRequest)
		return
	}

	if s.indexer != nil {
		doc, err := s.indexer.GetByPath(path)
		if err != nil {
			jsonError(w, "index read failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if doc != nil {
			jsonResp(w, map[string]interface{}{
				"path":     doc.Path,
				"nas_path": doc.NasPath,
				"title":    doc.Title,
				"tags":     doc.Tags,
				"type":     doc.Type,
				"time":     doc.Time,
				"content":  doc.Content,
				"source":   "index",
			})
			return
		}
	}

	// 回源：索引未命中（如图片/历史文件），按相对路径拼回 NAS 绝对路径读取
	full := path
	if !strings.HasPrefix(path, "/") {
		full = filepath.Join(s.cfg.SSH.BasePath, path)
	}
	data, err := s.sshClient.ReadFile(full)
	if err != nil {
		jsonError(w, "entry not found", http.StatusNotFound)
		return
	}
	jsonResp(w, map[string]interface{}{
		"path":     path,
		"nas_path": full,
		"content":  string(data),
		"source":   "ssh",
	})
}

// handleV1Tags GET /api/v1/tags
// 聚合索引内全部标签及出现次数。
func (s *Server) handleV1Tags(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPIToken(r) {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.indexer == nil {
		jsonError(w, "index not available", http.StatusServiceUnavailable)
		return
	}
	tags, err := s.indexer.AllTags()
	if err != nil {
		jsonError(w, "tags failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if tags == nil {
		tags = []index.TagCount{}
	}
	jsonResp(w, map[string]interface{}{"count": len(tags), "tags": tags})
}

// handleV1Status GET /api/v1/status
// 索引健康状态：条目数，供外部服务探活与对账。
func (s *Server) handleV1Status(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPIToken(r) {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	resp := map[string]interface{}{"api": "v1", "index": "disabled"}
	if s.indexer != nil {
		n, err := s.indexer.Count()
		if err != nil {
			jsonError(w, "count failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resp = map[string]interface{}{"api": "v1", "index": "ok", "entries": n}
	}
	jsonResp(w, resp)
}

// handleV1Backfill POST /api/v1/admin/backfill
// 触发一次后台回溯：遍历 NAS 归档，把全部 md/txt 灌入本地索引（幂等）。
func (s *Server) handleV1Backfill(w http.ResponseWriter, r *http.Request) {
	if !s.requireAPIToken(r) {
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if s.indexer == nil {
		jsonError(w, "index not available", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, "Method not allowed, use POST", http.StatusMethodNotAllowed)
		return
	}
	go RunBackfill(s.cfg, s.sshClient, s.indexer)
	jsonResp(w, map[string]string{"status": "backfill started"})
}
