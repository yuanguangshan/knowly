package web

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

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
	startTime time.Time
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
		startTime: time.Now(),
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
	mux.HandleFunc("/api/tags", s.handleTags)

	// 状态 API
	mux.HandleFunc("/api/status", s.handleStatus)

	// 管理 API
	mux.HandleFunc("/api/admin/restart", s.handleRestart)

	// 发布 API
	mux.HandleFunc("/api/publish", s.handlePublish)

	// 统计 API
	mux.HandleFunc("/api/stats", s.handleStats)

	// 搜索 API
	mux.HandleFunc("/api/search", s.handleSearch)

	// 构建处理链：Basic Auth -> 路由
	handler := http.Handler(mux)
	if s.cfg.Web.Auth != "" {
		handler = s.basicAuth(mux)
	}

	fmt.Printf("Knas Web UI 启动: http://localhost%s\n", s.addr)
	return http.ListenAndServe(s.addr, handler)
}

// basicAuth 返回一个 HTTP Basic Auth 中间件
func (s *Server) basicAuth(next http.Handler) http.Handler {
	// 预计算期望的 Authorization header 值
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(s.cfg.Web.Auth))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != expectedAuth {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// basicAuthDisabled 检查是否配置了 Web 认证
func basicAuthDisabled(auth string) bool {
	return strings.TrimSpace(auth) == ""
}
