package infra

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vevila2020/order-sys/internal/payment/app"
	"github.com/vevila2020/order-sys/internal/payment/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// paymentRepo 是 PaymentRepository 的 MySQL 实现。
//
// 本包独占 payments 表的写入权（ADR-0002 数据所有权）。
type paymentRepo struct{}

// NewPaymentRepository 构造支付仓储。无状态，事务句柄按调用传入。
func NewPaymentRepository() app.PaymentRepository { return paymentRepo{} }

// Create 实现 PaymentRepository.Create。
func (paymentRepo) Create(ctx context.Context, tx db.Tx, p *domain.Payment) error {
	if tx == nil {
		return errors.New("payment: tx 不能为空")
	}
	if p == nil {
		return errors.New("payment: 支付单对象不能为空")
	}

	// finished_at 显式写 NULL：新建一定是 PENDING，让"零值时间"与"数据库 NULL"
	// 保持一一对应，读取时不必猜测。
	err := tx.WithContext(ctx).Exec(
		`INSERT INTO payments (payment_id, order_id, amount_cents, status, provider_ref, created_at, updated_at, finished_at)
		 VALUES (?, ?, ?, ?, NULLIF(?, ''), UTC_TIMESTAMP(6), UTC_TIMESTAMP(6), NULL)`,
		p.ID(), p.OrderID(), p.AmountCents(), string(p.Status()), p.ProviderRef(),
	).Error
	if err != nil {
		// uk_payments_order_id 与 uk_payments_payment_id 都可能命中。对调用方而言
		// 结论相同：这笔支付不能再建，应读取既有记录。错误文案带上 order_id
		// 以便排查是"并发重复下单"还是"payment_id 生成重复"。
		if db.IsDuplicateKey(err) {
			return fmt.Errorf("%w: order_id=%s payment_id=%s", app.ErrPaymentExists, p.OrderID(), p.ID())
		}
		return fmt.Errorf("写入支付单 %s: %w", p.ID(), err)
	}
	return nil
}

// GetByID 实现 PaymentRepository.GetByID。
func (r paymentRepo) GetByID(ctx context.Context, tx db.Tx, paymentID string) (*domain.Payment, error) {
	return r.load(ctx, tx, "payment_id", paymentID)
}

// GetByOrderID 实现 PaymentRepository.GetByOrderID。
func (r paymentRepo) GetByOrderID(ctx context.Context, tx db.Tx, orderID string) (*domain.Payment, error) {
	return r.load(ctx, tx, "order_id", orderID)
}

// load 按指定列读取支付单。column 只接受内部白名单值，不是用户输入。
func (paymentRepo) load(ctx context.Context, tx db.Tx, column, value string) (*domain.Payment, error) {
	if tx == nil {
		return nil, errors.New("payment: tx 不能为空")
	}
	// column 来自上面两个方法的字面量；这里再挡一次，避免将来有人把参数透传进来。
	if column != "payment_id" && column != "order_id" {
		return nil, fmt.Errorf("payment: 不支持的查询列 %q", column)
	}

	var row struct {
		PaymentID     string     `gorm:"column:payment_id"`
		OrderID       string     `gorm:"column:order_id"`
		AmountCents   int64      `gorm:"column:amount_cents"`
		Status        string     `gorm:"column:status"`
		ProviderRef   *string    `gorm:"column:provider_ref"`
		FailureReason *string    `gorm:"column:failure_reason"`
		FinishedAt    *time.Time `gorm:"column:finished_at"`
		Version       int64      `gorm:"column:version"`
	}
	err := tx.WithContext(ctx).
		Raw(`SELECT payment_id, order_id, amount_cents, status, provider_ref,
		            failure_reason, finished_at, version
		       FROM payments WHERE `+column+` = ?`, value).
		Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("查询支付单 %s=%s: %w", column, value, err)
	}
	if row.PaymentID == "" {
		return nil, fmt.Errorf("%w: %s=%s", app.ErrPaymentNotFound, column, value)
	}

	// NULL 列用指针接收后转空值：Rehydrate 需要区分"没有原因"与"原因被删"，
	// 但对于字符串两者都表现为空串，因此只需安全解引用。
	providerRef, failureReason := "", ""
	if row.ProviderRef != nil {
		providerRef = *row.ProviderRef
	}
	if row.FailureReason != nil {
		failureReason = *row.FailureReason
	}
	finishedAt := time.Time{}
	if row.FinishedAt != nil {
		finishedAt = *row.FinishedAt
	}

	return domain.RehydratePayment(row.PaymentID, row.OrderID, row.AmountCents,
		domain.PaymentStatus(row.Status), providerRef, failureReason, finishedAt, row.Version)
}

// Transition 实现 PaymentRepository.Transition。
//
// 条件更新要求当前状态等于 from：0 行表示状态已被其他流程推进。
//
// 关于"重复回调 rowsAffected = 0 但不报错"（M1 计划 T11 的表述）：
// 这一点在仓储层无法安全实现。影响行数为 0 有两种完全不同的来源——
//   - 相同结果的重放（渠道重发成功回调），正确响应是最初的结果；
//   - 相反结果的迟到回调（先 FAILED 又来 SUCCEEDED），必须拒绝。
//
// 仓储只看到 "0 行"，分不清二者；把 0 行一律当成功会让第二种情况静默丢失
// 竞态结论。因此仓储返回 ErrPaymentStateConflict，由 app 层重新读取当前状态后
// 判断：相同终态 → 稳定结果；不同终态 → 409。这才是计划想要的行为，且把
// 判定放在能看到业务语义的层。
func (paymentRepo) Transition(ctx context.Context, tx db.Tx, paymentID string, from, to domain.PaymentStatus,
	providerRef, failureReason string) error {
	if tx == nil {
		return errors.New("payment: tx 不能为空")
	}
	if !from.Valid() || !to.Valid() {
		return domain.ErrInvalidStateTransition
	}
	if from == to {
		return errors.New("payment: from 与 to 相同，无需迁移")
	}
	// 只允许 PENDING 出发：三个终态都不可再迁移，这是状态机的硬约束。
	if from.IsTerminal() {
		return fmt.Errorf("%w: from=%s 已是终态", domain.ErrInvalidStateTransition, from)
	}

	result := tx.WithContext(ctx).Exec(
		`UPDATE payments
		    SET status = ?, provider_ref = NULLIF(?, ''), failure_reason = NULLIF(?, ''),
		        finished_at = UTC_TIMESTAMP(6), version = version + 1, updated_at = UTC_TIMESTAMP(6)
		  WHERE payment_id = ? AND status = ?`,
		string(to), providerRef, failureReason, paymentID, string(from),
	)
	if result.Error != nil {
		return fmt.Errorf("迁移支付单状态 %s: %w", paymentID, result.Error)
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return fmt.Errorf("%w: payment_id=%s from=%s", app.ErrPaymentStateConflict, paymentID, from)
	default:
		return fmt.Errorf("payment: 状态迁移影响了 %d 行（期望 1），payment_id 唯一索引可能已损坏", result.RowsAffected)
	}
}

// 编译期断言：签名漂移在构建时暴露。
var _ app.PaymentRepository = paymentRepo{}
