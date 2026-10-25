package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/authx"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// Cancel 处理 POST /api/v1/orders/{order_id}/cancel。
//
// 成功统一返回 200：首次取消与重复取消的业务结果相同（都是 CANCELLED），
// 不必用 201/204 区分。冲突（已完成等）返回 409 INVALID_STATE_TRANSITION。
func (h *Handler) Cancel(c *gin.Context) {
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

	if err := h.svc.CancelOrder(c.Request.Context(), principal.UserID, orderID); err != nil {
		writeMapped(c, err)
		return
	}
	// 取消后再读一次详情，让客户端拿到最终的 payment_status 与 close_reason，
	// 不必再发一次请求。读取失败不该让已成功的取消变成 500——取消已经提交，
	// 这里只返回一个最小的成功响应。
	detail, err := h.svc.GetOrder(c.Request.Context(), principal.UserID, orderID, false)
	if err != nil {
		httpx.WriteData(c, stdhttp.StatusOK, gin.H{"order_id": orderID, "status": "CANCELLED"})
		return
	}
	httpx.WriteData(c, stdhttp.StatusOK, toOrderDetailResponse(detail))
}
