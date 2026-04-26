package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yuanguangshan/knowly/internal/ai"
	"github.com/yuanguangshan/knowly/internal/clipboard"
	"github.com/yuanguangshan/knowly/internal/config"
	"github.com/yuanguangshan/knowly/internal/fetcher"
	"github.com/yuanguangshan/knowly/internal/history"
	"github.com/yuanguangshan/knowly/internal/outbox"
	"github.com/yuanguangshan/knowly/internal/publisher"
	"github.com/yuanguangshan/knowly/internal/relay"
	"github.com/yuanguangshan/knowly/internal/retry"
	"github.com/yuanguangshan/knowly/internal/ssh"
	"github.com/yuanguangshan/knowly/internal/web"
	xclip "golang.design/x/clipboard"
)

func main() {
	// 0. 处理 --stop（无需加载配置）
	if len(os.Args) > 1 && os.Args[1] == "--stop" {
		stopDaemon()
		return
	}

	// 1. 加载配置
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 补全旧配置缺失的默认值
	if cfg.Clipboard.MaxLength == 0 {
		cfg.Clipboard.MaxLength = 1024 * 1024 // 1MB
	}
	if cfg.Clipboard.MinLength == 0 {
		cfg.Clipboard.MinLength = 100
	}

	// 初始化 web_reader（用于知乎等需要 JS 渲染的页面）
	if cfg.WebReader.APIKey != "" {
		fetcher.SetWebReaderAPIKey(cfg.WebReader.APIKey)
	}

	// 处理 --status
	if len(os.Args) > 1 && os.Args[1] == "--status" {
		showStatus(cfg)
		return
	}

	// 2. 初始化组件
	client := ssh.NewClient(&ssh.Config{
		Host:                 cfg.SSH.Host,
		Port:                 cfg.SSH.Port,
		User:                 cfg.SSH.User,
		KeyPath:              cfg.SSH.KeyPath,
		BasePath:             cfg.SSH.BasePath,
		FilenamePrefixLength: cfg.SSH.FilenamePrefixLength,
	})
	histStore := history.NewStore(config.GetConfigDir())
	outboxStore := outbox.NewStore(config.GetConfigDir())

	aiProcessor := ai.NewProcessor(&cfg.AI)
	if aiProcessor != nil {
		preset := cfg.AI.Preset
		if preset == "" {
			preset = "custom"
		}
		promptMode := cfg.AI.PromptTemplate
		if promptMode == "" {
			if cfg.AI.Prompt == "" {
				promptMode = "默认"
			} else {
				promptMode = "自定义"
			}
		}
		log.Printf("[INFO] AI processing enabled (preset: %s, model: %s, endpoint: %s, prompt: %s)", preset, cfg.AI.Model, cfg.AI.Endpoint, promptMode)
	}

	mon := clipboard.NewMonitor(clipboard.MonitorConfig{
		MinLength:    cfg.Clipboard.MinLength,
		MaxLength:    cfg.Clipboard.MaxLength,
		PollInterval: time.Duration(cfg.Clipboard.PollInterval) * time.Millisecond,
		ExcludeWords: cfg.Clipboard.ExcludeWords,
	}, config.GetConfigDir()+"/status.json")

	// 3. 处理 CLI 命令
	if len(os.Args) > 1 {
		if os.Args[1] == "--daemon" {
			writePidFile()
			redirectLogsToFile()
			// 继续执行守护逻辑
		} else {
			handleCLI(os.Args[1:], cfg, histStore)
			return
		}
	}

	// 4. 启动守护逻辑
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 4.1 先启动 Web 管理界面（不依赖 SSH 连接）
	var webSrv *web.Server
	if cfg.Web.IsEnabled() {
		webAddr := fmt.Sprintf(":%d", cfg.Web.Port)
		webSrv = web.NewServerWithDeps(cfg, webAddr, client, histStore)
		webSrv.StartAsync()
	}

	// 4.2 异步连接 SSH（不阻塞 Web 启动）
	go func() {
		for {
			if err := client.Connect(); err != nil {
				log.Printf("[WARN] SSH connect failed: %v, retrying in 10s...", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
					continue
				}
			}
			break
		}
	}()
	defer client.Disconnect()

	mon.Start()
	log.Println("[INFO] knowly daemon started")

	// 启动后尝试排空之前积压的 outbox 条目
	go drainOutbox(outboxStore, client, histStore)

	// 周期性排空 outbox（每 5 分钟检查一次）
	drainTicker := time.NewTicker(5 * time.Minute)
	defer drainTicker.Stop()

	// 周期性日志轮转检查（每 10 分钟）
	logRotateTicker := time.NewTicker(10 * time.Minute)
	defer logRotateTicker.Stop()

	// 5. 启动 Relay 拉取器（如果启用）
	if cfg.Relay.Enabled && cfg.Relay.Endpoint != "" {
		puller := relay.NewPuller(
			cfg.Relay.Endpoint,
			cfg.Relay.Secret,
			time.Duration(cfg.Relay.Interval)*time.Second,
			func(content string) {
				// Relay 内容也走统一的同步+归档流程
				go syncAndArchiveText(client, cfg, content, "relay", histStore, aiProcessor, outboxStore, mon)
			},
		)
		puller.Start()
		defer puller.Stop()
		log.Println("[INFO] Relay puller started")
	}

	// 7. 消费 Payload 循环
	for {
		select {
		case <-ctx.Done():
			if webSrv != nil {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				webSrv.Shutdown(shutdownCtx)
				cancel()
			}
			mon.Stop()
			removePidFile()
			log.Println("[INFO] knowly daemon stopped")
			return
		case payload := <-mon.Items():
			go handlePayload(client, cfg, payload, histStore, aiProcessor, outboxStore)
		case <-drainTicker.C:
			go drainOutbox(outboxStore, client, histStore)
		case <-logRotateTicker.C:
			go rotateLogIfNeeded(client, cfg)
		}
	}
}

