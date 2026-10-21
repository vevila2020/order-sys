-- 回滚 000001：按依赖逆序删除。
--
-- 不使用 CASCADE：业务表的删除应当显式列出，避免"少写一张表"被数据库静默
-- 补全，也避免将来有人误以为 CASCADE 是安全默认。这里的顺序是
-- "先删引用方、再删被引用方"：
--
--   reservation_items -> reservations -> inventories -> skus
--   order_items       -> orders
--   payments / idempotency_records / outbox_events（无外键，位置随意）
--
-- down 的用途是开发与演练中的重建，不是生产回滚工具：一旦有真实数据，
-- 回滚意味着数据丢失，应走备份恢复而不是执行本文件。

DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS idempotency_records;
DROP TABLE IF EXISTS payments;

DROP TABLE IF EXISTS order_items;
DROP TABLE IF EXISTS orders;

DROP TABLE IF EXISTS reservation_items;
DROP TABLE IF EXISTS reservations;
DROP TABLE IF EXISTS inventories;
DROP TABLE IF EXISTS skus;
