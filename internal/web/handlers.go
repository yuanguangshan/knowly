package web

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yuanguangshan/knas/internal/config"
	"github.com/yuanguangshan/knas/internal/history"
	"github.com/yuanguangshan/knas/internal/publisher"
	"github.com/yuanguangshan/knas/internal/ssh"
)

// serveIndex 返回前端页面
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// jsonResp 写入 JSON 响应
func jsonResp(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(data)
}

// jsonError 写入 JSON 错误
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// parseLogLine 解析日志行
func parseLogLine(line string) map[string]string {
	result := map[string]string{"raw": line}

	if len(line) >= 19 && line[4] == '/' && line[7] == '/' && line[10] == ' ' {
		result["time"] = line[:19]
		rest := line[19:]
		idx := strings.Index(rest, ": ")
		if idx >= 0 {
			rest = rest[idx+2:]
		}
		rest = strings.TrimSpace(rest)
		for _, level := range []string{"INFO", "WARN", "ERROR", "DEBUG"} {
			prefix := "[" + level + "] "
			if strings.HasPrefix(rest, prefix) {
				result["level"] = level
				result["message"] = rest[len(prefix):]
				return result
			}
		}
		result["message"] = rest
	}
	return result
}

// handleLogs 读取日志文件
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	limitStr := r.URL.Query().Get("limit")
	limit := 200
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
		limit = n
	}

	logPath := config.GetLogPath()
	f, err := os.Open(logPath)
	if err != nil {
		jsonError(w, "无法读取日志文件", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if level != "" && level != "all" {
			if !strings.Contains(line, "["+level+"]") {
				continue
			}
		}
		lines = append(lines, line)
	}

	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	result := make([]map[string]string, 0, len(lines))
	for _, line := range lines {
		result = append(result, parseLogLine(line))
	}

	jsonResp(w, result)
}

// handleLogStream SSE 实时日志流
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "不支持 SSE", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	logPath := config.GetLogPath()

	var offset int64
	if info, err := os.Stat(logPath); err == nil {
		offset = info.Size() - 5120
		if offset < 0 {
			offset = 0
		}
	}

	ctx := r.Context()
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			f, err := os.Open(logPath)
			if err != nil {
				continue
			}

			if info, err := f.Stat(); err == nil {
				if info.Size() < offset {
					offset = 0
				}
			}

			f.Seek(offset, io.SeekStart)
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "" {
					continue
				}
				data, _ := json.Marshal(parseLogLine(line))
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}

			if newOffset, err := f.Seek(0, io.SeekCurrent); err == nil {
				offset = newOffset
			}
			f.Close()
		}
	}
}

// handleArchiveList 列出归档目录
func (s *Server) handleArchiveList(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	fullPath := filepath.Join(s.cfg.SSH.BasePath, relPath)

	entries, err := s.sshClient.ListDir(fullPath)
	if err != nil {
		jsonError(w, fmt.Sprintf("无法列出目录: %v", err), http.StatusServiceUnavailable)
		return
	}

	jsonResp(w, entries)
}

