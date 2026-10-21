package domain

import "errors"

// 领域哨兵错误，供 errors.Is 判断，由 app 层映射为 httpx.Code。
var (
	// ErrInvalidPaymentID 表示 payment_id 为空。
	ErrInvalidPaymentID = errors.New("payment: payment_id 无效")
	// ErrInvalidOrderID 表示 order_id 为空。
	ErrInvalidOrderID = errors.New("payment: order_id 无效")
	// ErrInvalidAmount 表示金额为负。
	ErrInvalidAmount = errors.New("payment: 金额不能为负")
	// ErrAmountMismatch 表示支付金额与订单总额不一致。
	// 允许支付超过订单金额等于允许少收或多收，两者都会让对账无法收敛。
	ErrAmountMismatch = errors.New("payment: 支付金额与订单总额不一致")
	// ErrInvalidStateTransition 表示状态机不允许该迁移
	// （典型情形：相反结果的迟到回调试图覆盖已有终态）。
	ErrInvalidStateTransition = errors.New("payment: 当前状态不允许该操作")
)
