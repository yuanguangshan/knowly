package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
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

	"github.com/yuanguangshan/knowly/internal/ai"
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
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	tmpl, err := template.New("index").Parse(string(indexHTML))
	if err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	tmpl.Execute(w, map[string]interface{}{
		"RefreshSec":    s.cfg.Web.RefreshSec,
		"LogRefreshSec": s.cfg.Web.LogRefreshSec,
	})
}

// serveFavicon 返回空响应，避免 404 报错
func (s *Server) serveFavicon(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// serveManifest 返回 PWA manifest.json
func (s *Server) serveManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(manifestJSON)
}

// serveSW 返回 Service Worker
func (s *Server) serveSW(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(swJS)
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
		rest := strings.TrimSpace(line[19:])
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

// handleLogs 读取日志文件，从末尾逆向读取最后 N 行
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	level := r.URL.Query().Get("level")
	limitStr := r.URL.Query().Get("limit")
	limit := 300
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
		limit = n
	}

	logPath := config.GetLogPath()
	lines, err := tailFile(logPath, limit)
	if err != nil {
		jsonError(w, "无法读取日志文件", http.StatusInternalServerError)
		return
	}

	// 按级别过滤
	if level != "" && level != "all" {
		filtered := make([]string, 0, len(lines))
		pat := "[" + level + "]"
		for _, line := range lines {
			if strings.Contains(line, pat) {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}

	// 合并多行日志
	merged := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(line) >= 19 && line[4] == '/' && line[7] == '/' && line[10] == ' ' {
			merged = append(merged, line)
		} else if len(merged) > 0 {
			merged[len(merged)-1] += "\n" + line
		}
	}

	result := make([]map[string]string, 0, len(merged))
	for _, line := range merged {
		result = append(result, parseLogLine(line))
	}

	jsonResp(w, result)
}