// handleArchiveFile 读取归档文件内容
func (s *Server) handleArchiveFile(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		jsonError(w, "缺少 path 参数", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(s.cfg.SSH.BasePath, relPath)
	data, err := s.sshClient.ReadFile(fullPath)
	if err != nil {
		jsonError(w, fmt.Sprintf("无法读取文件: %v", err), http.StatusServiceUnavailable)
		return
	}

	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".gif":
		w.Header().Set("Content-Type", "image/gif")
	case ".md", ".txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	w.Write(data)
}

// handleArchiveDownload 下载归档文件
func (s *Server) handleArchiveDownload(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		jsonError(w, "缺少 path 参数", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(s.cfg.SSH.BasePath, relPath)
	data, err := s.sshClient.ReadFile(fullPath)
	if err != nil {
		jsonError(w, fmt.Sprintf("无法读取文件: %v", err), http.StatusServiceUnavailable)
		return
	}

	fileName := filepath.Base(relPath)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+fileName+"\"")

	ext := strings.ToLower(filepath.Ext(relPath))
	switch ext {
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	w.Write(data)
}

// handleHistory 读取本地历史记录
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	typeFilter := r.URL.Query().Get("type")
	tagFilter := r.URL.Query().Get("tag")
	limit := 50
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
		limit = n
	}

	entries, err := s.histStore.Recent(limit)
	if err != nil {
		jsonError(w, fmt.Sprintf("无法读取历史: %v", err), http.StatusInternalServerError)
		return
	}

	type histEntry struct {
		ID        string   `json:"id"`
		Content   string   `json:"content"`
		Type      string   `json:"type"`
		Timestamp string   `json:"timestamp"`
		NASPath   string   `json:"nas_path"`
		Tags      []string `json:"tags"`
	}

	var result []histEntry
	for _, e := range entries {
		if typeFilter != "" && typeFilter != "all" && e.Type != typeFilter {
			continue
		}
		if tagFilter != "" {
			found := false
			for _, t := range e.Tags {
				if t == tagFilter {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		result = append(result, histEntry{
			ID:        e.ID,
			Content:   e.Content,
			Type:      e.Type,
			Timestamp: e.Timestamp.Format("2006-01-02 15:04:05"),
			NASPath:   e.NASPath,
			Tags:      e.Tags,
		})
	}

	jsonResp(w, result)
}

// handleTags 返回所有标签及计数
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.histStore.AllTags()
	if err != nil {
		jsonError(w, fmt.Sprintf("获取标签失败: %v", err), http.StatusInternalServerError)
		return
	}
	if tags == nil {
		tags = []history.TagCount{}
	}
	jsonResp(w, tags)
}

// handleStatus 返回守护进程状态
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"ssh": map[string]string{
			"host":      s.cfg.SSH.Host,
			"user":      s.cfg.SSH.User,
			"port":      s.cfg.SSH.Port,
			"base_path": s.cfg.SSH.BasePath,
		},
	}

	pidPath := config.GetPidPath()
	if data, err := os.ReadFile(pidPath); err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			if proc, err := os.FindProcess(pid); err == nil && proc != nil {
				status["daemon_running"] = true
				status["pid"] = pid
			}
		}
	}

	histFile := filepath.Join(config.GetConfigDir(), "history.jsonl")
	if f, err := os.Open(histFile); err == nil {
		scanner := bufio.NewScanner(f)
		count := 0
		for scanner.Scan() {
			count++
		}
		f.Close()
		status["total_syncs"] = count
	}

	status["ssh_connected"] = s.sshClient != nil
	status["start_time"] = s.startTime.Format("2006-01-02 15:04:05")
	status["uptime"] = int64(time.Since(s.startTime).Seconds())
	status["pid"] = os.Getpid()

	// 发布渠道配置状态
	status["publishers"] = map[string]map[string]bool{
		"blog":    {"enabled": s.cfg.Blog.Enabled},
		"podcast": {"enabled": s.cfg.Podcast.Enabled},
		"ima":     {"enabled": s.cfg.IMA.Enabled && s.cfg.IMA.ClientID != "" && s.cfg.IMA.APIKey != ""},
	}

	jsonResp(w, status)
}

// handleRestart 重启 knas daemon 进程
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 读取 daemon PID
	pidPath := config.GetPidPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		jsonError(w, "守护进程未运行", http.StatusServiceUnavailable)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		jsonError(w, "无效的 PID 文件", http.StatusInternalServerError)
		return
	}

	// 停止 daemon
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		jsonError(w, fmt.Sprintf("停止守护进程失败: %v", err), http.StatusInternalServerError)
		return
	}

	jsonResp(w, map[string]string{"status": "restarting"})

	go func() {
		// 等待 daemon 完全停止
		time.Sleep(2 * time.Second)

		// 获取可执行文件路径并启动新 daemon
		exePath, err := os.Executable()
		if err != nil {
			log.Printf("[ERROR] restart: get exe path: %v", err)
			return
		}
		cmd := exec.Command(exePath, "--daemon")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			log.Printf("[ERROR] restart: start daemon: %v", err)
		}
	}()
}

// handlePublish 发布内容到 Blog/Podcast/IMA
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Content string   `json:"content"`
		Targets []string `json:"targets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "无效的请求体", http.StatusBadRequest)
		return
	}
	if req.Content == "" {
		jsonError(w, "内容不能为空", http.StatusBadRequest)
		return
	}

	type publishResult struct {
		Target string `json:"target"`
		OK     bool   `json:"ok"`
		Error  string `json:"error,omitempty"`
	}
	var results []publishResult

	for _, target := range req.Targets {
		var err error
		switch target {
		case "blog":
			err = publisher.PublishBlog(s.cfg.Blog, req.Content)
		case "podcast":
			err = publisher.PublishPodcast(s.cfg.Podcast, req.Content)
		case "ima":
			err = publisher.PublishIMA(s.cfg.IMA, req.Content)
		default:
			results = append(results, publishResult{Target: target, Error: "未知目标"})
			continue
		}
		if err != nil {
			results = append(results, publishResult{Target: target, OK: false, Error: err.Error()})
		} else {
			results = append(results, publishResult{Target: target, OK: true})
		}
	}

	jsonResp(w, results)
}

// handleStats 返回统计数据
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.histStore.Stats()
	if err != nil {
		jsonError(w, fmt.Sprintf("获取统计失败: %v", err), http.StatusInternalServerError)
		return
	}
	jsonResp(w, stats)
}
// handleSearch 全文搜索归档内容
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	keyword := r.URL.Query().Get("q")
	if keyword == "" {
		jsonError(w, "缺少搜索关键词", http.StatusBadRequest)
		return
	}
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
		limit = n
	}

	results, err := s.sshClient.Search(keyword, limit)
	if err != nil {
		jsonError(w, fmt.Sprintf("搜索失败: %v", err), http.StatusServiceUnavailable)
		return
	}
	if results == nil {
		results = []ssh.SearchResult{}
	}
	jsonResp(w, results)
}