// handlePayload 处理来自 Monitor 的同步项
func handlePayload(client *ssh.Client, cfg *config.Config, p clipboard.Payload, histStore *history.Store, aiProcessor *ai.Processor, outboxStore *outbox.Store) {
	retryCfg := retry.Config{
		MaxRetries: cfg.Sync.MaxRetries,
		BaseDelay:  time.Duration(cfg.Sync.RetryDelay) * time.Millisecond,
		MaxDelay:   30 * time.Second,
	}

	switch v := p.(type) {
	case clipboard.TextPayload:
		// PDF URL 走专门的下载保存流程
		if fetcher.IsURL(v.Content) {
			urlStr := fetcher.ExtractURL(v.Content)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			isPDF := fetcher.IsPDFURL(ctx, urlStr)
			cancel()
			if isPDF {
				syncPDF(client, cfg, urlStr, v.Timestamp, histStore, outboxStore, "Clipboard")
				return
			}
		}
		// 文本同步委托给 syncText（公共逻辑）
		syncText(client, cfg, v.Content, v.Timestamp, histStore, aiProcessor, outboxStore, "Clipboard")
	case clipboard.ImagePayload:
		handleImagePayload(client, retryCfg, v, histStore, outboxStore)
	}
}

// handleImagePayload 处理图片同步
func handleImagePayload(client *ssh.Client, retryCfg retry.Config, v clipboard.ImagePayload, histStore *history.Store, outboxStore *outbox.Store) {
	var nasPath string
	err := retry.Do(context.Background(), retryCfg, func() error {
		path, syncErr := client.SyncImage(v.Data, v.Timestamp)
		if syncErr == nil {
			nasPath = path
		}
		return syncErr
	})

	if err != nil {
		log.Printf("[ERROR] Image sync failed: %v, saving to outbox", err)
		client.ForceReset()
		if err := outboxStore.Push(outbox.Item{
			Type:      "image",
			Content:   base64.StdEncoding.EncodeToString(v.Data),
			Timestamp: v.Timestamp,
		}); err != nil {
			log.Printf("[ERROR] Failed to save image to outbox: %v", err)
		}
		return
	}

	if nasPath == "" {
		log.Printf("[INFO] Image duplicate skipped")
		return
	}

	histStore.Append(history.Entry{
		Content: fmt.Sprintf("[IMAGE] %d bytes", len(v.Data)),
		Type:    "image",
		NASPath: nasPath,
	}) // ignore returned ID for image entries
	log.Printf("[INFO] Synced & Archived (image): %s", nasPath)
}