// tailFile 从文件末尾逆向读取最后 N 行
func tailFile(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	fileSize := fi.Size()
	if fileSize == 0 {
		return nil, nil
	}

	var lines []string
	var tailBuf []byte
	remaining := fileSize
	const blockSize = int64(4096)
	bufPool := make([]byte, 0, blockSize)

	for remaining > 0 && len(lines) < n {
		readSize := blockSize
		if remaining < readSize {
			readSize = remaining
		}
		remaining -= readSize

		bufPool = bufPool[:readSize]
		if _, err := f.Seek(remaining, 0); err != nil {
			return nil, err
		}
		if _, err := f.Read(bufPool); err != nil {
			return nil, err
		}

		combined := make([]byte, int(readSize)+len(tailBuf))
		copy(combined, bufPool)
		copy(combined[readSize:], tailBuf)
		tailBuf = nil

		data := combined
		for i := len(data) - 1; i >= 0 && len(lines) < n; i-- {
			if data[i] == '\n' {
				if i < len(data)-1 {
					line := strings.TrimRight(string(data[i+1:]), "\r")
					if line != "" {
						lines = append(lines, line)
					}
				}
				data = data[:i]
			}
		}
		if len(data) > 0 {
			tailBuf = data
		}
	}

	if len(tailBuf) > 0 && len(lines) < n {
		line := strings.TrimRight(string(tailBuf), "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}

	// 已是最新在前的顺序（从文件末尾逆向读取）
	return lines, nil
}

// handleLogStream SSE 实时日志流（内存推送 + 文件轮询兜底）
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

	// 从文件末尾开始，不发送历史日志（前端通过 /api/logs 加载）
	var offset int64
	if info, err := os.Stat(logPath); err == nil {
		offset = info.Size()
	}

	ctx := r.Context()
	logCh := s.SubscribeLog()
	defer s.UnsubscribeLog(logCh)

	// 文件轮询兜底（2s），防止行堆积在文件缓冲区
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()

	sendLine := func(raw string) {
		parsed := parseLogLine(raw)
		data, _ := json.Marshal(parsed)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	pollFile := func() {
		if info, err := f.Stat(); err == nil {
			if info.Size() <= offset {
				return
			}
			if info.Size() < offset {
				offset = 0
			}
		}
		f.Seek(offset, io.SeekStart)
		scanner := bufio.NewScanner(f)
		var pending string
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if len(line) >= 19 && line[4] == '/' && line[7] == '/' && line[10] == ' ' {
				if pending != "" {
					sendLine(pending)
				}
				pending = line
			} else if pending != "" {
				pending += "\n" + line
			}
		}
		if pending != "" {
			sendLine(pending)
		}
		if newOffset, err := f.Seek(0, io.SeekCurrent); err == nil {
			offset = newOffset
		}
	}

	// 首次先轮询一次，拉取连接期间的日志
	pollFile()

	for {
		select {
		case <-ctx.Done():
			return
		case line := <-logCh:
			// 实时推送：内存缓冲区，零延迟
			sendLine(line)
		case <-ticker.C:
			pollFile()
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

	// 为 .md 文件提取 frontmatter 标题（一次性批量获取）
	titles := s.sshClient.BatchExtractTitles(fullPath, entries)
	titleMap := make(map[string]string)
	for _, t := range titles {
		titleMap[t.Name] = t.Title
	}
	for i := range entries {
		if !entries[i].IsDir && strings.HasSuffix(strings.ToLower(entries[i].Name), ".md") {
			if t, ok := titleMap[entries[i].Name]; ok {
				entries[i].Title = t
			}
		}
	}

	jsonResp(w, entries)
}


// handleArchiveToday 一次性返回归档初始化数据（年/月/日列表 + 当日文件）
// 避免前端首次加载时串行 4 次 SSH 请求
func (s *Server) handleArchiveToday(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	year := fmt.Sprintf("%04d", now.Year())
	month := fmt.Sprintf("%02d", int(now.Month()))
	day := fmt.Sprintf("%02d", now.Day())

	type archiveTodayResp struct {
		Years   []ssh.DirEntry `json:"years"`
		Months  []ssh.DirEntry `json:"months"`
		Days    []ssh.DirEntry `json:"days"`
		Files   []ssh.DirEntry `json:"files"`
		Year    string         `json:"year"`
		Month   string         `json:"month"`
		Day     string         `json:"day"`
	}

	resp := archiveTodayResp{
		Year:  year,
		Month: month,
		Day:   day,
	}

	basePath := s.cfg.SSH.BasePath

	// 并行发起 SSH 请求
	type listResult struct {
		key     string
		entries []ssh.DirEntry
		err     error
	}
	ch := make(chan listResult, 4)

	listAsync := func(key, relPath string) {
		entries, err := s.sshClient.ListDir(filepath.Join(basePath, relPath))
		ch <- listResult{key: key, entries: entries, err: err}
	}

	go listAsync("years", "")
	go listAsync("months", year)
	go listAsync("days", year+"/"+month)
	go listAsync("files", year+"/"+month+"/"+day)

	for i := 0; i < 4; i++ {
		r := <-ch
		if r.err != nil {
			// 如果某级目录不存在（例如当天还没有归档），跳过
			log.Printf("[DEBUG] archiveToday: %s list failed: %v", r.key, r.err)
			continue
		}
		switch r.key {
		case "years":
			resp.Years = r.entries
		case "months":
			resp.Months = r.entries
		case "days":
			resp.Days = r.entries
		case "files":
				// 为 .md 文件提取 frontmatter 标题
				files := r.entries
				titles := s.sshClient.BatchExtractTitles(year+"/"+month+"/"+day, files)
				titleMap := make(map[string]string)
				for _, t := range titles {
					titleMap[t.Name] = t.Title
				}
				for i := range files {
					if !files[i].IsDir && strings.HasSuffix(strings.ToLower(files[i].Name), ".md") {
						if t, ok := titleMap[files[i].Name]; ok {
							files[i].Title = t
						}
					}
				}
				resp.Files = files
		}
	}

	jsonResp(w, resp)
}

// contentTypeByExt 根据文件扩展名返回 Content-Type
func contentTypeByExt(ext string) string {
	if ext == "" {
		return "application/octet-stream"
	}
	ext = strings.ToLower(ext)
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".pdf":
		return "application/pdf"
	case ".mp3":
		return "audio/mpeg"
	case ".mp4":
		return "video/mp4"
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".webm":
		return "video/webm"
	case ".zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}

// handleArchiveFile 流式读取归档文件内容
func (s *Server) handleArchiveFile(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		jsonError(w, "缺少 path 参数", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(s.cfg.SSH.BasePath, relPath)

	// 检查文件大小
	maxSize := s.cfg.Web.MaxDownloadSize
	size, err := s.sshClient.FileSize(fullPath)
	if err != nil {
		jsonError(w, fmt.Sprintf("无法读取文件信息: %v", err), http.StatusServiceUnavailable)
		return
	}
	if size > maxSize {
		jsonError(w, fmt.Sprintf("文件过大（%d bytes），超过下载限制（%d bytes）", size, maxSize), http.StatusRequestEntityTooLarge)
		return
	}

	ext := strings.ToLower(filepath.Ext(relPath))
	w.Header().Set("Content-Type", contentTypeByExt(ext))

	if err := s.sshClient.ReadFileToWriter(fullPath, w); err != nil {
		log.Printf("[ERROR] failed to stream file %s: %v", fullPath, err)
	}
}

// handleArchiveDownload 流式下载归档文件
func (s *Server) handleArchiveDownload(w http.ResponseWriter, r *http.Request) {
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		jsonError(w, "缺少 path 参数", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(s.cfg.SSH.BasePath, relPath)

	// 检查文件大小
	maxSize := s.cfg.Web.MaxDownloadSize
	size, err := s.sshClient.FileSize(fullPath)
	if err != nil {
		jsonError(w, fmt.Sprintf("无法读取文件信息: %v", err), http.StatusServiceUnavailable)
		return
	}
	if size > maxSize {
		jsonError(w, fmt.Sprintf("文件过大（%d bytes），超过下载限制（%d bytes）", size, maxSize), http.StatusRequestEntityTooLarge)
		return
	}

	fileName := filepath.Base(relPath)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+fileName+"\"")
	w.Header().Set("Content-Type", contentTypeByExt(filepath.Ext(relPath)))

	if err := s.sshClient.ReadFileToWriter(fullPath, w); err != nil {
		log.Printf("[ERROR] failed to stream file %s: %v", fullPath, err)
	}
}

// handleHistory 读取本地历史记录
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	typeFilter := r.URL.Query().Get("type")
	tagFilter := r.URL.Query().Get("tag")
	afterStr := r.URL.Query().Get("after")
	limit := 50
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
		limit = n
	}

	var entries []history.Entry
	var err error

	// 有标签过滤时读取全部记录，因为目标条目可能不在最近 N 条中
	if tagFilter != "" {
		entries, err = s.histStore.ReadAll()
		if err != nil {
			jsonError(w, fmt.Sprintf("无法读取历史: %v", err), http.StatusInternalServerError)
			return
		}
		// 倒序：最新的在前面
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	} else if afterStr != "" {
		// 分页：加载指定时间戳之前的条目
		afterTime, parseErr := time.Parse("2006-01-02 15:04:05", afterStr)
		if parseErr != nil {
			jsonError(w, "无效的 after 参数格式", http.StatusBadRequest)
			return
		}
		entries, err = s.histStore.RecentAfter(afterTime, limit)
	} else {
		entries, err = s.histStore.Recent(limit)
	}

	if err != nil {
		jsonError(w, fmt.Sprintf("无法读取历史: %v", err), http.StatusInternalServerError)
		return
	}

	type histEntry struct {
		ID         string   `json:"id"`
		Content    string   `json:"content"`
		Type       string   `json:"type"`
		Timestamp  string   `json:"timestamp"`
		NASPath    string   `json:"nas_path"`
		Tags       []string `json:"tags"`
		Title      string   `json:"title,omitempty"`
		ManualEdit bool     `json:"manual_edit"`
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
		// 标题优先使用用户设置的 Title，回退到 PublishTitle
		title := e.Title
		if title == "" {
			title = e.PublishTitle
		}
		result = append(result, histEntry{
			ID:         e.ID,
			Content:    e.Content,
			Type:       e.Type,
			Timestamp:  e.Timestamp.Format("2006-01-02 15:04:05"),
			NASPath:    e.NASPath,
			Tags:       e.Tags,
			Title:      title,
			ManualEdit: e.ManualEdit,
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

	// Web 服务能运行就说明守护进程在跑
	status["daemon_running"] = true

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
		"kindle":  {"enabled": s.cfg.Kindle.Enabled && s.cfg.Kindle.SenderEmail != "" && s.cfg.Kindle.SenderPassword != ""},
	}

	jsonResp(w, status)
}

// restartDaemonScript 生成重启 daemon 的 shell 脚本
func (s *Server) restartDaemonScript(pid int, exePath string) string {
	return "knowly stop 2>/dev/null; sleep 1; knowly start"
}

// handleRestart 重启 knowly daemon 进程
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	pidPath := config.GetPidPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		// PID 文件不存在时仍尝试重启
		exePath, err := os.Executable()
		if err != nil {
			jsonError(w, "无法获取可执行文件路径", http.StatusInternalServerError)
			return
		}
		jsonResp(w, map[string]string{"status": "restarting"})
		script := s.restartDaemonScript(0, exePath)
		exec.Command("/bin/sh", "-c", script).Start()
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		jsonError(w, "无效的 PID 文件", http.StatusInternalServerError)
		return
	}
	exePath, err := os.Executable()
	if err != nil {
		jsonError(w, "获取可执行文件路径失败", http.StatusInternalServerError)
		return
	}

	jsonResp(w, map[string]string{"status": "restarting"})

	script := s.restartDaemonScript(pid, exePath)
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

	sendEvent("replacing", "替换二进制文件...")
	built := filepath.Join(sourceDir, tmpBinary)
	if err := os.Rename(built, currentExe); err != nil {
		sendEvent("error", fmt.Sprintf("替换失败: %v", err))
		return
	}

	// 提交并推送到远程
	sendEvent("pushing", "提交并推送到远程...")
	gitRun := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = sourceDir
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	gitRun("git", "add", "-A")
	if _, err := gitRun("git", "diff", "--cached", "--quiet"); err != nil {
		// 有变更，提交并推送
		if out, err := gitRun("git", "commit", "-m", "release"); err != nil && !strings.Contains(err.Error(), "nothing to commit") {
			sendEvent("pushing", "提交失败: "+out)
		} else if out, err := gitRun("git", "push"); err != nil {
			sendEvent("pushing", "推送失败: "+out)
		} else {
			sendEvent("pushing", "已提交并推送到远程")
		}
	} else {
		sendEvent("pushing", "无代码变更")
	}

	// 使用独立 shell 脚本重启（和 handleRestart 相同方式）
	// 脚本在独立进程组中运行，不受当前进程退出影响
	exePath, err := os.Executable()
	if err != nil {
		sendEvent("error", fmt.Sprintf("获取路径失败: %v", err))
		return
	}

	// 读取 PID，失败时仍尝试重启（传 0 让脚本跳过发信号）
	var pid int
	if pidData, err := os.ReadFile(config.GetPidPath()); err == nil {
		pid, _ = strconv.Atoi(strings.TrimSpace(string(pidData)))
	}

	script := s.restartDaemonScript(pid, exePath)
	restartCmd := exec.Command("/bin/sh", "-c", script)
	restartCmd.Stdin = nil
	restartCmd.Stdout = nil
	restartCmd.Stderr = nil
	restartCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := restartCmd.Start(); err != nil {
		sendEvent("error", fmt.Sprintf("启动重启脚本失败: %v", err))
		return
	}

	sendEvent("done", "更新完成，页面即将刷新")
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

	rawContent := req.Content

	// AI 生成标题和摘要（微信和播客只发原文，不加 AI 解读）
	var aiContent string
	var aiTitle string
	// 除了微信，其他目标都需要 AI 标题（播客也需要标题）
	needsAI := false
	for _, t := range req.Targets {
		if t != "wechat" {
			needsAI = true
			break
		}
	}
	if needsAI && s.aiProcessor != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if result := s.aiProcessor.GenerateTitleAndSummary(ctx, rawContent); result != nil {
			aiTitle = result.Title
			if aiTitle != "" {
				var header strings.Builder
				if result.Summary != "" {
					header.WriteString("> " + result.Summary + "\n\n")
				}
				header.WriteString("---\n\n")
				aiContent = header.String() + rawContent
				log.Printf("[INFO] AI generated title: %q for publish", aiTitle)
			}
		}
		if aiTitle == "" {
			log.Printf("[INFO] AI title not generated, will use content-based title")
		}
	}
	if aiContent == "" {
		aiContent = rawContent
	}

	// 异步执行发布（AI 标题生成可能较慢，先返回响应）
	go func() {
		for _, target := range req.Targets {
			content := aiContent
			if target == "wechat" || target == "podcast-read" || target == "podcast-multi" {
				content = rawContent
			}

			switch target {
			case "blog":
				if s.cfg.Blog.APIURL == "" {
					log.Printf("[WARN] Blog publish skipped: API URL not configured")
				} else {
					publisher.PublishBlog(s.cfg.Blog, content, aiTitle)
				}
			case "podcast":
				if s.cfg.Podcast.APIURL == "" {
					log.Printf("[WARN] Podcast publish skipped: API URL not configured")
				} else {
					publisher.PublishPodcast(s.cfg.Podcast, content, aiTitle)
				}
			case "ima":
				if s.cfg.IMA.APIURL == "" || s.cfg.IMA.ClientID == "" || s.cfg.IMA.APIKey == "" {
					log.Printf("[WARN] IMA publish skipped: incomplete config")
				} else {
					publisher.PublishIMA(s.cfg.IMA, content, aiTitle)
				}
			case "kindle":
				if s.cfg.Kindle.KindleEmail == "" || s.cfg.Kindle.SenderEmail == "" || s.cfg.Kindle.SenderPassword == "" {
					log.Printf("[WARN] Kindle publish skipped: incomplete config")
				} else {
					publisher.PublishKindle(s.cfg.Kindle, content, aiTitle)
				}
			case "wechat", "podcast-read", "podcast-multi":
				cleanContent := rawContent
				if idx := strings.Index(cleanContent, "### 原始内容"); idx >= 0 {
					cleanContent = strings.TrimSpace(cleanContent[idx+len("### 原始内容"):])
				}
				webhookTitle := ""
				if target != "wechat" {
					webhookTitle = aiTitle
					if webhookTitle == "" {
						if idx := strings.Index(cleanContent, "\n# "); idx >= 0 {
							end := strings.Index(cleanContent[idx+1:], "\n")
							if end > 0 {
								webhookTitle = strings.TrimSpace(cleanContent[idx+3 : idx+1+end])
							}
						}
					}
				}
				if !s.cfg.Webhook.Enabled {
					log.Printf("[WARN] %s publish skipped: webhook not enabled", target)
				} else {
					for _, t := range s.cfg.Webhook.Targets {
						if t.MsgType == target {
							if e := publisher.PublishWebhookTargetWithTitle(t, cleanContent, webhookTitle); e != nil {
								log.Printf("[WARN] %s publish failed: %v", target, e)
							}
							break
						}
					}
				}
			}
		}
	}()

	jsonResp(w, map[string]string{"status": "accepted"})
}

// handleTagAndPublish 添加标签并发布内容
// 注意：此接口不受全局 enabled 配置限制，允许手动触发发布
func (s *Server) handleTagAndPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID     string `json:"id"`
		Tag    string `json:"tag"`
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "无效的请求体", http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.Tag == "" || req.Target == "" {
		jsonError(w, "缺少必要参数", http.StatusBadRequest)
		return
	}

	// 验证目标是否有效
	validTargets := map[string]bool{
		"ima":           true,
		"blog":          true,
		"kindle":        true,
		"podcast":       true,
		"podcast-read":  true,
		"podcast-multi": true,
	}
	if !validTargets[req.Target] {
		jsonError(w, "无效的发布目标", http.StatusBadRequest)
		return
	}

	// 获取历史条目
	entry, err := s.histStore.GetByID(req.ID)
	if err != nil {
		jsonError(w, fmt.Sprintf("找不到条目: %v", err), http.StatusNotFound)
		return
	}

	// 添加标签（先添加标签，即使发布失败也保留标签）
	if err := s.histStore.UpdateTags(req.ID, []string{req.Tag}); err != nil {
		jsonError(w, fmt.Sprintf("添加标签失败: %v", err), http.StatusInternalServerError)
		return
	}

	// 获取完整内容
	content := entry.Content

	// 如果历史记录中只有预览内容，从 NAS 读取完整内容
	if entry.NASPath != "" && s.sshClient != nil {
		// NASPath 已经是完整绝对路径，直接读取
		data, err := s.sshClient.ReadFile(entry.NASPath)
		if err != nil {
			// NAS 读取失败，使用预览内容
			log.Printf("[WARN] Failed to read NAS file %s: %v, using preview content", entry.NASPath, err)
		} else {
			content = string(data)
		}
	}

	// 如果仍然没有内容，返回错误
	if content == "" {
		jsonError(w, "内容为空，无法发布", http.StatusBadRequest)
		return
	}

	// 优先使用同步时预生成的标题/摘要，避免重复 AI 调用
	var aiTitle, aiSummary string
	if entry.PublishTitle != "" {
		aiTitle = entry.PublishTitle
		aiSummary = entry.PublishSummary
		log.Printf("[INFO] Using cached publish title for %s", req.ID)
	} else if s.aiProcessor != nil {
		// 回退：旧数据没有预生成标题时才调 AI
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		var result *ai.TitleAndSummary = s.aiProcessor.GenerateTitleAndSummary(ctx, content)
		if result != nil {
			aiTitle = result.Title
			aiSummary = result.Summary
			log.Printf("[INFO] AI generated title and summary for %s", req.ID)
		} else {
			log.Printf("[WARN] AI title generation failed for %s, using original content", req.ID)
		}
	}

	// 先去掉原始 frontmatter（NAS 文件自带），再加 AI 摘要
	content = stripFrontmatter(content)
	if aiSummary != "" {
		var header strings.Builder
		header.WriteString("> " + aiSummary + "\n\n")
		header.WriteString("---\n\n")
		content = header.String() + content
	}

	// 异步发布内容（后台处理，先返回响应）
	go func() {
		switch req.Target {
		case "blog":
			if s.cfg.Blog.APIURL == "" {
				log.Printf("[WARN] Blog publish skipped: API URL not configured")
			} else {
				publisher.PublishBlog(s.cfg.Blog, content, aiTitle)
			}
		case "podcast":
			if s.cfg.Podcast.APIURL == "" {
				log.Printf("[WARN] Podcast publish skipped: API URL not configured")
			} else {
				publisher.PublishPodcast(s.cfg.Podcast, content, aiTitle)
			}
		case "ima":
			if s.cfg.IMA.APIURL == "" || s.cfg.IMA.ClientID == "" || s.cfg.IMA.APIKey == "" {
				log.Printf("[WARN] IMA publish skipped: incomplete config")
			} else {
				publisher.PublishIMA(s.cfg.IMA, content, aiTitle)
			}
		case "kindle":
			if s.cfg.Kindle.KindleEmail == "" || s.cfg.Kindle.SenderEmail == "" || s.cfg.Kindle.SenderPassword == "" {
				log.Printf("[WARN] Kindle publish skipped: incomplete config")
			} else {
				publisher.PublishKindle(s.cfg.Kindle, content, aiTitle)
			}
		case "podcast-read", "podcast-multi":
			if s.cfg.Webhook.Enabled {
				for _, t := range s.cfg.Webhook.Targets {
					if t.MsgType == req.Target {
						if e := publisher.PublishWebhookTargetWithTitle(t, content, aiTitle); e != nil {
							log.Printf("[WARN] %s publish failed: %v", req.Target, e)
						}
						break
					}
				}
			}
		}
	}()

	jsonResp(w, map[string]interface{}{
		"tag_added": true,
		"target":    req.Target,
		"published": true,
	})
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

// handleAIConfig 处理 AI 配置的读取和更新（GET/PUT）
func (s *Server) handleAIConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetAIConfig(w, r)
	case http.MethodPut:
		s.handleUpdateAIConfig(w, r)
	default:
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetAIConfig 返回当前 AI 配置（API Key 脱敏）
func (s *Server) handleGetAIConfig(w http.ResponseWriter, r *http.Request) {
	ai := s.cfg.AI

	// 构建 presets 列表
	presets := make(map[string]config.AIPresetOption)
	for k, v := range config.AIPresets {
		presets[k] = v
	}

	// 构建 prompt templates
	templates := make(map[string]string)
	for k, v := range config.AIPromptTemplates {
		templates[k] = v
	}

	// 脱敏 API Key
	masked := ai
	if len(masked.APIKey) > 4 {
		masked.APIKey = "****" + masked.APIKey[len(masked.APIKey)-4:]
	} else if masked.APIKey != "" {
		masked.APIKey = "****"
	}

	jsonResp(w, map[string]interface{}{
		"config":           masked,
		"presets":          presets,
		"prompt_templates": templates,
	})
}

// handleUpdateAIConfig 更新 AI 配置
func (s *Server) handleUpdateAIConfig(w http.ResponseWriter, r *http.Request) {
	var newCfg config.AIConfig
	if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
		jsonError(w, "无效的请求体", http.StatusBadRequest)
		return
	}

	// 预设解析：填充 endpoint 和 model
	if newCfg.Preset != "" && newCfg.Preset != "custom" {
		if p, ok := config.AIPresets[newCfg.Preset]; ok {
			if newCfg.Endpoint == "" {
				newCfg.Endpoint = p.Endpoint
			}
			if newCfg.Model == "" {
				newCfg.Model = p.Model
			}
		}
	}

	// 验证
	if newCfg.Enabled && newCfg.Endpoint == "" {
		jsonError(w, "启用 AI 时 endpoint 不能为空", http.StatusBadRequest)
		return
	}

	// 保留原有 API Key（如果传来的是脱敏值或空值）
	if newCfg.APIKey == "" || strings.HasPrefix(newCfg.APIKey, "****") {
		newCfg.APIKey = s.cfg.AI.APIKey
	}

	// 补全默认值
	if newCfg.MinContentLen == 0 {
		newCfg.MinContentLen = 100
	}
	if newCfg.MaxContentLen == 0 {
		newCfg.MaxContentLen = 10000
	}
	if newCfg.Timeout == 0 {
		newCfg.Timeout = 60
	}

	// 更新内存中的配置
	s.cfg.AI = newCfg

	// 持久化到磁盘
	if err := config.Save(s.cfg); err != nil {
		jsonError(w, fmt.Sprintf("保存配置失败: %v", err), http.StatusInternalServerError)
		return
	}

	promptMode := newCfg.PromptTemplate
	if promptMode == "" {
		if newCfg.Prompt == "" {
			promptMode = "默认"
		} else {
			promptMode = "自定义"
		}
	}
	log.Printf("[INFO] AI config updated (preset: %s, model: %s, endpoint: %s, prompt: %s)", newCfg.Preset, newCfg.Model, newCfg.Endpoint, promptMode)
	jsonResp(w, map[string]string{"status": "saved"})
}

// handleConfig 处理完整配置的读取和更新（GET/PUT）
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetConfig(w, r)
	case http.MethodPut:
		s.handleUpdateConfig(w, r)
	default:
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// maskField 脱敏字段：保留末4位
func maskField(val string) string {
	if len(val) > 4 {
		return "****" + val[len(val)-4:]
	} else if val != "" {
		return "****"
	}
	return ""
}

// sensitiveFields 需要脱敏的字段列表
var sensitiveFields = map[string]bool{
	"api_key":         true,
	"secret":          true,
	"auth":            true,
	"sender_password": true,
	"key_path":        false, // 路径不脱敏
}

// maskConfig 返回脱敏后的配置 JSON
func maskConfig(cfg *config.Config) map[string]interface{} {
	data, _ := json.Marshal(cfg)
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	maskSensitive(m)
	return m
}

// maskSensitive 递归脱敏 map 中的敏感字段
func maskSensitive(m map[string]interface{}) {
	sensitive := map[string]bool{
		"api_key": true, "secret": true, "auth": true,
		"sender_password": true,
	}
	for k, v := range m {
		if sensitive[k] {
			if s, ok := v.(string); ok && s != "" {
				m[k] = maskField(s)
			}
		} else if nested, ok := v.(map[string]interface{}); ok {
			maskSensitive(nested)
		}
	}
}

// handleGetConfig 返回完整配置（敏感字段脱敏）
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	masked := maskConfig(s.cfg)
	jsonResp(w, masked)
}

