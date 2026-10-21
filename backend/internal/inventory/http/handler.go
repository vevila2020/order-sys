package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/inventory/app"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// Handler 持有 SKU 端点所需的用例服务。
//
// 只依赖 app.Service（具体类型而非接口）：http 层是模块的入口，它的协作者
// 就是本模块的用例服务，没有必要为它再造一层接口。测试可以构造真实的 Service
// 并注入假仓储——那才是真正的边界。
type Handler struct {
	svc *app.Service
}

// NewHandler 构造 handler。
func NewHandler(svc *app.Service) *Handler {
	if svc == nil {
		panic("inventory/http: Service 不能为空")
	}
	return &Handler{svc: svc}
}

// Create 处理 POST /api/v1/skus。
//
// handler 只做三件事：绑定与校验、调用用例、映射响应。事务与业务规则都在 app 层
// （需求 §9.3）——这里看不到 db、tx 或条件更新。
func (h *Handler) Create(c *gin.Context) {
	var req CreateSKURequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteErrorFrom(c, httpx.NewErrorWithDetails(
			httpx.CodeValidationFailed, "", nil, err,
		))
		return
	}

	view, err := h.svc.CreateSKU(c.Request.Context(), app.CreateSKUInput{
		SKUID:        req.SKUID,
		Name:         req.Name,
		PriceCents:   req.PriceCents,
		InitialStock: req.InitialStock,
	})
	if err != nil {
		writeMapped(c, err)
		return
	}
	// 201：创建资源。响应体里的 version 是后续 PATCH 必须携带的值。
	httpx.WriteData(c, stdhttp.StatusCreated, toSKUResponse(view))
}

// Get 处理 GET /api/v1/skus/{sku_id}。
func (h *Handler) Get(c *gin.Context) {
	skuID := c.Param("sku_id")
	if skuID == "" {
		httpx.WriteError(c, httpx.NewError(httpx.CodeValidationFailed, nil))
		return
	}

	view, err := h.svc.GetSKU(c.Request.Context(), skuID)
	if err != nil {
		writeMapped(c, err)
		return
	}
	httpx.WriteData(c, stdhttp.StatusOK, toSKUResponse(view))
}

// Update 处理 PATCH /api/v1/skus/{sku_id}。
func (h *Handler) Update(c *gin.Context) {
	skuID := c.Param("sku_id")
	if skuID == "" {
		httpx.WriteError(c, httpx.NewError(httpx.CodeValidationFailed, nil))
		return
	}

	var req UpdateSKURequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.WriteErrorFrom(c, httpx.NewErrorWithDetails(
			httpx.CodeValidationFailed, "", nil, err,
		))
		return
	}
	in, err := req.toUpdateInput(skuID)
	if err != nil {
		httpx.WriteErrorFrom(c, httpx.NewError(httpx.CodeValidationFailed, err))
		return
	}

	view, err := h.svc.UpdateSKU(c.Request.Context(), in)
	if err != nil {
		writeMapped(c, err)
		return
	}
	httpx.WriteData(c, stdhttp.StatusOK, toSKUResponse(view))
}

// writeMapped 统一处理错误映射。
//
// 先尝试模块内的映射表；映射不到时退回 WriteErrorFrom，由 httpx.FromError
// 把未知错误折叠为 INTERNAL。这样"漏映射"的后果是 500 而不是泄露内部细节，
// 且新增错误时有明确的两道防线。
func writeMapped(c *gin.Context, err error) {
	if appErr := toAppError(err); appErr != nil {
		httpx.WriteError(c, appErr)
		return
	}
	httpx.WriteErrorFrom(c, err)
}
