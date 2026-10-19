package middleware

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// Timeout 为每个请求建立带截止时间的 context，并在 handler 未产生响应且
// 截止时间已到时返回统一 504 TIMEOUT 错误体。
//
// 设计说明：这里刻意同步调用 c.Next()，不把 handler 放进独立 goroutine。
// gin.Context 会被 sync.Pool 复用，异步执行 handler 会在根请求返回后继续
// 访问已回收的 context，产生数据竞争和难以复现的写入错误。代价是只有
// 尊重 ctx 的 handler 能被真正中断——项目约定所有 I/O 必须传递 context
// （需求 §9.3），因此该约束由编码规范保证，而不是靠中间件强杀 goroutine。
func Timeout(timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if timeout <= 0 {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)

		c.Next()

		// handler 已经写过响应时，以 handler 的结果为准，不在响应后追加错误。
		if c.Writer.Written() {
			return
		}
		if err := ctx.Err(); err != nil {
			httpx.WriteError(c, httpx.NewError(httpx.CodeTimeout, err))
		}
	}
}
