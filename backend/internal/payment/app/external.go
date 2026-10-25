package app

import (
	"context"
	"errors"

	"github.com/vevila2020/order-sys/internal/payment/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// Service 编排 payment 模块的用例。
//
// 依赖全部是端口，由 main 注入 infra 实现；事务边界由调用方掌控——
// 支付单必须与订单在同一事务内创建（payment/app 包文档）。
type Service struct {
	payments PaymentRepository
}

// NewService 构造支付用例服务。
func NewService(payments PaymentRepository) *Service {
	if payments == nil {
		panic("payment/app: PaymentRepository 不能为空")
	}
	return &Service{payments: payments}
}

// CreatePayment 在调用方事务内创建待支付单。
//
// paymentID 由调用方传入：TransactionManager 会重试整个事务，若这里生成 ID，
// 重试可能产生多个 payment_id，而幂等快照里记录的只有一个。
//
// 不校验金额与订单总额是否一致：本方法只拿到金额、拿不到订单。一致性由调用方
// （下单用例）保证——它用刚构造的订单总额调用本方法。domain.ErrAmountMismatch
// 留给后续"支付回调校验金额"的路径，那条路径同时持有支付单与订单。
func (s *Service) CreatePayment(ctx context.Context, tx db.Tx, paymentID, orderID string, amountCents int64) error {
	p, err := domain.NewPayment(paymentID, orderID, amountCents)
	if err != nil {
		return err
	}
	return s.payments.Create(ctx, tx, p)
}

// GetPayment 读取支付单（供订单详情展示支付状态）。
func (s *Service) GetPayment(ctx context.Context, tx db.Tx, paymentID string) (*domain.Payment, error) {
	return s.payments.GetByID(ctx, tx, paymentID)
}

// GetByOrderID 读取订单对应的支付单。
func (s *Service) GetByOrderID(ctx context.Context, tx db.Tx, orderID string) (*domain.Payment, error) {
	return s.payments.GetByOrderID(ctx, tx, orderID)
}

// CancelByOrderID 在调用方事务内取消订单的支付单。
//
// 已是终态时返回 nil 而不是错误：取消订单可能被重复触发，也可能与支付回调
// 并发。此时"支付单已经不是 PENDING"是预期结果，真正的冲突（支付已成功、
// 订单已 COMPLETED）由订单状态的条件更新先一步拦下——那条更新成功，说明
// 订单当时确实还是 PENDING_PAYMENT，支付单理论上也应当是 PENDING。
func (s *Service) CancelByOrderID(ctx context.Context, tx db.Tx, orderID string) error {
	p, err := s.payments.GetByOrderID(ctx, tx, orderID)
	if err != nil {
		if errors.Is(err, ErrPaymentNotFound) {
			// 订单没有支付单（历史数据或下单中途的旧版本）：取消订单不需要
			// 支付单配合，直接放行。
			return nil
		}
		return err
	}
	if p.IsTerminal() {
		return nil
	}
	// 条件更新要求 from=PENDING：并发回调若已把状态推进到终态，这里得到
	// ErrPaymentStateConflict，同样说明"已经处理过"，按幂等处理。
	err = s.payments.Transition(ctx, tx, p.ID(), domain.StatusPending, domain.StatusCancelled, "", "")
	if err != nil && errors.Is(err, ErrPaymentStateConflict) {
		return nil
	}
	return err
}