// handleUpdateConfig 更新完整配置
func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	// 先解析请求体为 map，检查哪些顶级字段被显式提交
	var bodyMap map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&bodyMap); err != nil {
		jsonError(w, "无效的请求体", http.StatusBadRequest)
		return
	}

	// 将 map 序列化回 JSON 再反序列化到 struct
	bodyJSON, _ := json.Marshal(bodyMap)
	var newCfg config.Config
	json.Unmarshal(bodyJSON, &newCfg)

	// 从旧配置中补全未提交的字段
	oldJSON, _ := json.Marshal(s.cfg)
	var oldMap map[string]interface{}
	json.Unmarshal(oldJSON, &oldMap)
	for k, v := range oldMap {
		if _, exists := bodyMap[k]; !exists {
			bodyMap[k] = v
		}
	}

	// 保留脱敏字段的原值
	preserveMasked(oldMap, bodyMap)

	// 反序列化回结构体
	mergedJSON, _ := json.Marshal(bodyMap)
	var merged config.Config
	json.Unmarshal(mergedJSON, &merged)

	// 展开路径
	merged.SSH.KeyPath = config.ExpandPath(merged.SSH.KeyPath)
	merged.SSH.BasePath = config.ExpandPath(merged.SSH.BasePath)

	// 补全默认值（前端设置页不包含这些字段）
	if merged.Web.MaxDownloadSize == 0 {
		merged.Web.MaxDownloadSize = s.cfg.Web.MaxDownloadSize
		if merged.Web.MaxDownloadSize == 0 {
			merged.Web.MaxDownloadSize = 500 << 20
		}
	}
	if merged.Web.MaxUploadSize == 0 {
		merged.Web.MaxUploadSize = s.cfg.Web.MaxUploadSize
		if merged.Web.MaxUploadSize == 0 {
			merged.Web.MaxUploadSize = 500 << 20
		}
	}
	if merged.Web.Port == 0 {
		merged.Web.Port = s.cfg.Web.Port
	}
	if merged.Sync.MaxRetries == 0 {
		merged.Sync.MaxRetries = s.cfg.Sync.MaxRetries
	}
	if merged.Sync.RetryDelay == 0 {
		merged.Sync.RetryDelay = s.cfg.Sync.RetryDelay
	}
	if merged.AI.Timeout == 0 {
		merged.AI.Timeout = s.cfg.AI.Timeout
	}
	if merged.AI.MinContentLen == 0 {
		merged.AI.MinContentLen = s.cfg.AI.MinContentLen
	}
	if merged.AI.MaxContentLen == 0 {
		merged.AI.MaxContentLen = s.cfg.AI.MaxContentLen
	}

	// 更新内存中的配置（逐个字段赋值，保持所有内部指针有效）
	s.cfg.SSH = merged.SSH
	s.cfg.Clipboard = merged.Clipboard
	s.cfg.Sync = merged.Sync
	s.cfg.Web = merged.Web
	s.cfg.Relay = merged.Relay
	s.cfg.Blog = merged.Blog
	s.cfg.Podcast = merged.Podcast
	s.cfg.IMA = merged.IMA
	s.cfg.Kindle = merged.Kindle
	s.cfg.AI = merged.AI
	s.cfg.WebReader = merged.WebReader
	s.cfg.Knasync = merged.Knasync
	s.cfg.Webhook = merged.Webhook

	// 持久化到磁盘
	if err := config.Save(s.cfg); err != nil {
		jsonError(w, fmt.Sprintf("保存配置失败: %v", err), http.StatusInternalServerError)
		return
	}

	log.Printf("[INFO] Config updated via web UI")
	jsonResp(w, map[string]string{"status": "saved"})
}

