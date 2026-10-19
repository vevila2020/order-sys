package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/loggerx"
)

// Recover 捕获 handler panic，记录服务端堆栈并返回统一 500 错误体。
// 堆栈只写入日志，绝不进入响应，这是 NFR-005 的硬性要求。
func Recover(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler 是标准的主动中止信号，不应被记录为 panic。
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			loggerx.WithContext(c.Request.Context(), log).Error("panic recovered",
				zap.Any("panic", rec),
				zap.String("method", c.Request.Method),
				zap.String("path", c.FullPath()),
				zap.ByteString("stack", debug.Stack()),
			)
			httpx.WriteError(c, httpx.NewError(httpx.CodeInternal, nil))
		}()
		c.Next()
	}
}
