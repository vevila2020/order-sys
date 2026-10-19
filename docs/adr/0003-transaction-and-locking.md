# ADR-0003: 事务与并发控制

| 项目 | 内容 |
| --- | --- |
| 状态 | Proposed（M1 接受） |
| 日期 | 2026-10-19 |
| 关联需求 | §3.4、§7.3、FR-008、NFR-004 |

## 背景

库存扣减、状态迁移和幂等记录必须在并发下保持正确。应用层"先查再写"在
并发时必然失效，必须依赖数据库的唯一约束、条件更新或行锁兜底。

## 决策（草案）

1. 事务统一通过 `TransactionManager.Do(ctx, fn)` 执行；事务内使用同一个
   `*gorm.DB` 实例，禁止在事务内调用 Kafka/HTTP/Redis 等外部 I/O。
2. 关键写操作用条件更新，并校验 `RowsAffected == 1`：

   ```sql
   UPDATE inventories
      SET available = available - ?, reserved = reserved + ?, version = version + 1
    WHERE sku_id = ? AND available >= ? AND version = ?
   ```

3. 需要读后写的场景使用 `SELECT ... FOR UPDATE`，并按 `sku_id` 字典序加锁，
   降低死锁概率；死锁与锁等待超时最多重试 3 次，指数退避 + 抖动。
4. 幂等与唯一性由数据库唯一约束兜底（`idempotency_records`、
   `orders.order_id`、`reservations.order_id`），不依赖应用层先查。
5. 隔离级别使用 MySQL 默认 `REPEATABLE READ`；条件更新保证不超卖，不额外
   依赖 `SERIALIZABLE`。
6. 所有条件更新附注释说明锁定顺序与并发语义（需求 §3.4 第 1 条）。

## 待办（M1 接受前需补全）

- TransactionManager 的具体接口与 GORM 实现。
- 死锁重试的判定方式（MySQL error 1213 / 1205）。
- 与 Outbox 写入的同一事务边界验证。
- 100 并发下的集成测试与慢查询基线。

## 备选方案

- **应用层加分布式锁。** 拒绝：Redis 不可作为正确性证明，且增加故障面。
- **乐观锁 + 重试整个事务。** 部分采用：`version` 字段保留用于审计与低冲突
  场景，但库存扣减以条件更新为主，避免高冲突时大量重试。

## 后果

- 正面：正确性由数据库保证，可测试、可解释。
- 负面：需要显式 SQL 与锁顺序文档；高冲突 SKU 会串行化。

## 关联

- [ADR-0002](0002-modular-monolith-boundaries.md)
- 需求规格 §3.4、§7.3、§4.5（数据字典中的条件更新语句）
