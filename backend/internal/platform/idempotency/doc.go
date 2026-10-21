// Package idempotency 提供幂等键、请求指纹与幂等记录存储。
//
// 语义固定于 ADR-0006，本包只实现"记录"这一层，不包含任何业务规则：
//
//   - 键的三元组作用域是 (scope, user_id, key)，由数据库唯一索引保证隔离；
//     user_id 必须来自 JWT，不得取自请求体。
//   - 记录与业务数据写在同一个事务内，因此不存在"回滚后留下 IN_PROGRESS"的
//     故障形态，也就不需要补偿删除或租约。调用方必须把 Platform 传入的 tx
//     用于业务写入。
//   - 快照只保存成功响应体的 data 部分；request_id 属于单次请求，重放时重新生成。
//
// 本包不决定 HTTP 状态码：Begin 返回的 ErrInProgress / ErrKeyReused 由业务层
// （app）映射为 IDEMPOTENCY_IN_PROGRESS / IDEMPOTENCY_CONFLICT。这样平台包
// 不必知道 HTTP 的存在，测试也可以脱离 Gin 运行。
package idempotency
