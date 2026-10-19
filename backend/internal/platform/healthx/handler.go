package healthx

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// ReadyHandler 返回 readiness 处理器：关键依赖失败 503，仅非关键失败 200 + degraded。
// 响应仍使用统一响应体，data 为 Report。
func ReadyHandler(registry *Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		report := registry.Run(c.Request.Context())
		status := http.StatusOK
		if report.Status == StatusFail {
			status = http.StatusServiceUnavailable
		}
		httpx.WriteData(c, status, report)
	}
}

// LiveHandler 返回 liveness 处理器。它只证明进程能响应，不执行任何依赖检查。
func LiveHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		httpx.WriteData(c, http.StatusOK, gin.H{"status": StatusOK})
	}
}