// preserveMasked 保留被脱敏的字段原值
func preserveMasked(old, new map[string]interface{}) {
	sensitive := map[string]bool{
		"api_key": true, "secret": true, "auth": true, "sender_password": true,
	}
	for k, v := range new {
		if sensitive[k] {
			if s, ok := v.(string); ok && strings.HasPrefix(s, "****") {
				new[k] = old[k]
			} else if s == "" {
				new[k] = old[k]
			}
		} else if nested, ok := v.(map[string]interface{}); ok {
			if oldNested, ok := old[k].(map[string]interface{}); ok {
				preserveMasked(oldNested, nested)
			}
		}
	}
}

// handleHistoryEntryFull 返回历史条目的完整内容（从 NAS 读取）
func (s *Server) handleHistoryEntryFull(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "缺少 ID 参数", http.StatusBadRequest)
		return
	}

	entry, err := s.histStore.GetByID(id)
	if err != nil {
		jsonError(w, fmt.Sprintf("找不到条目: %v", err), http.StatusNotFound)
		return
	}

	content := entry.Content

	// 如果有 NASPath，从 NAS 读取完整内容
	if entry.NASPath != "" && s.sshClient != nil {
		data, err := s.sshClient.ReadFile(entry.NASPath)
		if err != nil {
			log.Printf("[WARN] Failed to read NAS file %s: %v, using local content", entry.NASPath, err)
		} else {
			content = string(data)
		}
	}

	jsonResp(w, map[string]string{"content": content})
}

