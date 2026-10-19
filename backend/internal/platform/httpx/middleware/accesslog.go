package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/vevila2020/order-sys/internal/platform/loggerx"
)

// AccessLog 记录每次请求的方法、路由模板、状态码与耗时。
//
// 使用 c.FullPath() 而不是原始 URL，避免把用户输入写进日志并控制指标基数；
// 未匹配路由时 FullPath 为空，显式标记为 unknown。
func AccessLog(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unknown"
		}
		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("route", route),
			zap.Int("status", c.Writer.Status()),
			zap.Duration("duration", time.Since(start)),
			zap.Int("size", c.Writer.Size()),
			zap.String("client_ip", c.ClientIP()),
		}
		// 4xx 是客户端问题，5xx 才需要 error 级别，便于告警规则区分。
		l := loggerx.WithContext(c.Request.Context(), log)
		switch {
		case c.Writer.Status() >= 500:
			l.Error("http request", fields...)
		case c.Writer.Status() >= 400:
			l.Warn("http request", fields...)
		default:
			l.Info("http request", fields...)
		}
	}
}
