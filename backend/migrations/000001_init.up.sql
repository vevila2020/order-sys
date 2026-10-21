-- M1 初始 schema：普通订单主链路。
--
-- 约定（需求 §4.5）：
--   * 字符集 utf8mb4 / 排序规则 utf8mb4_unicode_ci，与部署服务器一致。
--     utf8mb4_0900_ai_ci 是 MySQL 8 的默认值，但两者在重音与大小写等价集合上
--     不同，混用会让同一查询在开发库与生产库得到不同结果。
--   * 时间列一律 DATETIME(6) 且存 UTC。不用 TIMESTAMP：后者按会话时区转换，
--     一旦某个客户端忘了设 time_zone 就会写入偏移后的值。
--   * 金额一律 BIGINT UNSIGNED 的最小货币单位，不用 DECIMAL/FLOAT。
--   * 时间戳不设 DEFAULT 与 ON UPDATE：由应用在同一事务内显式写入同一个 now，
--     保证一次事务产生的多行时间一致。ON UPDATE 会在任何 UPDATE 时静默改写，
--     连 DBA 手工修数也会触发，反而掩盖问题。
--   * 状态列用 VARCHAR 而不是 ENUM：ENUM 增加取值要 ALTER TABLE，会锁表且让
--     迁移与代码强耦合；取值合法性由领域层与 CHECK 共同保证。
--
-- 外键只在模块内部建立（inventory 内部、order 内部）。跨模块只存 ID 而不建外键：
-- 模块间是数据所有权边界，外键会把另一模块的删改语义带进来，也会让未来的服务
-- 拆分变成硬依赖。

-- ---------------------------------------------------------------------------
-- inventory 模块：SKU、库存、预占
-- ---------------------------------------------------------------------------

