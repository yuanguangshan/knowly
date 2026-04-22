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

	"github.com/yuanguangshan/knowly/internal/config"
	"github.com/yuanguangshan/knowly/internal/history"
	"github.com/yuanguangshan/knowly/internal/publisher"
	"github.com/yuanguangshan/knowly/internal/ssh"
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

	// Start from current end of file, don't send historical logs
	// (frontend loads history separately via /api/logs)
	var offset int64
	if info, err := os.Stat(logPath); err == nil {
		offset = info.Size()
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
			}
		}
	}

	histFile := filepath.Join(config.GetConfigDir(), "history.jsonl")
	if f, err := os.Open(histFile); err == nil {
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line
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

	// 读取版本号
	sourceDir := findProjectRoot()
	if sourceDir != "" {
		if pkgData, err := os.ReadFile(filepath.Join(sourceDir, "package.json")); err == nil {
			var pkg struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(pkgData, &pkg) == nil {
				status["version"] = pkg.Version
			}
		}
	}

	// 发布渠道配置状态
	status["publishers"] = map[string]map[string]bool{
		"blog":    {"enabled": s.cfg.Blog.Enabled},
		"podcast": {"enabled": s.cfg.Podcast.Enabled},
		"ima":     {"enabled": s.cfg.IMA.Enabled && s.cfg.IMA.ClientID != "" && s.cfg.IMA.APIKey != ""},
	}

	jsonResp(w, status)
}

// handleRestart 重启 knowly daemon 进程
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

	// 获取可执行文件路径
	exePath, err := os.Executable()
	if err != nil {
		jsonError(w, fmt.Sprintf("获取可执行文件路径失败: %v", err), http.StatusInternalServerError)
		return
	}

	jsonResp(w, map[string]string{"status": "restarting"})

	// 用 setsid 启动一个独立的 shell 脚本：先等旧进程退出，再启动新 daemon
	// 这样即使当前进程被 SIGTERM 杀掉，重启脚本仍会继续运行
	script := fmt.Sprintf(
		"kill -TERM %d; timeout 10 sh -c 'while kill -0 %d 2>/dev/null; do sleep 0.2; done' || kill -9 %d 2>/dev/null; sleep 0.5; exec %s --daemon",
		pid, pid, pid, exePath,
	)
	restartCmd := exec.Command("/bin/sh", "-c", script)
	restartCmd.Stdin = nil
	restartCmd.Stdout = nil
	restartCmd.Stderr = nil
	restartCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := restartCmd.Start(); err != nil {
		log.Printf("[ERROR] restart: start restart script: %v", err)
	}
}

// handleUpdate 从源码编译并替换二进制文件，然后重启服务
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 查找源码目录：从当前工作目录向上查找 go.mod
	sourceDir := findProjectRoot()
	if sourceDir == "" {
		jsonError(w, "找不到项目源码目录（未检测到 go.mod）", http.StatusInternalServerError)
		return
	}

	currentExe, err := os.Executable()
	if err != nil {
		jsonError(w, "获取当前可执行文件路径失败", http.StatusInternalServerError)
		return
	}
	tmpBinary := "knowly-update-tmp"

	// 设置 SSE
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher := w.(http.Flusher)

	sendEvent := func(step, msg string) {
		data, _ := json.Marshal(map[string]string{"step": step, "message": msg})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	sendEvent("building", "编译中...")
	cmd := exec.Command("go", "build", "-o", tmpBinary, "./cmd/knowly")
	cmd.Dir = sourceDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		sendEvent("error", fmt.Sprintf("编译失败: %s\n%s", err, string(output)))
		return
	}

	sendEvent("stopping", "停止服务...")
	pidData, err := os.ReadFile(config.GetPidPath())
	if err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(pidData))); err == nil {
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	time.Sleep(1 * time.Second)

	sendEvent("replacing", "替换二进制文件...")
	built := filepath.Join(sourceDir, tmpBinary)
	if err := os.Rename(built, currentExe); err != nil {
		sendEvent("error", fmt.Sprintf("替换失败: %v", err))
		return
	}

	sendEvent("starting", "启动服务...")

	// fork 新进程然后退出当前进程
	exePath, err := os.Executable()
	if err != nil {
		sendEvent("error", fmt.Sprintf("获取路径失败: %v", err))
		return
	}
	newCmd := exec.Command(exePath, "--daemon")
	newCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	newCmd.Stdin = nil
	newCmd.Stdout = nil
	newCmd.Stderr = nil
	if err := newCmd.Start(); err != nil {
		sendEvent("error", fmt.Sprintf("启动失败: %v", err))
		return
	}

	sendEvent("done", "更新完成，页面即将刷新")

	// 延迟退出当前进程，让 SSE 响应发完
	go func() {
		time.Sleep(1 * time.Second)
		os.Exit(0)
	}()
}

// findProjectRoot 从当前目录向上查找包含 go.mod 的目录
func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// handleRelease 版本发布：git push → npm version minor → git push --tags
func (s *Server) handleRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sourceDir := findProjectRoot()
	if sourceDir == "" {
		jsonError(w, "找不到项目源码目录", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher := w.(http.Flusher)

	sendEvent := func(step, msg string) {
		data, _ := json.Marshal(map[string]string{"step": step, "message": msg})
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	run := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = sourceDir
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}

	// 1. git add & commit
	sendEvent("commit", "提交代码...")
	out, err := run("git", "add", "-A")
	if err != nil {
		sendEvent("error", "git add 失败: "+out)
		return
	}
	// 检查是否有变更
	out, _ = run("git", "diff", "--cached", "--quiet")
	// diff --cached --quiet 返回非0表示有变更
	out, err = run("git", "commit", "-m", "release")
	if err != nil && !strings.Contains(err.Error(), "nothing to commit") {
		// nothing to commit 也算正常
		sendEvent("commit", "无代码变更")
	} else {
		sendEvent("commit", "代码已提交")
	}

	// 2. git push
	sendEvent("push", "推送到远程...")
	if out, err = run("git", "push"); err != nil {
		sendEvent("error", "git push 失败: "+out)
		return
	}
	sendEvent("push", "推送完成")

	// 3. npm version minor
	sendEvent("version", "升级版本号...")
	if out, err = run("npm", "version", "minor"); err != nil {
		sendEvent("error", "npm version 失败: "+out)
		return
	}
	sendEvent("version", "版本: "+out)

	// 4. git push --tags
	sendEvent("tags", "推送标签...")
	if out, err = run("git", "push", "--tags"); err != nil {
		sendEvent("error", "git push --tags 失败: "+out)
		return
	}

	sendEvent("done", "发布完成")
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
