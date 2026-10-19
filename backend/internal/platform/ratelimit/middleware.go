package ratelimit

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/authx"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// Middleware 在认证与角色授权之后执行限流。
//
// 放在认证之后的原因：未认证请求应被 401 拒绝，不能消耗限流存储；
// 同时可以优先使用用户 ID 作为键，避免 NAT 后的多个用户共享 IP 配额。
func Middleware(limiter Limiter, enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enabled || limiter == nil {
			c.Next()
			return
		}
		key := KeyFromContext(c)
		allowed, retryAfter, err := limiter.Allow(c.Request.Context(), key, 1)
		if err != nil {
			// 限流存储故障时失败关闭：宁可拒绝也不能放行全部流量。
			httpx.WriteError(c, httpx.NewError(httpx.CodeDependencyUnavailable, err))
			return
		}
		if !allowed {
			if retryAfter <= 0 {
				retryAfter = time.Second
			}
			httpx.WriteError(c, httpx.NewError(httpx.CodeRateLimited, nil).WithRetryAfter(retryAfter))
			return
		}
		c.Next()
	}
}

// KeyFromContext 生成限流键：优先用户 ID，其次客户端 IP。
// 两者都不包含敏感信息，可直接用于 Redis key。
func KeyFromContext(c *gin.Context) string {
	if p, ok := authx.CurrentUser(c.Request.Context()); ok && p.UserID != "" {
		return "user:" + p.UserID
	}
	return "ip:" + c.ClientIP()
}
