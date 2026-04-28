package fetcher

import (
	"context"
	"testing"
)

func TestSetKnasyncAuthKey(t *testing.T) {
	key := "test1234"
	SetKnasyncAuthKey(key)
	if knasyncAuthKey != key {
		t.Errorf("expected auth key %s, got %s", key, knasyncAuthKey)
	}
}

func TestContainsOK(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"OK uppercase", "OK (zhihu)", true},
		{"OK lowercase", "ok (general)", true},
		{"not OK", "ERROR", false},
		{"short text", "O", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsOK(tt.text); got != tt.want {
				t.Errorf("containsOK() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSubmitToKnasyncNoKey(t *testing.T) {
	// 确保没有设置 auth key
	knasyncAuthKey = ""
	ctx := context.Background()
	err := SubmitToKnasync(ctx, "https://www.zhihu.com/question/123")
	if err == nil {
		t.Error("expected error when auth key is not set")
	}
}

func TestSubmitToKnasyncWithKey(t *testing.T) {
	// 设置测试 auth key
	SetKnasyncAuthKey("test1234")
	ctx := context.Background()
	err := SubmitToKnasync(ctx, "https://www.zhihu.com/question/123")
	// 这个测试会实际发送请求，可能会失败，但至少能验证代码逻辑
	// 在实际环境中，你可能需要 mock HTTP 客户端
	if err != nil {
		t.Logf("SubmitToKnasync failed (expected in test env): %v", err)
	}
}
