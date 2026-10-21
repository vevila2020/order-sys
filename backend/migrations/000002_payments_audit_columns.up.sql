-- 为 payments 补齐领域模型依赖的两列。
--
-- 背景：000001 的 payments 缺少 failure_reason 与 version，而
-- internal/payment/domain.Payment 已有这两个字段，且其单元测试（版本语义、
-- 失败原因记录）覆盖了它们。缺列的后果不是"少一个功能"：
--   - 没有 failure_reason：失败回调的原因无法落库，只能进日志，事后无法追溯
--     某个订单为什么 PAYMENT_FAILED；
--   - 没有 version：无法审计"状态被改过几次"，也无法在排查时区分
--     "一次迁移"与"多次重试后迁移"。
--
-- 两列都允许 NULL / 带默认值，对既有数据可空回填，因此是安全的前向扩展
-- （需求 §9.5 的"扩展/收缩"两步法中的扩展步）。

ALTER TABLE payments
    ADD COLUMN failure_reason VARCHAR(255) NULL COMMENT '进入 FAILED 的原因；其他状态为 NULL' AFTER provider_ref,
    ADD COLUMN version        BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '状态迁移次数，条件更新的审计辅助' AFTER status;
