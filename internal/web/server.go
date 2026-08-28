package web

import (
	"context"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yuanguangshan/knowly/internal/ai"
	"github.com/yuanguangshan/knowly/internal/cluster"
	"github.com/yuanguangshan/knowly/internal/config"
	"github.com/yuanguangshan/knowly/internal/history"
	"github.com/yuanguangshan/knowly/internal/index"

	"github.com/yuanguangshan/knowly/internal/ssh"
)

// SyncTextFn 异步文本同步回调函数（由 main.go 提供，指向 syncText）
type SyncTextFn func(content string, timestamp time.Time)

// SSHClient 接口抽象，便于测试 mock
//
//go:generate go run golang.org/x/tools/cmd/stringer -type=SSHClient 2>/dev/null
type SSHClient interface {
	Connect() error
	MkdirAll(path string) error
	FileExists(path string) bool
	FileSize(path string) (int64, error)
	ReadFile(path string) ([]byte, error)
	ReadFileToWriter(path string, w io.Writer) error
	WriteBinary(path string, data []byte) error
	ListDir(path string) ([]ssh.DirEntry, error)
	BatchExtractTitles(basePath string, entries []ssh.DirEntry) []ssh.TitleEntry
	Search(keyword string, limit int) ([]ssh.SearchResult, error)
	UpdateFileMetadata(path string, meta *ssh.ContentMetadata) error
	MoveFile(src, dst string) error
}

// logSubscriber 日志订阅者
type logSubscriber struct {
	ch   chan string
	done chan struct{}
}

// Server Web 管理界面服务器
type Server struct {
	cfg        *config.Config
	sshClient  SSHClient
	histStore  *history.Store
	aiProcessor *ai.Processor
	clusterEngine *cluster.Engine
	addr       string
	startTime  time.Time
	httpServer *http.Server
	syncTextFn SyncTextFn
	logSubs   []*logSubscriber // 日志实时订阅者列表
	logMu     sync.RWMutex
	sessionKey []byte // 用于签名 session cookie，启动时随机生成
	indexer    index.Indexer // 本地全文索引（可空），支撑 /api/v1 查询
}

// SetIndexer 注入本地全文索引，启用 /api/v1 查询接口。
func (s *Server) SetIndexer(ix index.Indexer) { s.indexer = ix }

// NewServer 创建 Web 服务器实例（创建新的 SSH 和 History 依赖）
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
	aiProcessor := ai.NewProcessor(&cfg.AI)

	return &Server{
		cfg:         cfg,
		sshClient:   sshClient,
		histStore:   histStore,
		aiProcessor: aiProcessor,
		addr:        addr,
		startTime:   time.Now(),
		sessionKey:  sessionKeyInit(),
	}
}

// NewServerWithDeps 创建 Web 服务器实例（使用已有的 SSH 和 History 依赖）
func NewServerWithDeps(cfg *config.Config, addr string, sshClient *ssh.Client, histStore *history.Store, syncTextFn SyncTextFn, clusterEngine *cluster.Engine) *Server {
	aiProcessor := ai.NewProcessor(&cfg.AI)
	s := &Server{
		cfg:        cfg,
		sshClient:  sshClient,
		histStore:  histStore,
		aiProcessor: aiProcessor,
		clusterEngine: clusterEngine,
		addr:       addr,
		startTime:  time.Now(),
		syncTextFn: syncTextFn,
		sessionKey: sessionKeyInit(),
	}
	// 接管日志输出：写入文件的同时推送给 SSE 订阅者
	oldWriter := log.Writer()
	bw := &logBroadcastWriter{s: s}
	log.SetOutput(io.MultiWriter(oldWriter, bw))
	return s
}

