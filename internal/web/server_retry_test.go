package web

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/yuanguangshan/knowly/internal/config"
)

// TestServeWithRetry 验证端口被占时按退避重试、端口释放后自动绑定并恢复服务。
// 复现 2026-08-26 事故：双实例抢端口导致 web 静默消失且永不恢复。
func TestServeWithRetry(t *testing.T) {
	addr := ":18291"
	cfg := &config.Config{}
	s := &Server{cfg: cfg, addr: addr, startTime: time.Now(), sessionKey: sessionKeyInit()}

	// 用同栈（tcp 双栈通配）listener 占住端口，确保产生真实 EADDRINUSE
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("cannot occupy test port %s: %v", addr, err)
	}

	done := make(chan error, 1)
	go func() { done <- s.serveWithRetry() }()

	// 1 秒后释放端口；下次退避（2s）重试应成功绑定
	time.Sleep(1 * time.Second)
	_ = ln.Close()

	// 轮询等待服务恢复（最多 15s，覆盖一次退避周期）
	deadline := time.Now().Add(15 * time.Second)
	serving := false
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1" + addr + "/api/v1/status")
		if err == nil {
			resp.Body.Close()
			serving = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !serving {
		t.Fatal("server did not recover after port released")
	}
	if s.httpServer == nil {
		t.Fatal("httpServer should be set after successful bind")
	}

	// 优雅关闭应让 serveWithRetry 返回 nil
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveWithRetry should return nil on graceful shutdown, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveWithRetry did not return after shutdown")
	}
}
