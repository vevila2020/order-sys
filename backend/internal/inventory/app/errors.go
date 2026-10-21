package app

import "errors"

// app 层的错误在领域错误之外补充"仓储视角"的结论。
//
// 它们与 domain 的哨兵错误分开，是因为它们的来源是持久化行为（并发、缺失行），
// 而不是业务规则。HTTP 映射仍由 app 层统一完成，infra 不 import httpx。
var (
	// ErrReservationExists 表示该订单已有预占（唯一约束冲突）。
	ErrReservationExists = errors.New("inventory: 订单已有预占")
	// ErrReservationNotFound 表示订单没有对应的预占。
	ErrReservationNotFound = errors.New("inventory: 预占不存在")
	// ErrReservationStateConflict 表示预占状态已被其他流程推进，
	// 本次迁移的前提不成立。
	ErrReservationStateConflict = errors.New("inventory: 预占状态已被并发修改")
	// ErrInventoryNotFound 表示 SKU 没有库存行。
	//
	// 与 ErrInsufficientStock 分开：前者是配置/数据缺失（SKU 未初始化库存），
	// 后者是正常业务结论。混为一谈会让"上架忘记建库存"看起来像"卖完了"。
	ErrInventoryNotFound = errors.New("inventory: 库存行不存在")
	// ErrSKUExists 表示 sku_id 已存在。
	ErrSKUExists = errors.New("inventory: SKU 已存在")
	// ErrSKUNotFound 表示 SKU 不存在。
	ErrSKUNotFound = errors.New("inventory: SKU 不存在")
	// ErrSKUVersionConflict 表示更新时携带的 version 已过期（丢失更新检测）。
	ErrSKUVersionConflict = errors.New("inventory: SKU 版本冲突")
	// ErrInvalidInput 表示请求参数本身不合法（与领域规则无关，例如未知的状态字符串）。
	ErrInvalidInput = errors.New("inventory: 请求参数无效")
)