// syncPDF 下载 PDF 并保存到 NAS
func syncPDF(client *ssh.Client, cfg *config.Config, urlStr string, timestamp time.Time, histStore *history.Store, outboxStore *outbox.Store, source string) {
	log.Printf("[INFO] %s PDF detected: %s", source, urlStr)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	data, err := fetcher.FetchPDF(ctx, urlStr)
	if err != nil {
		log.Printf("[ERROR] %s PDF download failed: %v", source, err)
		return
	}

	log.Printf("[INFO] %s PDF downloaded (%d bytes)", source, len(data))

	nasPath, err := client.SyncPDF(data, timestamp, urlStr)
	if err != nil {
		log.Printf("[ERROR] %s PDF sync failed: %v", source, err)
		client.ForceReset()
		return
	}

	histStore.Append(history.Entry{
		Content: fmt.Sprintf("[PDF] %s", urlStr),
		Type:    "pdf",
		NASPath: nasPath,
	})
	log.Printf("[INFO] %s PDF synced & archived: %s", source, nasPath)
}

// drainOutbox 尝试排空本地暂存队列，将积压条目同步到远端
func drainOutbox(outboxStore *outbox.Store, client *ssh.Client, histStore *history.Store) {
	if outboxStore.PendingCount() == 0 {
		return
	}

	log.Printf("[INFO] Outbox: draining pending items...")

	syncFn := func(item outbox.Item) (string, error) {
		switch item.Type {
		case "text":
			var meta *ssh.ContentMetadata
			if item.Processed {
				meta = &ssh.ContentMetadata{
					Tags:             item.Tags,
					Summary:          item.Summary,
					Score:            item.Score,
					OrganizedContent: item.OrganizedContent,
					Processed:        true,
				}
			}
			return client.SyncItem(item.Content, item.Timestamp, meta)
		case "image":
			data, err := outbox.DecodeImageContent(item.Content)
			if err != nil {
				return "", fmt.Errorf("base64 decode failed: %w", err)
			}
			return client.SyncImage(data, item.Timestamp)
		default:
			return "", fmt.Errorf("unknown type: %s", item.Type)
		}
	}

	synced, err := outboxStore.Drain(syncFn)
	if err != nil {
		// 排空过程中遇到 SSH 错误，重置连接
		client.ForceReset()
		log.Printf("[WARN] Outbox: drain stopped after %d items (SSH error)", synced)
		return
	}

	if synced > 0 {
		log.Printf("[INFO] Outbox: drained %d pending items", synced)
	}
}

