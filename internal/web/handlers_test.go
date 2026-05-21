package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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