// buildHandler 构建路由和中间件
// corsMiddleware 为所有响应添加 CORS 头，允许跨域访问和 iframe 嵌入。
// 用于 Cloudflare Access 等反代场景，避免浏览器 CORS 策略阻断 API 调用。
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 允许所有来源（凭据模式下不能用 *，需要反射 Origin）
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Auth-Key, X-Requested-With")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Max-Age", "86400")
		// 允许任意站点以 iframe 方式嵌入（自己的页面）
		// Vary: Origin，避免缓存混淆
		w.Header().Add("Vary", "Origin")

		// 预检请求直接返回 204
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()

	// 静态文件
	mux.HandleFunc("/", s.serveIndex)
	mux.HandleFunc("/favicon.ico", s.serveFavicon)
	mux.HandleFunc("/manifest.json", s.serveManifest)
	mux.HandleFunc("/sw.js", s.serveSW)

	// 日志 API
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/logs/stream", s.handleLogStream)

	// 归档 API
	mux.HandleFunc("/api/archive/list", s.handleArchiveList)
	mux.HandleFunc("/api/archive/today", s.handleArchiveToday)
	mux.HandleFunc("/api/archive/file", s.handleArchiveFile)
	mux.HandleFunc("/api/archive/download", s.handleArchiveDownload)

	// 历史 API
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/history/{id}", s.handleHistoryEntry)
	mux.HandleFunc("/api/history/{id}/full", s.handleHistoryEntryFull)
	mux.HandleFunc("/api/history/{id}/reprocess", s.handleReprocess)
	mux.HandleFunc("/api/tags", s.handleTags)

	// 状态 API
	mux.HandleFunc("/api/status", s.handleStatus)

	// 管理 API
	mux.HandleFunc("/api/admin/restart", s.handleRestart)
	mux.HandleFunc("/api/admin/update", s.handleUpdate)
	mux.HandleFunc("/api/admin/release", s.handleRelease)

	// 发布 API
	mux.HandleFunc("/api/publish", s.handlePublish)
	mux.HandleFunc("/api/tag-and-publish", s.handleTagAndPublish)

	// 聚类 API
	mux.HandleFunc("/api/clusters", s.handleClusters)
	mux.HandleFunc("/api/clusters/rerun", s.handleClusterRerun)

	// 统计 API
	mux.HandleFunc("/api/stats", s.handleStats)

	// 搜索 API
	mux.HandleFunc("/api/search", s.handleSearch)

	// 对外查询 API（/api/v1，独立 Bearer token 鉴权，供外部服务经 WireGuard 调用）
	if s.cfg.API.IsEnabled() {
		mux.HandleFunc("/api/v1/search", s.handleV1Search)
		mux.HandleFunc("/api/v1/entry", s.handleV1Entry)
		mux.HandleFunc("/api/v1/tags", s.handleV1Tags)
		mux.HandleFunc("/api/v1/status", s.handleV1Status)
		mux.HandleFunc("/api/v1/admin/backfill", s.handleV1Backfill)
	}

	// 文件上传/下载 API
	mux.HandleFunc("/api/upload", s.handleUpload)
	mux.HandleFunc("/api/uploads/download", s.handleUploadsDownload)

	// AI 配置 API
	mux.HandleFunc("/api/config/ai", s.handleAIConfig)

		// 完整配置 API
		mux.HandleFunc("/api/config", s.handleConfig)

	// 登录页和登录 API（无需认证）
	mux.HandleFunc("/login", s.handleLoginPage)
	mux.HandleFunc("/api/login", s.handleLogin)

	// 构建处理链：认证 -> CSRF -> CORS -> Gzip
	handler := http.Handler(mux)
	if s.cfg.Web.Auth != "" {
		handler = s.authMiddleware(mux)
	}
	handler = s.corsMiddleware(handler)
	handler = s.gzipMiddleware(handler)

	return handler
}

// Start 启动 Web 服务器（阻塞）
func (s *Server) Start() error {
	// 连接 SSH
	if err := s.sshClient.Connect(); err != nil {
		log.Printf("[WARN] SSH connect failed: %v (archive browsing will be unavailable)", err)
	}
	return s.serveWithRetry()
}

// StartAsync 启动 Web 服务器（非阻塞）
func (s *Server) StartAsync() {
	go func() {
		if err := s.serveWithRetry(); err != nil {
			log.Printf("[ERROR] Web server error: %v", err)
		}
	}()
}

// serveWithRetry 监听并提供服务；端口被占用等启动失败按退避策略重试
// （2s→5s→10s→30s→60s 封顶），解决双实例抢端口时 Web 服务静默消失、
// 且之后永不恢复的问题（2026-08-26 事故：同步正常但 UI 挂了近两天）。
// 优雅关闭（Shutdown）时返回 nil 退出循环。
func (s *Server) serveWithRetry() error {
	handler := s.buildHandler()
	delays := []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, 60 * time.Second}
	for attempt := 0; ; attempt++ {
		delay := delays[min(attempt, len(delays)-1)]

		ln, err := net.Listen("tcp", s.addr)
		if err != nil {
			log.Printf("[WARN] Web UI listen %s failed: %v, retry in %s (attempt %d)",
				s.addr, err, delay, attempt+1)
			time.Sleep(delay)
			continue
		}
		if attempt > 0 {
			log.Printf("[INFO] Web UI bound to %s after %d retries", s.addr, attempt)
		}
		fmt.Printf("Knowly Web UI 启动: http://localhost%s\n", s.addr)

		srv := &http.Server{Addr: s.addr, Handler: handler}
		s.httpServer = srv
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("[ERROR] Web server error: %v, retry in %s", serveErr, delay)
			time.Sleep(delay)
			continue
		}
		return nil // 正常关闭
	}
}

