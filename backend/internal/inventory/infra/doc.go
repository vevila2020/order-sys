// Package infra 提供 SKU、库存与预占的持久化适配器。
//
// 本包独占 skus / inventories / reservations / reservation_items 的写入权。
//
// 库存扣减一律用条件更新而非"先查再写"：
//
//	UPDATE inventories SET available = available - ?, reserved = reserved + ?
//	 WHERE sku_id = ? AND available >= ?
//
// 并校验 RowsAffected == 1。多 SKU 在同一事务内按 sku_id 字典序处理，固定加锁
// 顺序以降低死锁概率（ADR-0003 第 3 节）。条件命中 0 行是并发结果，翻译成
// 领域错误 INSUFFICIENT_STOCK，不重试。
package infra
