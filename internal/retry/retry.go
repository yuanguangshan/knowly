package retry

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

type Config struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

// Do 执行带指数退避 + Full Jitter 的重试（ctx 可中断）
func Do(ctx context.Context, cfg Config, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			// Full Jitter：指数退避 + 0~delay 随机
			delay := cfg.BaseDelay * time.Duration(1<<uint(attempt-1))
			if delay > cfg.MaxDelay {
				delay = cfg.MaxDelay
			}
			jitter := time.Duration(rand.Float64() * float64(delay))
			total := delay + jitter

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(total):
			}
		}

		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("failed after %d retries: %w", cfg.MaxRetries, lastErr)
}
