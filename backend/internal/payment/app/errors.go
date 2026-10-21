package app

import "errors"

// 持久化视角的错误，与 domain 的业务规则错误分开。
var (
	// ErrPaymentExists 表示该订单已有支付单（唯一索引冲突）。
	ErrPaymentExists = errors.New("payment: 订单已有支付单")
	// ErrPaymentNotFound 表示支付单不存在。
	ErrPaymentNotFound = errors.New("payment: 支付单不存在")
	// ErrPaymentStateConflict 表示支付单状态已被并发推进，条件更新未命中。
	//
	// 它是**并发结果**而非故障：回调和超时任务可能同时到达。调用方应重新读取
	// 当前状态并按稳定结果返回，而不是把它报成 500。
	ErrPaymentStateConflict = errors.New("payment: 支付单状态已被并发修改")
)