// handleHistoryEntry GET 返回单条记录，PUT 更新条目
func (s *Server) handleHistoryEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "缺少 ID 参数", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		entry, err := s.histStore.GetByID(id)
		if err != nil {
			jsonError(w, fmt.Sprintf("找不到条目: %v", err), http.StatusNotFound)
			return
		}
		title := entry.Title
		if title == "" {
			title = entry.PublishTitle
		}
		jsonResp(w, map[string]interface{}{
			"id":             entry.ID,
			"content":        entry.Content,
			"type":           entry.Type,
			"timestamp":      entry.Timestamp.Format("2006-01-02 15:04:05"),
			"nas_path":       entry.NASPath,
			"tags":           entry.Tags,
			"title":          title,
			"publish_summary": entry.PublishSummary,
			"manual_edit":    entry.ManualEdit,
		})

	case http.MethodPut:
		var req struct {
			Title   *string  `json:"title"`
			Tags    []string `json:"tags"`
			Summary string   `json:"summary"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "无效的请求体", http.StatusBadRequest)
			return
		}

		entry, err := s.histStore.GetByID(id)
		if err != nil {
			jsonError(w, fmt.Sprintf("找不到条目: %v", err), http.StatusNotFound)
			return
		}

		title := ""
		if req.Title != nil {
			title = *req.Title
		}

		if err := s.histStore.UpdateEntry(id, title, req.Tags, req.Summary, false); err != nil {
			jsonError(w, fmt.Sprintf("更新失败: %v", err), http.StatusInternalServerError)
			return
		}

		// 同步更新远程 NAS 文件的 frontmatter
		if entry.NASPath != "" && s.sshClient != nil {
			data, err := s.sshClient.ReadFile(entry.NASPath)
			if err == nil {
				meta := parseContentMetadata(string(data))
				meta.Tags = req.Tags
				meta.Summary = req.Summary
				if title != "" {
					meta.Title = title
				}
				meta.ManualEdit = true
				_ = s.sshClient.UpdateFileMetadata(entry.NASPath, &meta)
			}
		}

		jsonResp(w, map[string]interface{}{"status": "saved", "manual_edit": true})

	default:
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleReprocess 重新运行 AI 处理
func (s *Server) handleReprocess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "缺少 ID 参数", http.StatusBadRequest)
		return
	}

	entry, err := s.histStore.GetByID(id)
	if err != nil {
		jsonError(w, fmt.Sprintf("找不到条目: %v", err), http.StatusNotFound)
		return
	}

	if s.aiProcessor == nil {
		jsonError(w, "AI 处理器未启用", http.StatusServiceUnavailable)
		return
	}

	// 获取完整内容
	content := entry.Content
	if entry.NASPath != "" && s.sshClient != nil {
		data, err := s.sshClient.ReadFile(entry.NASPath)
		if err == nil {
			text := string(data)
			if strings.HasPrefix(text, "---") {
				endIdx := strings.Index(text[4:], "---")
				if endIdx >= 0 {
					text = text[4+endIdx+3:]
				}
			}
			content = text
		}
	}

	if content == "" {
		jsonError(w, "内容为空", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.cfg.AI.Timeout)*time.Second)
	defer cancel()

	// 重新运行 AI 处理
	aiResult := s.aiProcessor.Process(ctx, content)
	var title, summary string
	var tags []string
	var score int
	if aiResult != nil {
		tags = aiResult.Tags
		summary = aiResult.Summary
		score = aiResult.Score
	}

	// 异步生成标题和完整结果（不阻塞返回）
	go func() {
		ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel2()
		ts := s.aiProcessor.GenerateTitleAndSummary(ctx2, content)
		finalTitle := title
		if ts != nil {
			finalTitle = ts.Title
		}
		_ = s.histStore.UpdateEntry(id, finalTitle, tags, summary, true)
		// 更新远程文件
		if entry.NASPath != "" && s.sshClient != nil {
			data, err := s.sshClient.ReadFile(entry.NASPath)
			if err == nil {
				meta := parseContentMetadata(string(data))
				meta.Tags = tags
				meta.Summary = summary
				meta.Title = finalTitle
				meta.Score = score
				meta.ManualEdit = false
				if aiResult != nil {
					meta.OrganizedContent = aiResult.OrganizedContent
				}
				_ = s.sshClient.UpdateFileMetadata(entry.NASPath, &meta)
			}
		}
	}()

	jsonResp(w, map[string]interface{}{
		"status":  "processing",
		"title":   title,
		"tags":    tags,
		"summary": summary,
		"score":   score,
	})
}

// stripFrontmatter 去除 YAML frontmatter，保留正文
func stripFrontmatter(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return content
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return content
	}
	return strings.TrimSpace(content[3+end+3:])
}

// parseContentMetadata 从 .md 文件内容中解析现有的 frontmatter 元数据
func parseContentMetadata(content string) ssh.ContentMetadata {
	meta := ssh.ContentMetadata{}
	if !strings.HasPrefix(content, "---") {
		return meta
	}
	endIdx := strings.Index(content[4:], "---")
	if endIdx < 0 {
		return meta
	}
	frontmatter := content[4 : 4+endIdx]

	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "tags:"):
			val := strings.TrimSpace(line[5:])
			if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
				val = strings.Trim(val, "[]")
				if val != "" {
					for _, t := range strings.Split(val, ",") {
						t = strings.TrimSpace(t)
						if t != "" {
							meta.Tags = append(meta.Tags, t)
						}
					}
				}
			}
		case strings.HasPrefix(line, `summary:`):
			val := strings.TrimSpace(line[8:])
			if len(val) >= 2 && val[0] == '"' {
				val = val[1 : len(val)-1]
			}
			meta.Summary = val
		case strings.HasPrefix(line, "score:"):
			fmt.Sscanf(strings.TrimSpace(line[6:]), "%d", &meta.Score)
		case strings.HasPrefix(line, `title:`):
			val := strings.TrimSpace(line[6:])
			if len(val) >= 2 && val[0] == '"' {
				val = val[1 : len(val)-1]
			}
			meta.Title = val
		case strings.HasPrefix(line, "manual_edit:"):
			meta.ManualEdit = strings.TrimSpace(line[12:]) == "true"
		case strings.HasPrefix(line, "sync_time:"):
			meta.Processed = true
		}
	}

	// 提取 organized_content（# 核心摘要 到 ### 原始内容 之间）
	if idx := strings.Index(content, "# 核心摘要"); idx >= 0 {
		body := content[idx:]
		if endIdx := strings.Index(body, "### 原始内容"); endIdx >= 0 {
			meta.OrganizedContent = strings.TrimSpace(body[len("# 核心摘要"):endIdx])
		}
	}

	return meta
}

// handleUpload 处理文件上传，保存到 NAS（远程 SSH 目录）
// 请求格式: multipart/form-data，字段名 "file"
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 限制最大上传大小（默认 500MB，可通过配置修改）
	maxUploadSize := s.cfg.Web.MaxUploadSize
	if maxUploadSize <= 0 {
		maxUploadSize = 500 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		http.Error(w, "file too large or invalid form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing 'file' field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 读取文件内容
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusInternalServerError)
		return
	}

	// 所有文件放在同一个 uploads 目录下
	remoteDir := filepath.Join(s.cfg.SSH.BasePath, "uploads")

	// 创建远程目录
	if err := s.sshClient.MkdirAll(remoteDir); err != nil {
		http.Error(w, "failed to create upload directory", http.StatusInternalServerError)
		return
	}

	// 使用时间戳+原文件名避免冲突
	safeName := strings.ReplaceAll(header.Filename, "/", "_")
	destPath := filepath.Join(remoteDir, safeName)

	// 如果文件已存在，旧文件重命名加时间戳，新文件保持原文件名
	if s.sshClient.FileExists(destPath) {
		timestamp := time.Now().Format("20060102_150405")
		ext := filepath.Ext(safeName)
		base := strings.TrimSuffix(safeName, ext)
		archiveName := base + "_" + timestamp + ext
		archivePath := filepath.Join(remoteDir, archiveName)
		if err := s.sshClient.MoveFile(destPath, archivePath); err != nil {
			log.Printf("[WARN] Failed to archive old file %s: %v, will overwrite", destPath, err)
		}
	}

	// 写入远程 NAS
	if err := s.sshClient.WriteBinary(destPath, data); err != nil {
		http.Error(w, "failed to save file", http.StatusInternalServerError)
		return
	}

	written := int64(len(data))
	log.Printf("[INFO] File uploaded to NAS: %s (%d bytes) -> %s", header.Filename, written, destPath)

	// 如果是 .txt 或 .md 文件，异步投递到 syncText 流水线
	if s.syncTextFn != nil {
		ext := strings.ToLower(filepath.Ext(safeName))
		if ext == ".txt" || ext == ".md" {
			content := string(data)
			log.Printf("[INFO] Upload text file detected (%s), queuing async sync", ext)
			go s.syncTextFn(content, time.Now())
		}
	}

	jsonResp(w, map[string]interface{}{
		"status":   "ok",
		"filename": header.Filename,
		"saved_as": filepath.Base(destPath),
		"path":     destPath,
		"size":     written,
	})
}

// handleUploadsDownload 从 uploads 目录流式下载文件（供 API 调用）
func (s *Server) handleUploadsDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filename := r.URL.Query().Get("filename")
	if filename == "" {
		jsonError(w, "缺少 filename 参数", http.StatusBadRequest)
		return
	}

	// 防止路径穿越
	filename = filepath.Base(filename)
	fullPath := filepath.Join(s.cfg.SSH.BasePath, "uploads", filename)

	// 检查文件大小
	maxSize := s.cfg.Web.MaxDownloadSize
	size, err := s.sshClient.FileSize(fullPath)
	if err != nil {
		jsonError(w, fmt.Sprintf("无法读取文件信息: %v", err), http.StatusServiceUnavailable)
		return
	}
	if size > maxSize {
		jsonError(w, fmt.Sprintf("文件过大（%d bytes），超过下载限制（%d bytes）", size, maxSize), http.StatusRequestEntityTooLarge)
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Type", contentTypeByExt(filepath.Ext(filename)))

	if err := s.sshClient.ReadFileToWriter(fullPath, w); err != nil {
		log.Printf("[ERROR] failed to stream file %s: %v", fullPath, err)
	}
}


// handleClusters returns the current cluster result (GET).
func (s *Server) handleClusters(w http.ResponseWriter, r *http.Request) {
	log.Printf("[DEBUG] handleClusters called, engine=%v", s.clusterEngine != nil)
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.clusterEngine == nil {
		jsonResp(w, map[string]any{"clusters": []any{}, "generated_at": nil, "total_entries": 0, "clustered_count": 0})
		return
	}
	result := s.clusterEngine.GetResult()
	if result == nil {
		// 尝试从磁盘重新加载（适配启动时文件尚未生成的情况）
		result = s.clusterEngine.LoadClusters()
	}
	if result == nil {
		log.Printf("[WARN] handleClusters: result is nil, returning empty")
		jsonResp(w, map[string]any{"clusters": []any{}, "generated_at": nil, "total_entries": 0, "clustered_count": 0})
		return
	}
	log.Printf("[INFO] handleClusters: returning %d clusters", len(result.Clusters))
	jsonResp(w, result)
}

// handleClusterRerun triggers an immediate clustering pass (POST).
func (s *Server) handleClusterRerun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.clusterEngine == nil {
		jsonError(w, "clustering not configured", http.StatusServiceUnavailable)
		return
	}
	go func() {
		if err := s.clusterEngine.ForceRun(); err != nil {
			log.Printf("[ERROR] Cluster rerun failed: %v", err)
		}
	}()
	jsonResp(w, map[string]string{"status": "started"})
}
