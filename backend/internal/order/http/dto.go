package http

import (
	"time"

	"github.com/vevila2020/order-sys/internal/order/app"
)

// IdempotencyKeyHeader 是幂等键的请求头名称（ADR-0006 第 1 节）。
const IdempotencyKeyHeader = "Idempotency-Key"

// PlaceOrderItemRequest 是下单请求中的一行。
//
// quantity 用 gte=1 而不是 gte=0：0 件商品没有业务含义，让它进入领域层
// 只会得到一条语义模糊的错误信息。
type PlaceOrderItemRequest struct {
	SKUID    string `json:"sku_id" binding:"required,max=36"`
	Quantity int64  `json:"quantity" binding:"required,gte=1"`
}

// PlaceOrderRequest 是 POST /orders 的请求体。
type PlaceOrderRequest struct {
	Items []PlaceOrderItemRequest `json:"items" binding:"required,min=1,dive"`
}

// OrderItemResponse 是订单明细的对外表示。
//
// 金额字段全部是整数最小货币单位，客户端不得用浮点解析（需求 §4.2）。
type OrderItemResponse struct {
	SKUID          string `json:"sku_id"`
	Quantity       int64  `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	AmountCents    int64  `json:"amount_cents"`
}

// OrderResponse 是下单响应，字段与需求 §8.5 的示例一致。
type OrderResponse struct {
	OrderID       string              `json:"order_id"`
	Status        string              `json:"status"`
	TotalCents    int64               `json:"total_cents"`
	Currency      string              `json:"currency"`
	Items         []OrderItemResponse `json:"items"`
	PaymentID     string              `json:"payment_id"`
	PaymentStatus string              `json:"payment_status"`
	CreatedAt     string              `json:"created_at"`
}

// OrderDetailResponse 是查询订单详情的响应体。
//
// close_reason 用 omitempty：非终态订单没有关闭原因，返回空串会让客户端
// 误以为"因为空原因而关闭"。字段缺失与"无原因"是同一件事，省略更清楚。
type OrderDetailResponse struct {
	OrderID       string              `json:"order_id"`
	UserID        string              `json:"user_id"`
	Status        string              `json:"status"`
	TotalCents    int64               `json:"total_cents"`
	Currency      string              `json:"currency"`
	CloseReason   string              `json:"close_reason,omitempty"`
	Items         []OrderItemResponse `json:"items"`
	PaymentID     string              `json:"payment_id,omitempty"`
	PaymentStatus string              `json:"payment_status,omitempty"`
	CreatedAt     string              `json:"created_at"`
}

// toOrderDetailResponse 把详情视图映射为响应体。
func toOrderDetailResponse(d *app.OrderDetail) OrderDetailResponse {
	items := make([]OrderItemResponse, 0, len(d.Items))
	for _, it := range d.Items {
		items = append(items, OrderItemResponse{
			SKUID:          it.SKUID,
			Quantity:       it.Quantity,
			UnitPriceCents: it.UnitPriceCents,
			AmountCents:    it.AmountCents,
		})
	}
	return OrderDetailResponse{
		OrderID:       d.OrderID,
		UserID:        d.UserID,
		Status:        string(d.Status),
		TotalCents:    d.TotalCents,
		Currency:      d.Currency,
		CloseReason:   d.CloseReason,
		Items:         items,
		PaymentID:     d.PaymentID,
		PaymentStatus: d.PaymentStatus,
		CreatedAt:     d.CreatedAt,
	}
}

// toOrderResponse 把用例视图映射为响应体。
//
// created_at 用 RFC 3339 UTC（需求 §8.1）：数据库存的是不带时区的 DATETIME，
// 必须显式标注 UTC，否则客户端会按本地时区解析。
func toOrderResponse(v *app.OrderView) OrderResponse {
	items := make([]OrderItemResponse, 0, len(v.Items))
	for _, it := range v.Items {
		items = append(items, OrderItemResponse{
			SKUID:          it.SKUID,
			Quantity:       it.Quantity,
			UnitPriceCents: it.UnitPriceCents,
			AmountCents:    it.AmountCents,
		})
	}
	return OrderResponse{
		OrderID:       v.OrderID,
		Status:        string(v.Status),
		TotalCents:    v.TotalCents,
		Currency:      v.Currency,
		Items:         items,
		PaymentID:     v.PaymentID,
		PaymentStatus: v.PaymentStatus,
		CreatedAt:     v.CreatedAt.UTC().Format(time.RFC3339),
	}
}
