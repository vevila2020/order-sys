package httpx

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/vevila2020/order-sys/internal/platform/ctxkeys"
)

// RequestIDContextKey 是 gin context 中请求 ID 的键；request context 使用
// ctxkeys.RequestID，两者必须保持同一个值。
const RequestIDContextKey = "request_id"

// formatSeconds 把等待时间向上取整为秒，符合 Retry-After 的整数秒格式。
func formatSeconds(d time.Duration) string {
	secs := int(math.Ceil(d.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return fmt.Sprintf("%d", secs)
}

// RequestIDFromContext 返回 context 中的请求 ID；缺失时返回空串。
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ctxkeys.RequestID).(string)
	return id
}

// WithRequestID 把请求 ID 写入 context，供中间件、日志与错误体共享。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxkeys.RequestID, id)
}
