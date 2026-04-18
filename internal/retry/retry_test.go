package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDo_SuccessFirstTry(t *testing.T) {
	cfg := Config{MaxRetries: 3, BaseDelay: 10 * time.Millisecond, MaxDelay: 100 * time.Millisecond}
	err := Do(context.Background(), cfg, func() error {
		return nil
	})
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestDo_SuccessAfterRetries(t *testing.T) {
	cfg := Config{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	attempts := 0
	err := Do(context.Background(), cfg, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("fail")
		}
		return nil
	})
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestDo_AllRetriesFail(t *testing.T) {
	cfg := Config{MaxRetries: 2, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	err := Do(context.Background(), cfg, func() error {
		return errors.New("always fail")
	})
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestDo_ContextCancellation(t *testing.T) {
	cfg := Config{MaxRetries: 10, BaseDelay: 50 * time.Millisecond, MaxDelay: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after first failure
	attempts := 0
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := Do(ctx, cfg, func() error {
		attempts++
		return errors.New("fail")
	})
	if err == nil {
		t.Error("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestDo_MaxDelayCap(t *testing.T) {
	cfg := Config{MaxRetries: 5, BaseDelay: 1 * time.Hour, MaxDelay: 5 * time.Millisecond}
	start := time.Now()
	err := Do(context.Background(), cfg, func() error {
		return errors.New("fail")
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Error("expected error, got nil")
	}
	// Without the MaxDelay cap, even 1 retry would take 1 hour.
	// With cap at 5ms, all retries should complete quickly.
	if elapsed > 200*time.Millisecond {
		t.Errorf("retry took too long: %v (MaxDelay not working?)", elapsed)
	}
}

func TestDo_ZeroRetries(t *testing.T) {
	cfg := Config{MaxRetries: 0, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond}
	err := Do(context.Background(), cfg, func() error {
		return errors.New("fail")
	})
	if err == nil {
		t.Error("expected error, got nil")
	}
}
