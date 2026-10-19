package authx

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// RequireAuth 校验 Authorization: Bearer <token>，成功后把 Principal 放入
// request context。缺失、格式错误、过期、签名错误统一返回 401 UNAUTHORIZED，
// 不区分具体原因以免泄露信息。
func RequireAuth(svc *TokenService) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := bearerToken(c.GetHeader("Authorization"))
		if raw == "" {
			httpx.WriteError(c, httpx.NewError(httpx.CodeUnauthorized, nil))
			return
		}
		principal, err := svc.Parse(raw)
		if err != nil {
			httpx.WriteError(c, httpx.NewError(httpx.CodeUnauthorized, err))
			return
		}
		c.Request = c.Request.WithContext(WithPrincipal(c.Request.Context(), principal))
		c.Next()
	}
}

// RequireRole 要求调用者具备指定角色；未认证返回 401，角色不符返回 403。
// 管理接口对无权限统一返回 403，不泄露资源是否存在。
func RequireRole(role Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := CurrentUser(c.Request.Context())
		if !ok {
			httpx.WriteError(c, httpx.NewError(httpx.CodeUnauthorized, nil))
			return
		}
		if p.Role != role {
			httpx.WriteError(c, httpx.NewError(httpx.CodeForbidden, nil))
			return
		}
		c.Next()
	}
}

// bearerToken 从 Authorization 头提取 token；格式不合法时返回空串。
func bearerToken(header string) string {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