// syncAndArchiveText 处理来自 Relay 的文本同步
func syncAndArchiveText(client *ssh.Client, cfg *config.Config, content, source string, histStore *history.Store, aiProcessor *ai.Processor, outboxStore *outbox.Store, mon *clipboard.Monitor) {
	start := time.Now()

	// 本地去重：如果内存中已存在相同内容，直接跳过
	if mon.IsDuplicate(content) {
		log.Printf("[INFO] Relay content skipped: duplicate (in-memory)")
		return
	}

	// Relay 内容同样需要经过过滤检查
	isURL := fetcher.IsURL(content)
	preview := content
	if len(preview) > 50 {
		preview = preview[:50] + "..."
	}
	log.Printf("[DEBUG] Relay content: len=%d isURL=%v repr=%q", len(content), isURL, preview)
	if r := clipboard.ShouldFilterDetail(content, cfg.Clipboard.MinLength, cfg.Clipboard.MaxLength, cfg.Clipboard.ExcludeWords); r.Filtered {
		switch r.Reason {
		case "exclude_word":
			log.Printf("[INFO] Relay content filtered by sensitive word: %q", r.MatchedWord)
		case "length_too_short":
			log.Printf("[INFO] Relay content filtered: too short (%d < %d)", len(content), cfg.Clipboard.MinLength)
		case "length_too_long":
			log.Printf("[INFO] Relay content filtered: too long (%d > %d)", len(content), cfg.Clipboard.MaxLength)
		}
		return
	}

	// Relay 路径也需要 URL 增强（与剪贴板 enhanceAndSend 一致）
	if fetcher.IsURL(content) {
		urlStr := fetcher.ExtractURL(content)

		// PDF URL 直接下载保存到 NAS
		pdfCtx, pdfCancel := context.WithTimeout(context.Background(), 5*time.Second)
		isPDF := fetcher.IsPDFURL(pdfCtx, urlStr)
		pdfCancel()
		if isPDF {
			syncPDF(client, cfg, urlStr, time.Now(), histStore, outboxStore, "Relay")
			log.Printf("[INFO] Relay total processing time: %.1fs", time.Since(start).Seconds())
			return
		}

		log.Printf("[INFO] Relay fetching URL: %s", urlStr)
		urlStart := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		info, err := fetcher.FetchPage(ctx, urlStr)
		cancel()
		log.Printf("[INFO] Relay URL fetched in %.1fs", time.Since(urlStart).Seconds())
		if err == nil && info != nil {
			var enhanced strings.Builder
			enhanced.WriteString(content)
			if info.Title != "" {
				enhanced.WriteString("\n\n# " + info.Title)
			}
			if info.Content != "" {
				enhanced.WriteString("\n\n" + info.Content)
			}
			if enhanced.Len() > len(content) {
				content = enhanced.String()
			}
		} else {
			log.Printf("[DEBUG] Relay URL fetch failed: %v", err)
		}
	}

	syncText(client, cfg, content, time.Now(), histStore, aiProcessor, outboxStore, "Relay")

	log.Printf("[INFO] Relay total processing time: %.1fs", time.Since(start).Seconds())
}

// syncText 公共文本同步逻辑（剪贴板和 Relay 共用）
func syncText(client *ssh.Client, cfg *config.Config, content string, timestamp time.Time, histStore *history.Store, aiProcessor *ai.Processor, outboxStore *outbox.Store, source string) {
	retryCfg := retry.Config{
		MaxRetries: cfg.Sync.MaxRetries,
		BaseDelay:  time.Duration(cfg.Sync.RetryDelay) * time.Millisecond,
		MaxDelay:   30 * time.Second,
	}

	// AI 处理
	var meta *ssh.ContentMetadata
	var aiTags []string
	if aiProcessor != nil && aiProcessor.ShouldProcess(content) {
		aiStart := time.Now()
		log.Printf("[INFO] %s AI processing started (len=%d)", source, len(content))
		aiCtx, aiCancel := context.WithTimeout(context.Background(), time.Duration(cfg.AI.Timeout)*time.Second)
		aiResult := aiProcessor.Process(aiCtx, content)
		aiCancel()
		log.Printf("[INFO] %s AI processing done in %.1fs", source, time.Since(aiStart).Seconds())
		if aiResult != nil {
			aiTags = aiResult.Tags
			meta = &ssh.ContentMetadata{
				Tags:             aiResult.Tags,
				Summary:          aiResult.Summary,
				Score:            aiResult.Score,
				OrganizedContent: aiResult.OrganizedContent,
				Processed:        true,
			}
		}
	}

	var nasPath string
	err := retry.Do(context.Background(), retryCfg, func() error {
		path, syncErr := client.SyncItem(content, timestamp, meta)
		if syncErr == nil {
			nasPath = path
		}
		return syncErr
	})

	if err != nil {
		log.Printf("[ERROR] %s sync failed: %v, saving to outbox", source, err)
		client.ForceReset()
		// 保留完整 AI 元数据
		item := outbox.Item{
			Type:      "text",
			Content:   content,
			Timestamp: timestamp,
			Tags:      aiTags,
		}
		if meta != nil && meta.Processed {
			item.Summary = meta.Summary
			item.Score = meta.Score
			item.OrganizedContent = meta.OrganizedContent
			item.Processed = true
		}
		if err := outboxStore.Push(item); err != nil {
			log.Printf("[ERROR] Failed to save to outbox: %v", err)
		}
		return
	}

	if nasPath == "" {
		log.Printf("[INFO] %s duplicate skipped", source)
		return
	}

	entryID, _ := histStore.Append(history.Entry{
		Content: content,
		Type:    "text",
		NASPath: nasPath,
		Tags:    aiTags,
	})
	log.Printf("[INFO] %s synced & archived: %s", source, nasPath)

	// 异步预生成发布标题/摘要，手动发布时直接使用
	if aiProcessor != nil && aiProcessor.ShouldProcess(content) && entryID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result := aiProcessor.GenerateTitleAndSummary(ctx, content)
			if result != nil {
				if err := histStore.UpdatePublishMeta(entryID, result.Title, result.Summary); err != nil {
					log.Printf("[WARN] Failed to cache publish meta for %s: %v", entryID, err)
				}
			}
		}()
	}

	// 异步推送到已启用的外部渠道
	publisher.PublishIfNeeded(cfg, content)
}

