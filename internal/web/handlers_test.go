package web

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuanguangshan/knowly/internal/config"
	"github.com/yuanguangshan/knowly/internal/ssh"
)

// newTestMultipartForm 构建一个 multipart/form-data 请求体
func newTestMultipartForm(t *testing.T, fieldName, filename string, content []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("CreateFormFile failed: %v", err)
	}
	part.Write(content)
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestHandleUpload_TextFileTriggersSync(t *testing.T) {
	var mu sync.Mutex
	var syncCalls []struct {
		content   string
		timestamp time.Time
	}

	syncFn := func(content string, timestamp time.Time) {
		mu.Lock()
		defer mu.Unlock()
		syncCalls = append(syncCalls, struct {
			content   string
			timestamp time.Time
		}{content, timestamp})
	}

	s := &Server{
		cfg:        testConfig(),
		sshClient:  &mockSSHClient{files: make(map[string]bool)},
		syncTextFn: syncFn,
	}

	testCases := []string{"article.txt", "note.md", "report.TXT", "README.MD"}
	for _, filename := range testCases {
		t.Run(filename, func(t *testing.T) {
			body, contentType := newTestMultipartForm(t, "file", filename, []byte("test content"))
			req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
			req.Header.Set("Content-Type", contentType)
			rr := httptest.NewRecorder()

			s.handleUpload(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
			}

			var resp map[string]interface{}
			json.Unmarshal(rr.Body.Bytes(), &resp)
			if resp["status"] != "ok" {
				t.Errorf("response status = %v, want ok", resp["status"])
			}
		})
	}

	// 等待异步 goroutine 完成
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(syncCalls) != len(testCases) {
		t.Errorf("syncText called %d times, want %d", len(syncCalls), len(testCases))
	}
}

func TestHandleUpload_NonTextNoSync(t *testing.T) {
	var mu sync.Mutex
	callCount := 0

	syncFn := func(content string, timestamp time.Time) {
		mu.Lock()
		callCount++
		mu.Unlock()
	}

	s := &Server{
		cfg:        testConfig(),
		sshClient:  &mockSSHClient{files: make(map[string]bool)},
		syncTextFn: syncFn,
	}

	testCases := []string{"image.png", "doc.pdf", "data.zip"}
	for _, filename := range testCases {
		t.Run(filename, func(t *testing.T) {
			body, contentType := newTestMultipartForm(t, "file", filename, []byte("binary data"))
			req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
			req.Header.Set("Content-Type", contentType)
			rr := httptest.NewRecorder()

			s.handleUpload(rr, req)

			if rr.Code != http.StatusOK {
				t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
			}
		})
	}

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if callCount != 0 {
		t.Errorf("syncText called %d times for non-text files, want 0", callCount)
	}
}

func TestHandleUpload_NilSyncFn(t *testing.T) {
	// syncTextFn 为 nil 时不应 panic
	s := &Server{
		cfg:        testConfig(),
		sshClient:  &mockSSHClient{files: make(map[string]bool)},
		syncTextFn: nil,
	}

	body, contentType := newTestMultipartForm(t, "file", "test.txt", []byte("hello"))
	req := httptest.NewRequest(http.MethodPost, "/api/upload", body)
	req.Header.Set("Content-Type", contentType)
	rr := httptest.NewRecorder()

	// 不应 panic
	s.handleUpload(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestHandleUpload_WrongMethod(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest(http.MethodGet, "/api/upload", nil)
	rr := httptest.NewRecorder()

	s.handleUpload(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

// testConfig 返回最小配置用于测试
func testConfig() *config.Config {
	return &config.Config{
		Web: config.WebConfig{
			MaxUploadSize:   500 << 20,
			MaxDownloadSize: 500 << 20,
		},
	}
}

// mockSSHClient 是 ssh.Client 的轻量级 mock，仅用于测试 upload 路径
type mockSSHClient struct {
	files map[string]bool
}

func (m *mockSSHClient) MkdirAll(path string) error {
	return nil
}

func (m *mockSSHClient) FileExists(path string) bool {
	return m.files[path]
}

func (m *mockSSHClient) WriteBinary(path string, data []byte) error {
	m.files[path] = true
	return nil
}

func (m *mockSSHClient) FileSize(path string) (int64, error) {
	return 1024, nil // mock 返回 1KB
}

func (m *mockSSHClient) ReadFileToWriter(path string, w io.Writer) error {
	_, err := w.Write([]byte("mock content"))
	return err
}

func (m *mockSSHClient) ReadFile(path string) ([]byte, error) {
	return []byte("mock content"), nil
}

func (m *mockSSHClient) ListDir(path string) ([]ssh.DirEntry, error) {
	return nil, nil
}

func (m *mockSSHClient) BatchExtractTitles(basePath string, entries []ssh.DirEntry) []ssh.TitleEntry {
	return nil
}

func (m *mockSSHClient) Search(keyword string, limit int) ([]ssh.SearchResult, error) {
	return nil, nil
}

func (m *mockSSHClient) UpdateFileMetadata(path string, meta *ssh.ContentMetadata) error {
	return nil
}

func (m *mockSSHClient) MoveFile(src, dst string) error {
	if m.files[src] {
		m.files[dst] = true
	}
	delete(m.files, src)
	return nil
}

func (m *mockSSHClient) Connect() error {
	return nil
}

func TestGzipMiddleware_CompressesHTML(t *testing.T) {
	// Register a handler that writes a large HTML payload
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(strings.Repeat("<div>test content</div>\n", 500)))
	})

	s := &Server{
		cfg: testConfig(),
	}

	handler := s.gzipMiddleware(mux)

	// Request WITH gzip support
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("expected Content-Encoding gzip, got %q", rr.Header().Get("Content-Encoding"))
	}

	// Decompress and verify content
	zr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	decompressed, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read decompressed: %v", err)
	}

	if len(decompressed) != 12000 { // 19 bytes * 500
		t.Errorf("decompressed length = %d, want 12000", len(decompressed))
	}

	// Compressed body should be significantly smaller
	if rr.Body.Len() >= len(decompressed) {
		t.Errorf("compressed body (%d) should be smaller than original (%d)",
			rr.Body.Len(), len(decompressed))
	}
}

func TestGzipMiddleware_SkipsWhenNotAccepted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("plain text response"))
	})

	s := &Server{cfg: testConfig()}
	handler := s.gzipMiddleware(mux)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No Accept-Encoding header
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Content-Encoding") == "gzip" {
		t.Error("should not gzip when client doesn't accept it")
	}
	if rr.Body.String() != "plain text response" {
		t.Errorf("body = %q, want plain text", rr.Body.String())
	}
}

func TestGzipMiddleware_SkipsSSEStream(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("data: test\n\n"))
	})

	s := &Server{cfg: testConfig()}
	handler := s.gzipMiddleware(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/logs/stream", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Content-Encoding") == "gzip" {
		t.Error("SSE stream should not be gzipped")
	}
}
