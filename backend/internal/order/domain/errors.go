package domain

import "errors"

// 领域哨兵错误，供 errors.Is 判断，由 app 层映射为 httpx.Code。
var (
	// ErrInvalidOrderID 表示 order_id 为空。
	ErrInvalidOrderID = errors.New("order: order_id 无效")
	// ErrInvalidUserID 表示 user_id 为空。用户身份必须来自 JWT，不能为空串。
	ErrInvalidUserID = errors.New("order: user_id 无效")
	// ErrInvalidSKUID 表示明细中 sku_id 为空。
	ErrInvalidSKUID = errors.New("order: sku_id 无效")
	// ErrInvalidQuantity 表示数量非正。
	ErrInvalidQuantity = errors.New("order: 数量必须为正")
	// ErrInvalidPrice 表示单价为负。
	ErrInvalidPrice = errors.New("order: 单价不能为负")
	// ErrAmountOverflow 表示金额运算溢出。
	// 单笔订单不可能达到该量级；出现即说明输入异常，宁可拒绝也不回绕成小数。
	ErrAmountOverflow = errors.New("order: 金额溢出")
	// ErrEmptyOrder 表示订单没有任何明细。
	ErrEmptyOrder = errors.New("order: 订单必须至少包含一条明细")
	// ErrDuplicateSKU 表示同一订单内出现重复 SKU 行。
	ErrDuplicateSKU = errors.New("order: 同一订单内不允许重复 SKU")
	// ErrInvalidStateTransition 表示状态机不允许该迁移。
	ErrInvalidStateTransition = errors.New("order: 当前状态不允许该操作")
)
