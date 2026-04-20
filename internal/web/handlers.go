package web

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yuanguangshan/knas/internal/config"
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
		ID        string `json:"id"`
		Content   string `json:"content"`
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		NASPath   string `json:"nas_path"`
	}

	var result []histEntry
	for _, e := range entries {
		if typeFilter != "" && typeFilter != "all" && e.Type != typeFilter {
			continue
		}
		result = append(result, histEntry{
			ID:        e.ID,
			Content:   e.Content,
			Type:      e.Type,
			Timestamp: e.Timestamp.Format("2006-01-02 15:04:05"),
			NASPath:   e.NASPath,
		})
	}

	jsonResp(w, result)
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

	jsonResp(w, status)
}

// handleRestart 重启 knas 进程
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	exePath, err := os.Executable()
	if err != nil {
		jsonError(w, "无法获取可执行文件路径", http.StatusInternalServerError)
		return
	}

	jsonResp(w, map[string]string{"status": "restarting"})

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	go func() {
		time.Sleep(500 * time.Millisecond)
		syscall.Exec(exePath, os.Args, os.Environ())
	}()
}
