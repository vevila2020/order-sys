package authx

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// TokenRequest 是本地签发端点的请求体；用户身份由调用方显式指定，
// 仅用于 local/ci 模拟，不读取任何密码。
type TokenRequest struct {
	UserID string `json:"user_id" binding:"required,max=64"`
	Role   Role   `json:"role" binding:"required,oneof=buyer operator"`
}

// TokenResponse 是签发端点的响应体。
type TokenResponse struct {
	Token     string `json:"token"`
	Role      Role   `json:"role"`
	UserID    string `json:"user_id"`
	ExpiresAt string `json:"expires_at"`
}

// TokenHandler 返回本地签发端点处理器。
//
// 端点是否挂载由 main 根据配置决定（local/ci 且 local_auth_enabled=true）；
// handler 内部仍做一次开关校验，防止路由装配遗漏导致误开放。
func TokenHandler(svc *TokenService, enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enabled {
			// 非本地环境按"资源不存在"处理，不暴露端点存在但被禁用的信息。
			httpx.WriteError(c, httpx.NewError(httpx.CodeNotFound, nil))
			return
		}
		var req TokenRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			httpx.WriteErrorFrom(c, httpx.NewErrorWithDetails(
				httpx.CodeValidationFailed, "", nil, err,
			))
			return
		}
		token, claims, err := svc.Issue(req.UserID, req.Role)
		if err != nil {
			httpx.WriteErrorFrom(c, httpx.NewError(httpx.CodeValidationFailed, err))
			return
		}
		httpx.WriteData(c, http.StatusOK, TokenResponse{
			Token:     token,
			Role:      req.Role,
			UserID:    req.UserID,
			ExpiresAt: claims.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
}
