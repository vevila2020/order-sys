package http

import (
	"github.com/vevila2020/order-sys/internal/inventory/app"
	"github.com/vevila2020/order-sys/internal/inventory/domain"
)

// CreateSKURequest 是 POST /skus 的请求体。
//
// 校验分两层：binding 标签做"形状"校验（必填、长度、范围），领域构造做
// "语义"校验（价格非负、数量为正）。分层的意义是错误信息更贴近原因——
// 请求体缺字段不该报"库存必须为正"。
type CreateSKURequest struct {
	SKUID        string `json:"sku_id" binding:"required,max=36"`
	Name         string `json:"name" binding:"required,max=200"`
	PriceCents   int64  `json:"price_cents" binding:"gte=0"`
	InitialStock int64  `json:"initial_stock" binding:"gte=0"`
}

// UpdateSKURequest 是 PATCH /skus/{sku_id} 的请求体。
//
// 指针字段表示"可选"：价格 0 是合法值（赠品），不能用零值判断是否提供。
// version 必填：没有它就无法检测丢失更新。
type UpdateSKURequest struct {
	Version    *int64  `json:"version" binding:"required"`
	Name       *string `json:"name" binding:"omitempty,max=200"`
	PriceCents *int64  `json:"price_cents" binding:"omitempty,gte=0"`
	Status     *string `json:"status" binding:"omitempty,oneof=ACTIVE INACTIVE"`
}

// SKUResponse 是 SKU 的对外表示。
//
// 字段名与需求 §8.5 的示例一致。单独定义而不是直接序列化 app.SKUView：
// HTTP 契约与内部结构解耦，内部字段增减不会意外改变 API 形状。
type SKUResponse struct {
	SKUID      string `json:"sku_id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Status     string `json:"status"`
	Version    int64  `json:"version"`
	Available  int64  `json:"available"`
	Reserved   int64  `json:"reserved"`
	Sold       int64  `json:"sold"`
}

// toSKUResponse 把用例视图映射为响应体。
func toSKUResponse(v *app.SKUView) SKUResponse {
	return SKUResponse{
		SKUID:      v.SKUID,
		Name:       v.Name,
		PriceCents: v.PriceCents,
		Status:     string(v.Status),
		Version:    v.Version,
		Available:  v.Available,
		Reserved:   v.Reserved,
		Sold:       v.Sold,
	}
}

// toUpdateInput 把请求体转换为用例输入。
//
// status 字符串在这里转成领域类型：非法值由 binding 的 oneof 拦下，
// 这里再兜一次，避免 binding 标签被人改宽后非法值流入领域层。
func (r UpdateSKURequest) toUpdateInput(skuID string) (app.UpdateSKUInput, error) {
	if r.Version == nil {
		return app.UpdateSKUInput{}, app.ErrInvalidInput
	}
	in := app.UpdateSKUInput{
		SKUID:           skuID,
		ExpectedVersion: *r.Version,
		Name:            r.Name,
		PriceCents:      r.PriceCents,
	}
	if r.Status != nil {
		status := domain.SKUStatus(*r.Status)
		if !status.Valid() {
			return app.UpdateSKUInput{}, app.ErrInvalidInput
		}
		in.Status = &status
	}
	return in, nil
}
