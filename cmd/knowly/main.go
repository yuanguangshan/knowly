package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yuanguangshan/knowly/internal/ai"
	"github.com/yuanguangshan/knowly/internal/clipboard"
	"github.com/yuanguangshan/knowly/internal/cluster"
	"github.com/yuanguangshan/knowly/internal/config"
	"github.com/yuanguangshan/knowly/internal/fetcher"
	"github.com/yuanguangshan/knowly/internal/history"
	"github.com/yuanguangshan/knowly/internal/index"
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

	// 初始化 knasync（远程处理知乎链接）
	if cfg.Knasync.Enabled {
		fetcher.SetKnasyncEnabled(true)
		fetcher.SetKnasyncConfig(cfg.Knasync.Endpoint, cfg.Knasync.AuthKey)
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

	// 打开本地全文索引（SQLite FTS5）。注入 client 后，同步成功自动增量入索引；
	// 注入 web server 后，/api/v1 查询接口可用。NAS 仍是唯一真源，索引可重建。
	var localIndex index.Indexer
	if ix, err := index.Open(filepath.Join(config.GetConfigDir(), "index.db")); err != nil {
		log.Printf("[WARN] open index.db failed: %v (/api/v1 search disabled)", err)
	} else {
		localIndex = ix
		client.SetIndexer(ix)
		defer ix.Close()
	}

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

	// 初始化聚类引擎
	clusterEngine := cluster.NewEngine(histStore, cluster.AIAPIConfig{
		Enabled:  cfg.AI.Enabled,
		Endpoint: cfg.AI.Endpoint,
		APIKey:  cfg.AI.APIKey,
		Model:   cfg.AI.Model,
		Timeout: cfg.AI.Timeout,
	}, cluster.Config{Enabled: cfg.Clustering.Enabled, IntervalH: cfg.Clustering.IntervalH, MinScore: cfg.Clustering.MinScore, MaxEntries: cfg.Clustering.MaxEntries}, config.GetConfigDir())
	clusterEngine.LoadClusters()

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
		webSrv = web.NewServerWithDeps(cfg, webAddr, client, histStore, func(content string, timestamp time.Time) {
			syncText(client, cfg, content, timestamp, histStore, aiProcessor, outboxStore, "Upload")
		}, clusterEngine)
		if localIndex != nil {
			webSrv.SetIndexer(localIndex)
		}
		webSrv.StartAsync()
	}

	// 启动聚类引擎（后台定期运行）
	clusterEngine.Start(ctx)

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

	// 索引为空时自动后台回溯 NAS 归档（一次性；之后靠同步增量维护，无需再全树扫描）
	if localIndex != nil {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
			if n, err := localIndex.Count(); err == nil && n == 0 {
				log.Printf("[INFO] local index empty, starting backfill from NAS archive...")
				web.RunBackfill(cfg, client, localIndex)
			}
		}()
	}

	mon.Start()
	log.Println("[INFO] knowly daemon started")

	// 启动时检查：如果历史记录过多（超过 2000 条），自动截断至 200 条
	go func() {
		entries, err := histStore.ReadAll()
		if err != nil {
			log.Printf("[WARN] Auto-trim: failed to read history: %v", err)
			return
		}
		if len(entries) > 2000 {
			log.Printf("[INFO] Auto-trim: history has %d entries, trimming to 200...", len(entries))
			if err := histStore.TrimTo(200); err != nil {
				log.Printf("[WARN] Auto-trim failed: %v", err)
			} else {
				log.Printf("[INFO] Auto-trim complete: 200 entries retained")
			}
		}
	}()

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
		var puller *relay.Puller
		puller = relay.NewPuller(
			cfg.Relay.Endpoint,
			cfg.Relay.Secret,
			time.Duration(cfg.Relay.Interval)*time.Second,
			func(content string) {
				// Relay 内容也走统一的同步+归档流程，处理完后推送结果
				go func() {
					// original 用于让 Worker 永久去重：
					// URL 传 URL，纯文本传原始 content 本身。
					origKey := ""
					if fetcher.IsURL(content) {
						origKey = fetcher.ExtractURL(content)
					} else {
						origKey = content
					}

					enhanced := syncAndArchiveText(client, cfg, content, "relay", histStore, aiProcessor, outboxStore, mon)
					if enhanced != "" {
						// 正常链路：推送处理结果 + ack original
						if err := puller.Push(enhanced, origKey); err != nil {
							log.Printf("[WARN] Relay push back failed: %v", err)
						}
					} else {
						// 本地已去重（in-memory 命中）跳过了同步，
						// 但仍需 ack Worker 永久去重，否则手机端会反复重投。
						if err := puller.Ack(origKey); err != nil {
							log.Printf("[WARN] Relay ack failed: %v", err)
						}
					}
				}()
			},
		)
		puller.Start()
		defer puller.Stop()
		log.Println("[INFO] Relay puller started")
	}

	// 6. 启动结果拉取器（拉取 Chrome 扩展等处理后的结果，归档到 NAS）
	if cfg.Relay.Enabled && cfg.Relay.Endpoint != "" {
		cursorFile := filepath.Join(config.GetConfigDir(), "result_cursor.txt")
		resultPuller := relay.NewResultPuller(
			cfg.Relay.Endpoint,
			cfg.Relay.Secret,
			cursorFile,
			30*time.Second,
			func(content string) {
				// 结果已是处理后的成品，跳过 URL 抓取，但仍做 AI 标签/摘要
				go func() {
					syncText(client, cfg, content, time.Now(), histStore, aiProcessor, outboxStore, "RelayResult")
					// knasync 结果到达时自动推送到启用了 auto_publish 的 webhook 目标
					if cfg.Webhook.Enabled {
						for _, t := range cfg.Webhook.Targets {
							if t.AutoPublish {
								publisher.PublishWebhookTarget(t, content)
							}
						}
					}
				}()
			},
		)
		resultPuller.Start()
		defer resultPuller.Stop()
		log.Println("[INFO] Result puller started")
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
			// 主循环去重：同 hash 正在处理则丢弃
			var acquiredHash string
			if tp, ok := payload.(clipboard.TextPayload); ok {
				h := ssh.ContentHash([]byte(tp.Content))
				if verbose {
					fmt.Fprintf(os.Stdout, "%s [DEBUG] main loop payload hash=%s len=%d\n", time.Now().Format("2006/01/02 15:04:05"), h[:12], len(tp.Content))
				}
				if !tryAcquire(h) {
					fmt.Fprintf(os.Stdout, "%s [INFO] Payload dropped (hash: %s)\n", time.Now().Format("2006/01/02 15:04:05"), h[:8])
					continue
				}
				acquiredHash = h
			}
			go func(p clipboard.Payload, ah string) {
				if ah != "" {
					defer release(ah)
				}
				handlePayload(client, cfg, p, histStore, aiProcessor, outboxStore)
			}(payload, acquiredHash)
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

// inFlight 全局正在处理中的内容 hash，防止并发重复
var inFlightMu sync.Mutex
var inFlight = make(map[string]bool)

// verbose 控制高频 DEBUG 日志（每条同步都会打印 hash/len）。默认关闭，
// 设环境变量 KNOWLY_DEBUG=1 可开启，避免生产常开时放大磁盘 IO。
var verbose = os.Getenv("KNOWLY_DEBUG") != ""

// tryAcquire 尝试获取处理权，true 表示可以处理，false 表示已在处理中
func tryAcquire(hash string) bool {
	inFlightMu.Lock()
	defer inFlightMu.Unlock()
	// 仅按内容 hash 去重，避免并发重复处理同一条内容。
	// 注意：早期实现里有一个「5 秒内任何新内容一律丢弃」的全局时间闸门，
	// 会误伤用户 5 秒内连续复制的不同内容（第二条被静默丢弃）。这里只保留 hash 去重。
	if inFlight[hash] {
		return false
	}
	inFlight[hash] = true
	return true
}

// release 释放处理权
func release(hash string) {
	inFlightMu.Lock()
	delete(inFlight, hash)
	inFlightMu.Unlock()
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
func syncAndArchiveText(client *ssh.Client, cfg *config.Config, content, source string, histStore *history.Store, aiProcessor *ai.Processor, outboxStore *outbox.Store, mon *clipboard.Monitor) string {
	start := time.Now()

	// 本地去重：如果内存中已存在相同内容，直接跳过
	if mon.IsDuplicate(content) {
		log.Printf("[INFO] Relay content skipped: duplicate (in-memory)")
		return ""
	}

	// 过滤日志行（被错误提交到 knasync 的误报）
	if len(content) >= 19 && content[4] == '/' && content[7] == '/' && content[10] == ' ' {
		log.Printf("[INFO] Relay content filtered: appears to be a log line")
		return ""
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
		return ""
	}

	enhanced := content
	// Relay 路径也需要 URL 增强（与剪贴板 enhanceAndSend 一致）
	if isURL {
		urlStr := fetcher.ExtractURL(content)

		// PDF URL 直接下载保存到 NAS
		pdfCtx, pdfCancel := context.WithTimeout(context.Background(), 5*time.Second)
		isPDF := fetcher.IsPDFURL(pdfCtx, urlStr)
		pdfCancel()
		if isPDF {
			syncPDF(client, cfg, urlStr, time.Now(), histStore, outboxStore, "Relay")
			log.Printf("[INFO] Relay total processing time: %.1fs", time.Since(start).Seconds())
			return ""
		}

		log.Printf("[INFO] Relay fetching URL: %s", urlStr)
		urlStart := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		info, err := fetcher.FetchPage(ctx, urlStr)
		cancel()
		log.Printf("[INFO] Relay URL fetched in %.1fs", time.Since(urlStart).Seconds())
		
		if err == nil && info != nil {
			var sb strings.Builder
			sb.WriteString(content)
			title := info.Title
			if title == "" && info.Content != "" {
				// 页面标题为空时，从内容首行提取
				firstLine := info.Content
				if idx := strings.Index(firstLine, "\n"); idx > 0 {
					firstLine = firstLine[:idx]
				}
				title = firstLine
			}
			if title != "" {
				sb.WriteString("\n\n# " + title)
			}
			if info.Content != "" {
				sb.WriteString("\n\n" + info.Content)
			}
			enhanced = sb.String()
		} else if err != nil {
			log.Printf("[DEBUG] Relay URL fetch failed: %v", err)
		} else {
			// FetchPage 返回 nil, nil — knasync 已接管（如知乎链接），跳过本地处理
			log.Printf("[INFO] Relay URL submitted to external processor (knasync), skipping: %s", urlStr)
			return ""
		}
	}

	syncText(client, cfg, enhanced, time.Now(), histStore, aiProcessor, outboxStore, "Relay")

	log.Printf("[INFO] Relay total processing time: %.1fs", time.Since(start).Seconds())

	// 如果增强后的内容依然只是原始 URL，则没必要推送到结果队列
	if enhanced == content && isURL {
		return ""
	}
	return enhanced
}

// syncText 公共文本同步逻辑（剪贴板和 Relay 共用）
var syncTextCallCount int64

func syncText(client *ssh.Client, cfg *config.Config, content string, timestamp time.Time, histStore *history.Store, aiProcessor *ai.Processor, outboxStore *outbox.Store, source string) {
	c := atomic.AddInt64(&syncTextCallCount, 1)
	if verbose {
		fmt.Fprintf(os.Stdout, "%s [DEBUG] syncText CALL #%d source=%s hash=%s\n", time.Now().Format("2006/01/02 15:04:05"), c, source, ssh.ContentHash([]byte(content))[:12])
	}
	hash := ssh.ContentHash([]byte(content))
	isURL := fetcher.IsURL(content)

	// 远程去重前置检查
	relPath := filepath.Join(timestamp.Format("2006"), timestamp.Format("01"), timestamp.Format("02"))

	// 如果是纯 URL（抓取失败后回退），写入 NAS 作为失败记录
	if isURL {
		fallbackContent := fmt.Sprintf("> 链接抓取失败（已重试 3 次）\n\n原文链接：%s\n\n---\n\n请手动访问此链接。", content)
		fallbackMeta := &ssh.ContentMetadata{
			Title:     "抓取失败：" + content,
			Tags:      []string{"fetch-failed"},
			Processed: true,
		}
		nasPath, err := client.SyncItem(fallbackContent, timestamp, fallbackMeta)
		if err != nil {
			log.Printf("[WARN] %s fallback write failed: %v", source, err)
		} else {
			log.Printf("[INFO] %s fetch failed, fallback saved: %s", source, nasPath)
		}
		histStore.Append(history.Entry{
			Content:   content,
			Type:      "text",
			NASPath:   nasPath,
			Timestamp: timestamp,
		})
		return
	}

	if client.ExistsByHash(relPath, hash) {
		log.Printf("[INFO] %s remote duplicate detected (hash: %s), skipped entirely", source, hash[:8])
		return
	}

	retryCfg := retry.Config{
		MaxRetries: cfg.Sync.MaxRetries,
		BaseDelay:  time.Duration(cfg.Sync.RetryDelay) * time.Millisecond,
		MaxDelay:   30 * time.Second,
	}

	// AI 处理
	var meta *ssh.ContentMetadata
	var aiTags []string
	if aiProcessor != nil && aiProcessor.ShouldProcess(content) && aiProcessor.ShouldSendToAI(content) {
		aiStart := time.Now()
		fmt.Fprintf(os.Stdout, "%s [INFO] %s AI processing started (len=%d)\n", time.Now().Format("2006/01/02 15:04:05"), source, len(content))
		aiCtx, aiCancel := context.WithTimeout(context.Background(), time.Duration(cfg.AI.Timeout)*time.Second)
		aiResult := aiProcessor.Process(aiCtx, content)
		aiCancel()
		log.Printf("[INFO] %s AI processing done in %.1fs", source, time.Since(aiStart).Seconds())
		if aiResult != nil {
			aiTags = aiResult.Tags
			meta = &ssh.ContentMetadata{
				Title:            aiResult.Title,
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

	// Process 已产出标题和摘要，直接缓存到发布元数据，无需再调 AI
	if entryID != "" && meta != nil && meta.Title != "" {
		if err := histStore.UpdatePublishMeta(entryID, meta.Title, meta.Summary); err != nil {
			log.Printf("[WARN] Failed to cache publish meta for %s: %v", entryID, err)
		}
	}

	log.Printf("[INFO] %s synced & archived: %s", source, nasPath)

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
	remoteDir := filepath.Join(cfg.SSH.BasePath, "uploads")

	data, err := os.ReadFile(logPath)
	if err != nil {
		log.Printf("[WARN] Log rotate: failed to read log: %v", err)
		return
	}

	// 先确保远程日志目录存在，再上传归档
	if err := sshClient.MkdirAll(remoteDir); err != nil {
		log.Printf("[WARN] Log rotate: failed to create remote dir: %v", err)
		sshClient.ForceReset()
		return
	}

	retryCfg := retry.Config{
		MaxRetries: cfg.Sync.MaxRetries,
		BaseDelay:  time.Duration(cfg.Sync.RetryDelay) * time.Millisecond,
		MaxDelay:   30 * time.Second,
	}
	if err := retry.Do(context.Background(), retryCfg, func() error {
		return sshClient.WriteFile(filepath.Join(remoteDir, archiveName), string(data))
	}); err != nil {
		log.Printf("[WARN] Log rotate: failed to upload to NAS after retries: %v", err)
		sshClient.ForceReset()
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
// daemonPlistPath 返回 LaunchAgent plist 路径；不存在则返回空串。
func daemonPlistPath() string {
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	p := filepath.Join(home, "Library", "LaunchAgents", "com.knowly.daemon.plist")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func stopDaemon() {
	// 优先交给 LaunchAgent：unload 后 KeepAlive 不再拉起，避免「刚 stop 就被 launchd 重启」的循环。
	if plist := daemonPlistPath(); plist != "" {
		out, err := exec.Command("launchctl", "unload", plist).CombinedOutput()
		if err == nil {
			fmt.Println("✓ knowly daemon stopped (LaunchAgent unloaded)")
			return
		}
		fmt.Fprintf(os.Stderr, "launchctl unload failed: %s; fallback to PID file\n", strings.TrimSpace(string(out)))
	}
	// 回退：靠 PID 文件发 SIGTERM
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
	case "start":
		// 优先交给 LaunchAgent：load 让 launchd 拉起并持续看护（KeepAlive）。
		if plist := daemonPlistPath(); plist != "" {
			out, err := exec.Command("launchctl", "load", plist).CombinedOutput()
			if err == nil {
				fmt.Println("✓ knowly daemon started (LaunchAgent loaded)")
				return
			}
			// 已加载时报错，视为已在运行
			if strings.Contains(string(out), "already") {
				fmt.Println("✓ knowly daemon already running (LaunchAgent)")
				return
			}
			fmt.Fprintf(os.Stderr, "launchctl load failed: %s; fallback to re-exec\n", strings.TrimSpace(string(out)))
		}
		// 回退：直接 re-exec 一个独立 daemon（不由 launchd 管理）
		cmd := exec.Command(os.Args[0], "--daemon")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = nil
		if err := cmd.Start(); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("✓ knowly daemon started (PID %d)\n", cmd.Process.Pid)
	case "stop":
		stopDaemon()
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
	case "trim-history":
		n := 200
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &n)
		}

		// 1. 先读取本地记录数
		allEntries, err := histStore.ReadAll()
		if err != nil {
			log.Fatalf("读取全部记录失败: %v", err)
		}
		total := len(allEntries)
		if total <= n {
			fmt.Printf("当前记录数 (%d) 未超过阈值 (%d)，无需截断\n", total, n)
			return
		}

		// 2. 备份完整文件到 NAS
		histPath := filepath.Join(config.GetConfigDir(), "history.jsonl")
		backupName := fmt.Sprintf("history_backup_%s.jsonl", time.Now().Format("20060102_150405"))
		remoteDir := filepath.Join(cfg.SSH.BasePath, "uploads")

		backupClient := ssh.NewClient(&ssh.Config{
			Host:                 cfg.SSH.Host,
			Port:                 cfg.SSH.Port,
			User:                 cfg.SSH.User,
			KeyPath:              cfg.SSH.KeyPath,
			BasePath:             cfg.SSH.BasePath,
			FilenamePrefixLength: cfg.SSH.FilenamePrefixLength,
		})
		if err := backupClient.Connect(); err != nil {
			log.Fatalf("SSH 连接失败: %v", err)
		}

		fmt.Println("正在备份完整历史记录到 NAS...")
		if err := backupClient.MkdirAll(remoteDir); err != nil {
			backupClient.Disconnect()
			log.Fatalf("创建远程目录失败: %v", err)
		}

		data, err := os.ReadFile(histPath)
		if err != nil {
			backupClient.Disconnect()
			log.Fatalf("读取历史文件失败: %v", err)
		}

		if err := backupClient.WriteFile(filepath.Join(remoteDir, backupName), string(data)); err != nil {
			backupClient.Disconnect()
			log.Fatalf("备份到 NAS 失败: %v", err)
		}
		backupClient.Disconnect()
		fmt.Printf("✓ 已备份 %d 条记录到 NAS: %s\n", total, filepath.Join(remoteDir, backupName))

		// 3. 截断本地文件到最近 n 条
		fmt.Printf("正在截断本地历史记录，保留最近 %d 条...\n", n)
		if err := histStore.TrimTo(n); err != nil {
			log.Fatalf("截断历史记录失败: %v", err)
		}
		fmt.Printf("✓ 本地历史记录已截断，保留 %d 条最新记录\n", n)

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