// Shutdown 优雅关闭 Web 服务器
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// authMiddleware 认证中间件，支持两种认证方式：
// 1. Authorization: Basic header（curl / API / 前端 localStorage 拦截器）
// 2. Session cookie（登录页面设置）
// 未认证时：浏览器请求返回登录页，API 请求返回 401 JSON
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(s.cfg.Web.Auth))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 免认证路径：登录页、登录 API、PWA 资源、首页（由 JS 检查 auth 状态）
		if r.URL.Path == "/" || r.URL.Path == "/login" || r.URL.Path == "/api/login" ||
			r.URL.Path == "/manifest.json" || r.URL.Path == "/sw.js" || r.URL.Path == "/favicon.ico" {
			next.ServeHTTP(w, r)
			return
		}

		// /api/v1 走独立的 Bearer token 鉴权（在 handler 内校验），不走 Basic/session
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(auth), []byte(expectedAuth)) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		// 检查 session cookie
		if s.validateSession(r) {
			next.ServeHTTP(w, r)
			return
		}

		// 未认证：浏览器请求返回登录页，API 请求返回 401
		accept := r.Header.Get("Accept")
		if strings.Contains(accept, "text/html") && r.Method == http.MethodGet {
			s.handleLoginPage(w, r)
			return
		}
		jsonError(w, "Unauthorized", http.StatusUnauthorized)
	})
}

// sessionCookieName session cookie 名称
const sessionCookieName = "knowly_session"

// generateSession 创建签名 session token
func (s *Server) generateSession() (string, error) {
	expiry := time.Now().Add(30 * 24 * time.Hour).Unix()
	token := fmt.Sprintf("%d", expiry)
	return s.signToken(token), nil
}

// validateSession 验证请求中的 session cookie
func (s *Server) validateSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	if !s.verifyToken(cookie.Value) {
		return false
	}
	// 检查过期
	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return false
	}
	return true
}

// signToken 用 sessionKey 签名 token
func (s *Server) signToken(data string) string {
	mac := hmac.New(sha256.New, s.sessionKey)
	mac.Write([]byte(data))
	sig := hex.EncodeToString(mac.Sum(nil))
	return data + "." + sig
}

// verifyToken 验证签名
func (s *Server) verifyToken(token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expected := s.signToken(parts[0])
	return subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1
}

// setSessionCookie 设置 session cookie
func (s *Server) setSessionCookie(w http.ResponseWriter) {
	token, err := s.generateSession()
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 3600, // 30 天
	})
}

// basicAuthDisabled 检查是否配置了 Web 认证
func basicAuthDisabled(auth string) bool {
	return strings.TrimSpace(auth) == ""
}

// sessionKeyInit 初始化 sessionKey（启动时调用）
func sessionKeyInit() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// fallback：用时间戳 + PID 混合（不完美但可用）
		pid := os.Getpid()
		h := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), pid)))
		return h[:]
	}
	return key
}

// logBroadcastWriter 是一个 io.Writer，收到日志行后广播给所有 SSE 订阅者
type logBroadcastWriter struct {
	s *Server
}

func (w *logBroadcastWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n\r")
	w.s.logMu.RLock()
	for _, sub := range w.s.logSubs {
		select {
		case sub.ch <- line:
		default:
		}
	}
	w.s.logMu.RUnlock()
	return len(p), nil
}

// SubscribeLog 订阅实时日志，返回 channel
func (s *Server) SubscribeLog() chan string {
	sub := &logSubscriber{
		ch:   make(chan string, 64),
		done: make(chan struct{}),
	}
	s.logMu.Lock()
	s.logSubs = append(s.logSubs, sub)
	s.logMu.Unlock()
	return sub.ch
}

// UnsubscribeLog 取消订阅
func (s *Server) UnsubscribeLog(ch chan string) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	for i, sub := range s.logSubs {
		if sub.ch == ch {
			close(sub.done)
			s.logSubs = append(s.logSubs[:i], s.logSubs[i+1:]...)
			break
		}
	}
}

// gzipMiddleware compresses HTML, JSON, and text responses for clients that
// support gzip. SSE streams (/api/logs/stream) and binary downloads are
// excluded. This dramatically reduces transfer size for the 6.7K-line
// index.html and all JSON API payloads, especially over remote WAN links.
func (s *Server) gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip if client doesn't support gzip
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		// Skip SSE streams and file downloads — they handle their own output
		if strings.HasSuffix(r.URL.Path, "/stream") || strings.HasSuffix(r.URL.Path, "/download") {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")

		// Remove Content-Length so gzipWriter.Flush doesn't write a wrong value
		w.Header().Del("Content-Length")

		gz := gzipPool.Get().(*gzip.Writer)
		defer gzipPool.Put(gz)
		gz.Reset(w)
		defer gz.Close()

		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, Writer: gz}, r)
	})
}

// gzipResponseWriter wraps http.ResponseWriter with a gzip.Writer.
// WriteHeader is intercepted to remove Content-Length (invalid after compression)
// and set the Content-Encoding header before status is committed.
type gzipResponseWriter struct {
	http.ResponseWriter
	Writer *gzip.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	return g.Writer.Write(b)
}

func (g *gzipResponseWriter) Flush() {
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		g.Writer.Flush()
		f.Flush()
	}
}

// gzipPool reuses gzip.Writer instances to avoid allocations on every request.
var gzipPool = sync.Pool{
	New: func() interface{} {
		gz, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return gz
	},
}
