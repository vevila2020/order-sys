package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/authx"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// Get 处理 GET /api/v1/orders/{order_id}。
//
// 身份取自 token，不取查询参数：否则任何人只要猜到订单号就能读到别人的订单。
func (h *Handler) Get(c *gin.Context) {
	principal, ok := authx.CurrentUser(c.Request.Context())
	if !ok {
		httpx.WriteError(c, httpx.NewError(httpx.CodeUnauthorized, nil))
		return
	}
	orderID := c.Param("order_id")
	if orderID == "" {
		httpx.WriteError(c, httpx.NewError(httpx.CodeValidationFailed, nil))
		return
	}

	detail, err := h.svc.GetOrder(c.Request.Context(), principal.UserID, orderID, isOperator(principal))
	if err != nil {
		writeMapped(c, err)
		return
	}
	httpx.WriteData(c, stdhttp.StatusOK, toOrderDetailResponse(detail))
}

// isOperator 判断调用者是否为运营角色。
//
// 只在查询用例里放开归属校验；取消接口仍限定本人——运营代取消属于后台功能，
// 需求 §8.2 没有该端点，不应顺手放开。
func isOperator(p authx.Principal) bool {
	return p.Role == authx.RoleOperator
}
