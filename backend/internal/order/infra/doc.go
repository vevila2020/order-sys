// Package infra 提供订单模块的持久化适配器，实现 app 层定义的端口。
//
// 本包独占 order / order_items / outbox_events 的写入权：其他模块不得直接读写
// 这些表（ADR-0002 的数据所有权）。跨模块可见的结果通过 app 层接口或领域事件传递。
//
// 关键写操作使用条件更新（WHERE status = ?）并校验 RowsAffected：结果为 0 表示
// 前提已不成立，属于并发结果而不是错误，须翻译成领域错误交由 app 层映射。
// 显式 SQL 用于关键交易路径，常规读取可用 GORM（需求 §3.4）。
package infra
