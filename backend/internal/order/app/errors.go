package app

import "errors"

// 持久化视角的错误，与 domain 的业务规则错误分开。
//
// 它们描述的是"并发与数据存在性"，不是"业务是否允许"，因此放在 app 层：
// infra 负责识别并返回，HTTP 映射仍由 app 统一完成。
var (
	// ErrOrderExists 表示 order_id 已存在（唯一索引冲突）。
	ErrOrderExists = errors.New("order: 订单已存在")
	// ErrOrderNotFound 表示订单不存在。
	ErrOrderNotFound = errors.New("order: 订单不存在")
	// ErrOrderStateConflict 表示订单状态已被并发推进，本次条件更新未命中。
	ErrOrderStateConflict = errors.New("order: 订单状态已被并发修改")
	// ErrOrderForbidden 表示订单不属于当前用户。
	//
	// 与 ErrOrderNotFound 分开放在 app 层，但对外必须映射成同一个 404：
	// 区分二者等于告诉调用方"这个订单存在，只是不是你的"。
	ErrOrderForbidden = errors.New("order: 无权访问该订单")
)