CREATE TABLE skus (
    id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    sku_id      CHAR(36)        NOT NULL COMMENT '业务 ID，对外暴露',
    name        VARCHAR(200)    NOT NULL,
    price_cents BIGINT UNSIGNED NOT NULL COMMENT '整数最小货币单位',
    status      VARCHAR(20)     NOT NULL COMMENT 'ACTIVE / INACTIVE',
    version     BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '乐观锁：更新时带 version 匹配，检测丢失更新',
    created_at  DATETIME(6)     NOT NULL,
    updated_at  DATETIME(6)     NOT NULL,
    deleted_at  DATETIME(6)     NULL COMMENT '软删除只允许用于参考数据，订单相关数据不删',
    PRIMARY KEY (id),
    -- 所有按业务 ID 的查询与更新都走这个唯一索引；也是 inventories 外键的目标。
    UNIQUE KEY uk_skus_sku_id (sku_id),
    -- 后台按状态筛选可售商品；基数低（2 个取值）但配合覆盖查询仍有用。
    KEY idx_skus_status (status)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT = '商品 SKU';

CREATE TABLE inventories (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    sku_id     CHAR(36)        NOT NULL,
    total      BIGINT UNSIGNED NOT NULL COMMENT '权威总量',
    available  BIGINT UNSIGNED NOT NULL COMMENT '普通订单可售量',
    allocated  BIGINT UNSIGNED NOT NULL COMMENT '已划拨给活动额度，活动尚未消耗',
    reserved   BIGINT UNSIGNED NOT NULL COMMENT '普通与活动订单的预占量',
    sold       BIGINT UNSIGNED NOT NULL COMMENT '已确认销量',
    version    BIGINT UNSIGNED NOT NULL DEFAULT 0,
    updated_at DATETIME(6)     NOT NULL,
    PRIMARY KEY (id),
    -- 条件更新的定位索引，也是唯一入口：预占、确认、释放都按 sku_id 精确命中
    -- 单行，InnoDB 取记录锁而不会退化成间隙锁。
    UNIQUE KEY uk_inventories_sku_id (sku_id),
    -- 守护恒等式。条件更新与 CHECK 是双保险：条件更新保证单次变更合法，
    -- CHECK 兜住"某次变更漏改了某一列"这类代码错误，让它在写入时就失败，
    -- 而不是等到对账任务几个小时后才发现。
    CONSTRAINT ck_inventories_balance
        CHECK (available + allocated + reserved + sold = total),
    CONSTRAINT fk_inventories_sku
        FOREIGN KEY (sku_id) REFERENCES skus (sku_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT = '权威库存';

CREATE TABLE reservations (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    reservation_id CHAR(36)        NOT NULL,
    order_id       CHAR(36)        NOT NULL,
    status         VARCHAR(20)     NOT NULL COMMENT 'RESERVED / CONFIRMED / RELEASED',
    created_at     DATETIME(6)     NOT NULL,
    updated_at     DATETIME(6)     NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_reservations_reservation_id (reservation_id),
    -- 每单最多一个预占。这是并发兜底：两个请求同时为同一订单预占时，后到者
    -- 拿到 1062，而不是产生两条预占导致释放两次。
    UNIQUE KEY uk_reservations_order_id (order_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT = '库存预占';

CREATE TABLE reservation_items (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    reservation_id CHAR(36)        NOT NULL,
    sku_id         CHAR(36)        NOT NULL,
    quantity       INT UNSIGNED    NOT NULL,
    PRIMARY KEY (id),
    -- 按预占读明细（确认/释放时遍历）。
    KEY idx_reservation_items_reservation_id (reservation_id),
    -- 同一预占内不允许重复 SKU 行：重复行会让"确认/释放"对同一 SKU 执行两次。
    UNIQUE KEY uk_reservation_items_reservation_sku (reservation_id, sku_id),
    CONSTRAINT fk_reservation_items_reservation
        FOREIGN KEY (reservation_id) REFERENCES reservations (reservation_id),
    CONSTRAINT ck_reservation_items_quantity CHECK (quantity > 0)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT = '库存预占明细';

-- ---------------------------------------------------------------------------
-- order 模块：订单与明细
-- ---------------------------------------------------------------------------

CREATE TABLE orders (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    order_id     CHAR(36)        NOT NULL,
    user_id      VARCHAR(64)     NOT NULL COMMENT '来自 JWT sub，不取请求体',
    status       VARCHAR(32)     NOT NULL COMMENT 'PENDING_PAYMENT / COMPLETED / CANCELLED / PAYMENT_FAILED / PAYMENT_EXPIRED',
    total_cents  BIGINT UNSIGNED NOT NULL COMMENT '下单时金额快照',
    currency     CHAR(3)         NOT NULL DEFAULT 'CNY',
    close_reason VARCHAR(64)     NULL COMMENT '进入终态的原因',
    version      BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '状态条件迁移的辅助列，便于审计与排查',
    created_at   DATETIME(6)     NOT NULL,
    updated_at   DATETIME(6)     NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_orders_order_id (order_id),
    -- "我的订单"列表：按用户取最近创建的一页。DESC 让倒序分页不必额外排序。
    KEY idx_orders_user_created (user_id, created_at DESC),
    -- 超时关单扫描（M4）按状态 + 更新时间取候选，避免全表扫描。
    KEY idx_orders_status_updated (status, updated_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT = '订单';

CREATE TABLE order_items (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    order_id         CHAR(36)        NOT NULL,
    sku_id           CHAR(36)        NOT NULL,
    quantity         INT UNSIGNED    NOT NULL,
    unit_price_cents BIGINT UNSIGNED NOT NULL COMMENT '下单快照，之后改 SKU 价格不影响已有订单',
    amount_cents     BIGINT UNSIGNED NOT NULL COMMENT 'quantity * unit_price_cents',
    PRIMARY KEY (id),
    -- 按订单读明细。
    KEY idx_order_items_order_id (order_id),
    -- 同一订单内不允许重复 SKU 行，否则总额与库存扣减都会重复计算。
    UNIQUE KEY uk_order_items_order_sku (order_id, sku_id),
    CONSTRAINT fk_order_items_order
        FOREIGN KEY (order_id) REFERENCES orders (order_id),
    CONSTRAINT ck_order_items_quantity CHECK (quantity > 0)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT = '订单明细';

-- ---------------------------------------------------------------------------
-- payment 模块：模拟支付单
-- ---------------------------------------------------------------------------

CREATE TABLE payments (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    payment_id   CHAR(36)        NOT NULL,
    order_id     CHAR(36)        NOT NULL,
    amount_cents BIGINT UNSIGNED NOT NULL COMMENT '必须等于订单总额',
    status       VARCHAR(20)     NOT NULL COMMENT 'PENDING / SUCCEEDED / FAILED / CANCELLED',
    provider_ref VARCHAR(128)    NULL COMMENT '模拟渠道参考号',
    created_at   DATETIME(6)     NOT NULL,
    updated_at   DATETIME(6)     NOT NULL,
    finished_at  DATETIME(6)     NULL COMMENT '进入终态的时间',
    PRIMARY KEY (id),
    -- 回调按 payment_id 定位。
    UNIQUE KEY uk_payments_payment_id (payment_id),
    -- 每单最多一个支付单：重复创建支付单会让回调无法确定该改哪一条。
    UNIQUE KEY uk_payments_order_id (order_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT = '支付单';

-- ---------------------------------------------------------------------------
-- platform：幂等记录与 Outbox
-- ---------------------------------------------------------------------------

CREATE TABLE idempotency_records (
    id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    scope             VARCHAR(50)     NOT NULL COMMENT '如 order.create',
    user_id           VARCHAR(64)     NOT NULL,
    idem_key          VARCHAR(128)    NOT NULL COMMENT '请求头 Idempotency-Key 的值；key 是 MySQL 保留字，故加前缀',
    request_hash      CHAR(64)        NOT NULL COMMENT '规范化请求体的 SHA-256，十六进制小写',
    status            VARCHAR(20)     NOT NULL COMMENT 'IN_PROGRESS / COMPLETED',
    response_snapshot JSON            NULL COMMENT '成功响应的 data 部分；request_id 不缓存，重放时重新生成',
    expires_at        DATETIME(6)     NOT NULL COMMENT '保留期截止；清理任务在 M4 落地',
    created_at        DATETIME(6)     NOT NULL,
    updated_at        DATETIME(6)     NOT NULL,
    PRIMARY KEY (id),
    -- ADR-0006 的核心：并发的同键请求在这里排队。后到者的 INSERT 等待先到事务的
    -- 隐式锁，先到提交则收到 1062（重放或冲突），先到回滚则 INSERT 成功。
    -- 唯一性由数据库保证，不依赖应用层先查后写。
    UNIQUE KEY uk_idempotency (scope, user_id, idem_key),
    -- 清理任务按过期时间分批删除（M4），避免全表扫描。
    KEY idx_idempotency_expires (expires_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT = '幂等记录';

CREATE TABLE outbox_events (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT COMMENT '单调递增，决定发布顺序',
    event_id       CHAR(36)        NOT NULL,
    event_type     VARCHAR(100)    NOT NULL,
    aggregate_type VARCHAR(64)     NOT NULL,
    aggregate_id   CHAR(36)        NOT NULL,
    payload        JSON            NOT NULL COMMENT '含 schema_version，便于消费方兼容演进',
    status         VARCHAR(20)     NOT NULL DEFAULT 'PENDING' COMMENT 'PENDING / PUBLISHED / FAILED',
    attempts       INT UNSIGNED    NOT NULL DEFAULT 0,
    available_at   DATETIME(6)     NOT NULL COMMENT '退避后的可发布时间',
    created_at     DATETIME(6)     NOT NULL,
    published_at   DATETIME(6)     NULL,
    PRIMARY KEY (id),
    -- relay 用 SELECT ... FOR UPDATE SKIP LOCKED 按 (status, available_at, id)
    -- 取一批待发布事件；列顺序与查询条件一致，避免回表排序。
    KEY idx_outbox_pending (status, available_at, id),
    -- 消费方与重投按 event_id 定位，也防止同一事件被重复写入 Outbox。
    UNIQUE KEY uk_outbox_events_event_id (event_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci COMMENT = '事务 Outbox';
