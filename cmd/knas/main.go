package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yuanguangshan/knas/internal/clipboard"
	"github.com/yuanguangshan/knas/internal/config"
	"github.com/yuanguangshan/knas/internal/history"
	"github.com/yuanguangshan/knas/internal/relay"
	"github.com/yuanguangshan/knas/internal/retry"
	"github.com/yuanguangshan/knas/internal/ssh"
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
			// 继续执行守护逻辑
		} else {
			handleCLI(os.Args[1:], cfg, histStore)
			return
		}
	}

	// 4. 启动守护逻辑
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := client.Connect(); err != nil {
		log.Fatalf("SSH connect failed: %v", err)
	}
	defer client.Disconnect()

	mon.Start()
	log.Println("[INFO] knas daemon started")

	// 5. 启动 Relay 拉取器（如果启用）
	if cfg.Relay.Enabled && cfg.Relay.Endpoint != "" {
		puller := relay.NewPuller(
			cfg.Relay.Endpoint,
			cfg.Relay.Secret,
			time.Duration(cfg.Relay.Interval)*time.Second,
			func(content string) {
				// Relay 内容也走统一的同步+归档流程
				go syncAndArchiveText(client, cfg, content, "relay", histStore)
			},
		)
		puller.Start()
		defer puller.Stop()
		log.Println("[INFO] Relay puller started")
	}

	// 6. 消费 Payload 循环
	for {
		select {
		case <-ctx.Done():
			mon.Stop()
			removePidFile()
			log.Println("[INFO] knas daemon stopped")
			return
		case payload := <-mon.Items():
			go handlePayload(client, cfg, payload, histStore)
		}
	}
}

// handlePayload 处理来自 Monitor 的同步项
func handlePayload(client *ssh.Client, cfg *config.Config, p clipboard.Payload, histStore *history.Store) {
	retryCfg := retry.Config{
		MaxRetries: cfg.Sync.MaxRetries,
		BaseDelay:  time.Duration(cfg.Sync.RetryDelay) * time.Millisecond,
		MaxDelay:   30 * time.Second,
	}

	var nasPath string
	var err error
	var entryType string
	var entryContent string

	switch v := p.(type) {
	case clipboard.TextPayload:
		entryType = "text"
		entryContent = v.Content
		err = retry.Do(context.Background(), retryCfg, func() error {
			path, syncErr := client.SyncItem(v.Content, v.Timestamp)
			if syncErr == nil {
				nasPath = path
			}
			return syncErr
		})
	case clipboard.ImagePayload:
		entryType = "image"
		entryContent = fmt.Sprintf("[IMAGE] %d bytes", len(v.Data))
		err = retry.Do(context.Background(), retryCfg, func() error {
			path, syncErr := client.SyncImage(v.Data, v.Timestamp)
			if syncErr == nil {
				nasPath = path
			}
			return syncErr
		})
	}

	if err != nil {
		log.Printf("[ERROR] Sync failed (%s): %v", entryType, err)
		return
	}

	// nasPath 为空表示内容被去重跳过，不记录历史
	if nasPath == "" {
		log.Printf("[INFO] Duplicate skipped, no history entry")
		return
	}

	// 同步成功 -> 记录历史（含 NASPath）
	histStore.Append(history.Entry{
		Content: entryContent,
		Type:    entryType,
		NASPath: nasPath,
	})
	log.Printf("[INFO] Synced & Archived (%s): %s", entryType, nasPath)
}

// syncAndArchiveText 处理来自 Relay 的文本同步
func syncAndArchiveText(client *ssh.Client, cfg *config.Config, content, source string, histStore *history.Store) {
	retryCfg := retry.Config{
		MaxRetries: cfg.Sync.MaxRetries,
		BaseDelay:  time.Duration(cfg.Sync.RetryDelay) * time.Millisecond,
		MaxDelay:   30 * time.Second,
	}

	var nasPath string
	err := retry.Do(context.Background(), retryCfg, func() error {
		path, syncErr := client.SyncItem(content, time.Now())
		if syncErr == nil {
			nasPath = path
		}
		return syncErr
	})

	if err != nil {
		log.Printf("[ERROR] Relay sync failed: %v", err)
		return
	}

	if nasPath == "" {
		log.Printf("[INFO] Relay duplicate skipped")
		return
	}

	preview := content
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}

	histStore.Append(history.Entry{
		Content: preview,
		Type:    "text",
		NASPath: nasPath,
	})
	log.Printf("[INFO] Relay synced & archived: %s", nasPath)
}

// writePidFile 将当前进程 PID 写入文件
func writePidFile() {
	pidPath := config.GetPidPath()
	os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0644)
}

// removePidFile 删除 PID 文件
func removePidFile() {
	os.Remove(config.GetPidPath())
}

// stopDaemon 停止守护进程
func stopDaemon() {
	pidPath := config.GetPidPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		fmt.Println("knas daemon is not running (no PID file)")
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
	fmt.Printf("✓ knas daemon stopped (PID %d)\n", pid)
}

// showStatus 显示守护进程状态
func showStatus(cfg *config.Config) {
	pidPath := config.GetPidPath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		fmt.Println("✗ knas daemon is not running")
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Println("✗ Invalid PID file")
		return
	}
	if err := syscall.Kill(pid, 0); err != nil {
		fmt.Println("✗ knas daemon is not running (process dead)")
		os.Remove(pidPath)
		return
	}
	fmt.Printf("✓ knas daemon is running (PID: %d)\n", pid)
	if cfg != nil {
		fmt.Printf("  SSH: %s@%s:%s\n", cfg.SSH.User, cfg.SSH.Host, cfg.SSH.Port)
		fmt.Printf("  Base Path: %s\n", cfg.SSH.BasePath)
	}
	histFile := filepath.Join(config.GetConfigDir(), "history.jsonl")
	if data, err := os.ReadFile(histFile); err == nil {
		count := strings.Count(string(data), "\n")
		fmt.Printf("  Total syncs: %d\n", count)
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
			log.Fatal("Usage: knas restore <id>")
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
	default:
		fmt.Println("Unknown command:", cmd)
	}
}
