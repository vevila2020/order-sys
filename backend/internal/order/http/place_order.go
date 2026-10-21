package http

import (
	"encoding/json"
	"io"
	stdhttp "net/http"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"

	"github.com/vevila2020/order-sys/internal/order/app"
	"github.com/vevila2020/order-sys/internal/platform/authx"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/idempotency"
)

// Handler 持有下单所需的用例服务。
type Handler struct {
	svc *app.Service
}

// NewHandler 构造 handler。
func NewHandler(svc *app.Service) *Handler {
	if svc == nil {
		panic("order/http: Service 不能为空")
	}
	return &Handler{svc: svc}
}

// Place 处理 POST /api/v1/orders。
//
// 步骤与职责边界：
//  1. 从 JWT 上下文取 user_id（绝不取请求体）；
//  2. 校验 Idempotency-Key 的格式与存在；
//  3. 读取**原始请求体字节**并计算指纹；
//  4. 对同一份字节解码 DTO 并校验形状；
//  5. 调用用例，按 Replayed 决定 201 还是 200。
//
// 为什么不能直接用 c.ShouldBindJSON：绑定会消费请求体，之后就无法再对原始
// 字节计算指纹；而对解码后的 DTO 计算指纹会让"新增带默认值的字段"使历史
// 键全部失配（ADR-0006 第 2 节）。因此先读字节，再分别用于指纹与解码。
func (h *Handler) Place(c *gin.Context) {
	principal, ok := authx.CurrentUser(c.Request.Context())
	if !ok {
		// RequireAuth 已保证存在；这里再判断一次，让"误挂到无认证路由"变成 401
		// 而不是把空 user_id 写进库。
		httpx.WriteError(c, httpx.NewError(httpx.CodeUnauthorized, nil))
		return
	}

	key, err := idempotency.Key(c.GetHeader(IdempotencyKeyHeader))
	if err != nil {
		httpx.WriteErrorFrom(c, httpx.NewError(httpx.CodeValidationFailed, err))
		return
	}

	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		httpx.WriteErrorFrom(c, httpx.NewError(httpx.CodeValidationFailed, err))
		return
	}
	hash, err := idempotency.Fingerprint(raw)
	if err != nil {
		// 非法 JSON 在这里就被拒绝：幂等语义要求请求体可被规范化。
		httpx.WriteErrorFrom(c, httpx.NewError(httpx.CodeValidationFailed, err))
		return
	}

	var req PlaceOrderRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		httpx.WriteErrorFrom(c, httpx.NewErrorWithDetails(
			httpx.CodeValidationFailed, "", nil, err,
		))
		return
	}
	// 复用 Gin 已注册的校验器，保证错误信息与其它端点一致。
	if err := binding.Validator.ValidateStruct(&req); err != nil {
		httpx.WriteErrorFrom(c, httpx.NewErrorWithDetails(
			httpx.CodeValidationFailed, "", nil, err,
		))
		return
	}

	items := make([]app.PlaceOrderItem, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, app.PlaceOrderItem{SKUID: it.SKUID, Quantity: it.Quantity})
	}

	view, err := h.svc.PlaceOrder(c.Request.Context(), app.PlaceOrderInput{
		UserID:         principal.UserID,
		IdempotencyKey: key,
		RequestHash:    hash,
		Items:          items,
	})
	if err != nil {
		writeMapped(c, err)
		return
	}

	// 首次 201，重放 200（需求 §8.4）。
	status := stdhttp.StatusCreated
	if view.Replayed {
		status = stdhttp.StatusOK
	}
	httpx.WriteData(c, status, toOrderResponse(view))
}

// writeMapped 先走模块错误映射，映射不到再交给统一兜底（INTERNAL）。
func writeMapped(c *gin.Context, err error) {
	if appErr := toAppError(err); appErr != nil {
		httpx.WriteError(c, appErr)
		return
	}
	httpx.WriteErrorFrom(c, err)
}
