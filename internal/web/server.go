package web

import (
	"fmt"
	"log"
	"net/http"

	"github.com/yuanguangshan/knas/internal/config"
	"github.com/yuanguangshan/knas/internal/history"
	"github.com/yuanguangshan/knas/internal/ssh"
)

// Server Web 管理界面服务器
type Server struct {
	cfg       *config.Config
	sshClient *ssh.Client
	histStore *history.Store
	addr      string
}

// NewServer 创建 Web 服务器实例
func NewServer(cfg *config.Config, addr string) *Server {
	sshClient := ssh.NewClient(&ssh.Config{
		Host:                 cfg.SSH.Host,
		Port:                 cfg.SSH.Port,
		User:                 cfg.SSH.User,
		KeyPath:              cfg.SSH.KeyPath,
		BasePath:             cfg.SSH.BasePath,
		FilenamePrefixLength: cfg.SSH.FilenamePrefixLength,
	})
	histStore := history.NewStore(config.GetConfigDir())

	return &Server{
		cfg:       cfg,
		sshClient: sshClient,
		histStore: histStore,
		addr:      addr,
	}
}

// Start 启动 Web 服务器
func (s *Server) Start() error {
	// 连接 SSH
	if err := s.sshClient.Connect(); err != nil {
		log.Printf("[WARN] SSH connect failed: %v (archive browsing will be unavailable)", err)
	}

	mux := http.NewServeMux()

	// 静态文件
	mux.HandleFunc("/", s.serveIndex)

	// 日志 API
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/logs/stream", s.handleLogStream)

	// 归档 API
	mux.HandleFunc("/api/archive/list", s.handleArchiveList)
	mux.HandleFunc("/api/archive/file", s.handleArchiveFile)
	mux.HandleFunc("/api/archive/download", s.handleArchiveDownload)

	// 历史 API
	mux.HandleFunc("/api/history", s.handleHistory)

	// 状态 API
	mux.HandleFunc("/api/status", s.handleStatus)

	fmt.Printf("Knas Web UI 启动: http://localhost%s\n", s.addr)
	return http.ListenAndServe(s.addr, mux)
}
