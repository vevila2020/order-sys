// Package ratelimit 定义限流抽象与中间件。
//
// M0 只提供接口和 no-op 实现：需求明确禁止用内存限流冒充最终方案，
// Redis 令牌桶实现属于 M3（ADR-0001、ADR-0008）。这样调用方可以提前接入
// 中间件链，M3 替换实现时无需改动路由。
package ratelimit

import (
	"context"
	"time"
)

// Limiter 判断某个键是否允许本次请求。
//
// retryAfter 仅在 allowed=false 时有意义，用于 Retry-After 响应头；
// 返回 error 表示限流存储不可用，调用方应按失败关闭处理（返回 503），
// 不能因为存储故障放行全部流量。
type Limiter interface {
	Allow(ctx context.Context, key string, cost int) (allowed bool, retryAfter time.Duration, err error)
}

// Noop 是 M0 的占位实现；RATE_LIMIT_ENABLED=false 时使用。
type Noop struct{}

// Allow 永远放行。
func (Noop) Allow(context.Context, string, int) (bool, time.Duration, error) {
	return true, 0, nil
}

// KeyFunc 从请求上下文提取限流键。
type KeyFunc func(ctx context.Context) string
