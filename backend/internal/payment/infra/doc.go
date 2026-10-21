// Package infra 提供支付单的持久化适配器。
//
// 本包独占 payments 表的写入权。状态迁移使用条件更新
// （WHERE status = 'PENDING'）并校验 RowsAffected：重复回调命中 0 行属于幂等
// 情形，不视为错误；由 app 层比对当前终态后决定返回 200 还是 409。
package infra