// pidFileLock 全局持有 PID 文件的文件锁，进程退出时自动释放
var pidFileLock *os.File

// writePidFile 将当前进程 PID 写入文件，并使用排他文件锁防止重复启动
func writePidFile() {
	pidPath := config.GetPidPath()

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(pidPath), 0755); err != nil {
		log.Fatalf("Failed to create PID directory: %v", err)
	}

	f, err := os.OpenFile(pidPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open PID file %s: %v", pidPath, err)
	}

	// 尝试获取排他锁（非阻塞），如果失败说明已有另一个守护进程在运行
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		log.Fatalf("另一个 Knowly 守护进程正在运行 (PID 文件: %s)", pidPath)
	}

	// 获取锁成功，写入当前 PID
	if err := f.Truncate(0); err != nil {
		f.Close()
		log.Fatalf("Failed to truncate PID file: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		log.Fatalf("Failed to seek PID file: %v", err)
	}
	if _, err := f.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		f.Close()
		log.Fatalf("Failed to write PID: %v", err)
	}

	// 保存文件句柄，进程退出时 close 自动释放 flock
	pidFileLock = f
}

// redirectLogsToFile 将 stdout/stderr 重定向到日志文件
func redirectLogsToFile() {
	logPath := config.GetLogPath()
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("Failed to open log file %s: %v", logPath, err)
	}
	os.Stdout = f
	os.Stderr = f
	log.SetOutput(f)
}

// rotateLogIfNeeded 检查日志文件大小，超过 10MB 则归档到 NAS 并截断
func rotateLogIfNeeded(sshClient *ssh.Client, cfg *config.Config) {
	logPath := config.GetLogPath()
	info, err := os.Stat(logPath)
	if err != nil || info.Size() < 10*1024*1024 {
		return
	}

	// 归档文件名：knowly_20260426_153040.log
	now := time.Now()
	archiveName := fmt.Sprintf("knowly_%s.log", now.Format("20060102_150405"))
	remoteDir := filepath.Join(cfg.SSH.BasePath, "_logs")

	data, err := os.ReadFile(logPath)
	if err != nil {
		log.Printf("[WARN] Log rotate: failed to read log: %v", err)
		return
	}

	if err := sshClient.WriteFile(filepath.Join(remoteDir, archiveName), string(data)); err != nil {
		log.Printf("[WARN] Log rotate: failed to upload to NAS: %v", err)
		return
	}

	// 截断本地日志文件
	if err := os.Truncate(logPath, 0); err != nil {
		log.Printf("[WARN] Log rotate: failed to truncate: %v", err)
		return
	}

	log.Printf("[INFO] Log rotated: %s → NAS %s", archiveName, remoteDir)
}

// removePidFile 删除 PID 文件并释放文件锁
func removePidFile() {
	pidPath := config.GetPidPath()
	if pidFileLock != nil {
		pidFileLock.Close()
		pidFileLock = nil
	}
	os.Remove(pidPath)
}

// stopDaemon 停止守护进程
func stopDaemon() {
	pidPath := config.GetPidPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		fmt.Println("knowly daemon is not running (no PID file)")
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Println("Invalid PID file")
		return
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Printf("Failed to stop daemon (PID %d): %v\n", pid, err)
		os.Remove(pidPath)
		return
	}
	os.Remove(pidPath)
	fmt.Printf("✓ knowly daemon stopped (PID %d)\n", pid)
}

