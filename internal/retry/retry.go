package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

type Config struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

// nonRetryable 标记不可重试的错误（如 4xx 非 429）。
// retry.Do 检测到此错误会立即停止重试。
type nonRetryable struct{ err error }

func (e *nonRetryable) Error() string { return e.err.Error() }
func (e *nonRetryable) Unwrap() error { return e.err }

// Permanent 包装一个错误为不可重试。调用方在 fn 中返回 Permanent(err)
// 即可让 retry.Do 跳过后续重试。
func Permanent(err error) error { return &nonRetryable{err: err} }

// IsPermanent 判断是否为不可重试错误
func IsPermanent(err error) bool {
	var nr *nonRetryable
	return errors.As(err, &nr)
}

// Do 执行带指数退避 + Full Jitter 的重试（ctx 可中断）
// 若 fn 返回 Permanent(err)，立即停止重试并返回该错误。
func Do(ctx context.Context, cfg Config, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			// Full Jitter：等待时间 = rand(0, delay)
			delay := cfg.BaseDelay * time.Duration(1<<uint(attempt-1))
			if delay > cfg.MaxDelay {
				delay = cfg.MaxDelay
			}
			wait := time.Duration(rand.Float64() * float64(delay))

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}

		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
			// 不可重试错误（4xx 非 429 等）直接退出，避免浪费配额
			if IsPermanent(err) {
				return lastErr
			}
		}
	}
	return fmt.Errorf("failed after %d retries: %w", cfg.MaxRetries, lastErr)
}
