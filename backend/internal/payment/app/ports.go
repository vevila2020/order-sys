package app

import (
	"context"

	"github.com/vevila2020/order-sys/internal/payment/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// PaymentRepository 持久化支付单。
//
// 事务句柄由调用方传入：支付单、订单、预占与 Outbox 必须在同一事务内提交
// （payment/app 的包文档），仓储自行开事务会破坏这个原子性。
type PaymentRepository interface {
	// Create 写入支付单。同一 order_id 重复写入命中 uk_payments_order_id，
	// 返回 ErrPaymentExists——并发重复下单的结果，不是故障。
	Create(ctx context.Context, tx db.Tx, p *domain.Payment) error

	// GetByID 按 payment_id 读取（回调路径）。不存在返回 ErrPaymentNotFound。
	GetByID(ctx context.Context, tx db.Tx, paymentID string) (*domain.Payment, error)

	// GetByOrderID 按 order_id 读取（订单查询需要展示支付状态）。
	GetByOrderID(ctx context.Context, tx db.Tx, orderID string) (*domain.Payment, error)

	// Transition 以条件更新迁移状态，from 为期望的当前状态。
	//
	// 影响行数为 0 返回 ErrPaymentStateConflict。注意：**重复回调不是这个错误**——
	// 调用方应先按 payment_id 读出当前状态，若已是相同终态则直接返回稳定结果，
	// 根本不进入写入路径。这一点与"条件更新失败"是两回事，见 payment/app 的说明。
	Transition(ctx context.Context, tx db.Tx, paymentID string, from, to domain.PaymentStatus,
		providerRef, failureReason string) error
}