// showStatus 显示守护进程状态
func showStatus(cfg *config.Config) {
	pidPath := config.GetPidPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		fmt.Println("✗ knowly daemon is not running")
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Println("✗ Invalid PID file")
		return
	}
	if err := syscall.Kill(pid, 0); err != nil {
		fmt.Println("✗ knowly daemon is not running (process dead)")
		os.Remove(pidPath)
		return
	}
	fmt.Printf("✓ knowly daemon is running (PID: %d)\n", pid)
	if cfg != nil {
		fmt.Printf("  SSH: %s@%s:%s\n", cfg.SSH.User, cfg.SSH.Host, cfg.SSH.Port)
		fmt.Printf("  Base Path: %s\n", cfg.SSH.BasePath)
	}
	histFile := filepath.Join(config.GetConfigDir(), "history.jsonl")
	if data, err := os.ReadFile(histFile); err == nil {
		count := strings.Count(string(data), "\n")
		fmt.Printf("  Total syncs: %d\n", count)
	}
	if entries, err := history.NewStore(config.GetConfigDir()).Recent(1); err == nil && len(entries) > 0 {
		last := entries[0]
		preview := strings.ReplaceAll(last.Content, "\n", " ")
		preview = strings.ReplaceAll(preview, "\r", "")
		if runes := []rune(preview); len(runes) > 50 {
			preview = string(runes[:47]) + "..."
		}
		fmt.Printf("  Last sync: [%s] (%s) %s\n", last.Timestamp.Format("01-02 15:04"), last.Type, preview)
	}
}

// handleCLI 处理命令行指令
func handleCLI(args []string, cfg *config.Config, histStore *history.Store) {
	cmd := args[0]
	switch cmd {
	case "history":
		n := 20
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &n)
		}
		entries, err := histStore.Recent(n)
		if err != nil {
			log.Fatal(err)
		}
		if len(entries) == 0 {
			fmt.Println("暂无历史记录")
			return
		}
		for _, e := range entries {
			displayID := e.ID
			if len(displayID) > 14 {
				displayID = displayID[:14]
			}
			preview := strings.ReplaceAll(e.Content, "\n", " ")
			preview = strings.ReplaceAll(preview, "\r", "")
			if runes := []rune(preview); len(runes) > 60 {
				preview = string(runes[:57]) + "..."
			}
			fmt.Printf("[%s] (%s) %s\n", displayID, e.Type, preview)
		}
	case "restore":
		if len(args) < 2 {
			log.Fatal("Usage: knowly restore <id>")
		}
		id := args[1]
		entry, err := histStore.Find(id)
		if err != nil || entry == nil {
			log.Fatal("Entry not found")
		}

		switch entry.Type {
		case "text":
			if err := xclip.Write(xclip.FmtText, []byte(entry.Content)); err != nil {
				log.Fatal(err)
			}
		case "image":
			if entry.NASPath == "" {
				log.Fatal("图片记录中缺少远程路径，无法恢复")
			}
			// 临时建立 SSH 连接读取远程图片
			client := ssh.NewClient(&ssh.Config{
				Host:                 cfg.SSH.Host,
				Port:                 cfg.SSH.Port,
				User:                 cfg.SSH.User,
				KeyPath:              cfg.SSH.KeyPath,
				BasePath:             cfg.SSH.BasePath,
				FilenamePrefixLength: cfg.SSH.FilenamePrefixLength,
			})
			if err := client.Connect(); err != nil {
				log.Fatalf("SSH 连接失败: %v", err)
			}
			defer client.Disconnect()

			imgData, err := client.ReadFile(entry.NASPath)
			if err != nil {
				log.Fatalf("读取远程图片失败: %v", err)
			}
			if err := xclip.Write(xclip.FmtImage, imgData); err != nil {
				log.Fatal(err)
			}
		default:
			log.Fatalf("不支持的类型: %s", entry.Type)
		}
		fmt.Printf("✓ 已将记录 %s 恢复到剪贴板\n", id[:14])
	case "web":
		addr := ":8090"
		if len(args) > 1 {
			addr = args[1]
		}
		webSrv := web.NewServer(cfg, addr)
		log.Fatal(webSrv.Start())
	default:
		fmt.Println("Unknown command:", cmd)
	}
}
