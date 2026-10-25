package app

import (
	"context"
	"errors"

	"github.com/vevila2020/order-sys/internal/order/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// OrderDetail 是订单详情视图。
//
// 与 OrderView 的区别：OrderView 是下单响应（含 Replayed 与刚创建时的支付状态），
// OrderDetail 是查询结果（含关闭原因与实时支付状态）。两者字段高度重合，但
// 混用会让"下单响应为什么带 close_reason"这类问题无从回答，因此显式分开。
type OrderDetail struct {
	OrderID       string
	UserID        string
	Status        domain.OrderStatus
	TotalCents    int64
	Currency      string
	CloseReason   string
	Items         []OrderItemView
	PaymentID     string
	PaymentStatus string
	CreatedAt     string
}

// GetOrder 读取订单详情。
//
// 归属校验在用例层：调用方只能看自己的订单（需求 §8.1）。他人订单返回
// ErrOrderForbidden，HTTP 层把它与 ErrOrderNotFound 映射成同一个 404——
// 区分二者等于告诉调用方"这个订单存在，只是不是你的"。
//
// asOperator 为 true 时跳过归属校验（运营可以查他人订单）。这是一个显式参数
// 而不是由用例去读角色：角色解析属于 HTTP 层，app 只接收结论。
func (s *Service) GetOrder(ctx context.Context, viewerUserID, orderID string, asOperator bool) (*OrderDetail, error) {
	var detail *OrderDetail
	err := s.tx.Do(ctx, func(ctx context.Context, tx db.Tx) error {
		order, err := s.orders.GetByID(ctx, tx, orderID)
		if err != nil {
			return err
		}
		if !asOperator && order.UserID() != viewerUserID {
			return ErrOrderForbidden
		}

		detail = &OrderDetail{
			OrderID:     order.ID(),
			UserID:      order.UserID(),
			Status:      order.Status(),
			TotalCents:  order.TotalCents(),
			Currency:    order.Currency(),
			CloseReason: order.CloseReason(),
			Items:       toItemViews(order.Items()),
			CreatedAt:   order.CreatedAt().UTC().Format("2006-01-02T15:04:05Z"),
		}

		// 支付信息缺失不视为错误：历史订单或下单中途失败的订单可能没有支付单，
		// 详情接口应当如实返回空字段，而不是让整条查询 404。
		info, err := s.paymentRead.GetByOrderID(ctx, tx, orderID)
		if err != nil {
			if errors.Is(err, ErrPaymentNotFound) {
				return nil
			}
			return err
		}
		detail.PaymentID = info.PaymentID
		detail.PaymentStatus = info.Status
		return nil
	})
	if err != nil {
		return nil, err
	}
	return detail, nil
}
