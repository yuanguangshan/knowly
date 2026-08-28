package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/yuanguangshan/knowly/internal/config"
	"github.com/yuanguangshan/knowly/internal/index"
)

func newV1TestServer(t *testing.T, token string) *Server {
	t.Helper()
	ix, err := index.Open(filepath.Join(t.TempDir(), "idx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })

	cfg := &config.Config{}
	cfg.API.Token = token
	cfg.SSH.BasePath = "/data/archive"
	s := &Server{cfg: cfg, sshClient: &mockSSHClient{}, indexer: ix, startTime: time.Now()}

	now := time.Now()
	if err := ix.Index("2026/08/28/a.md", "/data/archive/2026/08/28/a.md",
		"棱镜之光", "科学", "text", "牛顿的棱镜把白光分解为七色。", now); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestV1SearchTokenAndHit(t *testing.T) {
	s := newV1TestServer(t, "secret1")

	// 无 token：401（配置了 token 时）
	rr := httptest.NewRecorder()
	s.handleV1Search(rr, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=%E6%A3%B1%E9%95%9C", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}

	// 正确 token：命中
	rr2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q=%E6%A3%B1%E9%95%9C", nil)
	req.Header.Set("Authorization", "Bearer secret1")
	s.handleV1Search(rr2, req)
	if rr2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
	var resp struct {
		Count   int `json:"count"`
		Results []struct {
			Path    string `json:"path"`
			NasPath string `json:"nas_path"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 1 || len(resp.Results) != 1 || resp.Results[0].Path != "2026/08/28/a.md" {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Results[0].NasPath == "" {
		t.Fatal("nas_path should be returned for外部回源")
	}
}

func TestV1EntryFromIndexAndSSHFallback(t *testing.T) {
	s := newV1TestServer(t, "")

	// 索引命中：source=index
	rr := httptest.NewRecorder()
	s.handleV1Entry(rr, httptest.NewRequest(http.MethodGet, "/api/v1/entry?path=2026/08/28/a.md", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["source"] != "index" || resp["content"] != "牛顿的棱镜把白光分解为七色。" {
		t.Fatalf("resp = %v", resp)
	}

	// 未命中：回源 SSH（mock 返回 mock content），路径拼回 BasePath
	rr2 := httptest.NewRecorder()
	s.handleV1Entry(rr2, httptest.NewRequest(http.MethodGet, "/api/v1/entry?path=2026/07/01/img.png", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr2.Code)
	}
	var resp2 map[string]interface{}
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if resp2["source"] != "ssh" || resp2["nas_path"] != "/data/archive/2026/07/01/img.png" {
		t.Fatalf("resp2 = %v", resp2)
	}
}

func TestV1TagsAndStatus(t *testing.T) {
	s := newV1TestServer(t, "")

	rr := httptest.NewRecorder()
	s.handleV1Tags(rr, httptest.NewRequest(http.MethodGet, "/api/v1/tags", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("tags: %d", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	s.handleV1Status(rr2, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("status: %d", rr2.Code)
	}
	var st struct {
		Index   string `json:"index"`
		Entries int    `json:"entries"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Index != "ok" || st.Entries != 1 {
		t.Fatalf("status = %+v", st)
	}
}
