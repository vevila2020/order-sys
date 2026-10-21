package domain

import "errors"

// 领域哨兵错误。
//
// 用哨兵而不是自定义错误类型：调用方只需 errors.Is 判断，不必知道实现细节；
// 由 app 层统一映射为 httpx.Code。领域层不 import httpx——一旦 import，
// 领域就被 HTTP 契约绑死了（ADR-0002）。
var (
	// ErrInvalidSKUID 表示 sku_id 为空或格式不合法。
	ErrInvalidSKUID = errors.New("inventory: sku_id 无效")
	// ErrInvalidOrderID 表示 order_id 为空。
	ErrInvalidOrderID = errors.New("inventory: order_id 无效")
	// ErrInvalidPrice 表示价格小于 0。
	ErrInvalidPrice = errors.New("inventory: 价格不能为负")
	// ErrInvalidQuantity 表示数量不合法（必须为正；构造预占时明细也不能为空）。
	ErrInvalidQuantity = errors.New("inventory: 数量必须为正")
	// ErrDuplicateSKU 表示同一预占内出现重复 SKU 行。
	//
	// 重复行会让确认/释放对同一 SKU 执行两次，因此必须在构造时拒绝，
	// 而不是留给数据库的唯一约束报 1062——那样错误会指向 SQL 而非业务意图。
	ErrDuplicateSKU = errors.New("inventory: 同一预占内不允许重复 SKU")
	// ErrInsufficientStock 表示可售量不足。
	ErrInsufficientStock = errors.New("inventory: 库存不足")
	// ErrInvalidReservationState 表示预占状态不允许该操作，
	// 或确认/释放的数量超过了已预占量。
	ErrInvalidReservationState = errors.New("inventory: 预占状态不允许该操作")
	// ErrInvariantViolated 表示库存恒等式被破坏，属于代码缺陷而非业务错误。
	ErrInvariantViolated = errors.New("inventory: 库存恒等式被破坏")
)
