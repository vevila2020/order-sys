package domain

import "time"

// PaymentStatus 是模拟支付单的状态。
type PaymentStatus string

const (
	// StatusPending 表示等待回调。
	StatusPending PaymentStatus = "PENDING"
	// StatusSucceeded 表示支付成功。
	StatusSucceeded PaymentStatus = "SUCCEEDED"
	// StatusFailed 表示支付失败。
	StatusFailed PaymentStatus = "FAILED"
	// StatusCancelled 表示随订单取消/超时而关闭。
	StatusCancelled PaymentStatus = "CANCELLED"
)

// Valid 判断状态取值是否合法，用于反序列化时拒绝脏数据。
func (s PaymentStatus) Valid() bool {
	switch s {
	case StatusPending, StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// IsTerminal 表示该状态不会再迁移。
func (s PaymentStatus) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// Payment 是模拟支付单。每个订单最多一个（数据库 uk_payments_order_id 兜底）。
//
// 状态机：PENDING → SUCCEEDED / FAILED / CANCELLED，三个目标均为终态。
//
// 迁移方法返回 (changed, error) 而不是只返回 error，是因为上层需要区分两种
// 成功：
//   - changed=true：本次真的发生了迁移，需要写 Outbox 事件、推进订单状态；
//   - changed=false：相同结果的重放，什么都不该做。
//
// 若只返回 error，上层无法区分，要么重复写事件，要么在重放时误判为失败。
type Payment struct {
	id            string
	orderID       string
	amountCents   int64
	status        PaymentStatus
	providerRef   string
	failureReason string
	finishedAt    time.Time
	version       int64
}

// NewPayment 构造待支付单。
//
// 允许零金额：全额优惠的订单确实存在，且用"金额必须为正"拒绝它会让这类订单
// 无法进入支付流程。金额一致性校验（与订单总额相等）由 app 层在创建时完成，
// 领域层只守"不为负"这条底线。
func NewPayment(paymentID, orderID string, amountCents int64) (*Payment, error) {
	if paymentID == "" {
		return nil, ErrInvalidPaymentID
	}
	if orderID == "" {
		return nil, ErrInvalidOrderID
	}
	if amountCents < 0 {
		return nil, ErrInvalidAmount
	}
	return &Payment{
		id:          paymentID,
		orderID:     orderID,
		amountCents: amountCents,
		status:      StatusPending,
	}, nil
}

// RehydratePayment 从持久化状态重建支付单。
//
// finishedAt 由调用方从库中读出后传入（用 time.Time 零值表示 NULL），
// 这里不再自己取 now：还原的对象不应凭空产生当前时间。
func RehydratePayment(paymentID, orderID string, amountCents int64, status PaymentStatus,
	providerRef, failureReason string, version int64) (*Payment, error) {
	if !status.Valid() {
		return nil, ErrInvalidStateTransition
	}
	return &Payment{
		id:            paymentID,
		orderID:       orderID,
		amountCents:   amountCents,
		status:        status,
		providerRef:   providerRef,
		failureReason: failureReason,
		version:       version,
	}, nil
}

// ID 返回支付单 ID。
func (p *Payment) ID() string { return p.id }

// OrderID 返回所属订单。
func (p *Payment) OrderID() string { return p.orderID }

// AmountCents 返回支付金额。
func (p *Payment) AmountCents() int64 { return p.amountCents }

// Status 返回当前状态。
func (p *Payment) Status() PaymentStatus { return p.status }

// ProviderRef 返回模拟渠道参考号。
func (p *Payment) ProviderRef() string { return p.providerRef }

// FailureReason 返回失败原因。
func (p *Payment) FailureReason() string { return p.failureReason }

// FinishedAt 返回进入终态的时间；PENDING 时为零值。
func (p *Payment) FinishedAt() time.Time { return p.finishedAt }

// Version 返回版本，供仓储条件更新使用。
func (p *Payment) Version() int64 { return p.version }

// IsTerminal 表示是否已进入终态。
func (p *Payment) IsTerminal() bool { return p.status.IsTerminal() }

// SetProviderRef 记录渠道参考号。
//
// 不递增版本：参考号是渠道回传的附属信息，不构成支付状态变更，
// 让它参与乐观锁只会制造无意义的冲突。
func (p *Payment) SetProviderRef(ref string) { p.providerRef = ref }

// Succeed 把支付标记为成功。
func (p *Payment) Succeed() (bool, error) {
	return p.transition(StatusSucceeded, "")
}

// Fail 把支付标记为失败并记录原因。
func (p *Payment) Fail(reason string) (bool, error) {
	return p.transition(StatusFailed, reason)
}

// Cancel 关闭支付单（订单取消或支付超时时由订单用例调用）。
func (p *Payment) Cancel() (bool, error) {
	return p.transition(StatusCancelled, "")
}

// transition 是所有状态迁移的唯一入口。
//
// 返回 nil error 且 changed=false 的情形只有一种：已经在目标状态，即重放。
// 这在支付回调里是常态而非异常——渠道会重发，客户端会重试。
func (p *Payment) transition(target PaymentStatus, reason string) (bool, error) {
	if p.status == target {
		// 重放：不改状态、不刷新 finishedAt、不递增版本。
		return false, nil
	}
	if p.status.IsTerminal() {
		// 相反结果：已有终态不可被覆盖。
		return false, ErrInvalidStateTransition
	}

	p.status = target
	p.failureReason = reason
	// 时间统一 UTC（需求 §4.2）。用 UTC() 显式转换，避免调用方机器时区影响结果。
	p.finishedAt = time.Now().UTC()
	p.version++
	return true, nil
}
