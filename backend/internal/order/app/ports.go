package app

import (
	"context"
	"time"

	"github.com/vevila2020/order-sys/internal/order/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/idempotency"
	"github.com/vevila2020/order-sys/internal/platform/outbox"
)

// 本文件定义 order 模块的端口。签名使用 db.Tx 表示事务句柄（约束见 platform/db），
// infra 实现这些接口，app 只认接口。

// OrderRepository 持久化订单聚合。
type OrderRepository interface {
	// Create 写入订单与明细。同一 order_id 重复写入命中唯一索引，返回
	// ErrOrderExists——这是并发/重试结果，不是故障。
	Create(ctx context.Context, tx db.Tx, o *domain.Order) error

	// GetByID 读取订单及其明细；不存在返回 ErrOrderNotFound。
	GetByID(ctx context.Context, tx db.Tx, orderID string) (*domain.Order, error)

	// GetByIDForUpdate 加行锁读取订单。
	//
	// 只用于"读后写"的状态迁移：读当前状态做校验、再写入新状态。能用条件更新
	// 表达的场景一律用 Transition，不要用行锁扩大争用面（ADR-0003 第 2 节）。
	GetByIDForUpdate(ctx context.Context, tx db.Tx, orderID string) (*domain.Order, error)

	// Transition 以条件更新迁移订单状态。
	//
	// from 是期望的当前状态：影响行数为 0 表示订单已被其他流程推进，
	// 返回 ErrOrderStateConflict，由调用方按稳定结果处理。
	Transition(ctx context.Context, tx db.Tx, orderID string, from, to domain.OrderStatus, closeReason string) error

	// ListByUser 按用户取最近的订单，按 created_at 倒序。
	// cursor 为上一页最后一条的 (createdAt, orderID)；空串表示第一页。
	ListByUser(ctx context.Context, tx db.Tx, userID string, limit int, cursor Cursor) ([]*domain.Order, error)
}

// Cursor 是"我的订单"的翻页游标。
//
// 用 (created_at, order_id) 而不是 offset：offset 分页在并发写入时会漏单或重复，
// 且页码越深扫描越多。order_id 作为第二列打破同一时刻的并列。
type Cursor struct {
	CreatedAt time.Time
	OrderID   string
}

// IsZero 表示第一页。
func (c Cursor) IsZero() bool { return c.OrderID == "" }

// OutboxWriter 是本模块对平台 Outbox 的依赖。
//
// 别名而不是直接引用 outbox.Writer：模块端口归属于本模块，平台能力可以替换
// 实现（例如测试用内存版）而不改调用点。
type OutboxWriter = outbox.Writer

// IdempotencyStore 是本模块对幂等记录存储的依赖。
//
// 别名同样是为了让端口归属于本模块。签名里的 db.Tx 与 outbox 一致。
type IdempotencyStore = idempotency.Store

// SKUReader 是下单用例需要的商品读取端口（由 inventory 模块的 app 提供）。
//
// 只声明"读价格"这一个能力，而不是接收整个 inventory 服务：下单只需要价格
// 快照与可售性判断，多余的依赖会让两个模块的耦合面变大。
type SKUReader interface {
	// ReadForOrder 读取下单所需的 SKU 信息；不存在或已停用返回错误。
	ReadForOrder(ctx context.Context, tx db.Tx, skuID string) (SKUPrice, error)
}

// SKUPrice 是下单时的价格快照输入。
type SKUPrice struct {
	SKUID      string
	PriceCents int64
	// Available 是当前可售量，仅用于快速失败：真正的扣减由库存条件更新保证。
	Available int64
}

// InventoryReserver 是下单事务内扣减库存的端口（由 inventory 模块的 app 提供）。
//
// 方法只做"扣得动/扣不动"，不判断订单状态（inventory/app 包文档）。返回
// domain 层的 ErrInsufficientStock 时，调用方整单回滚。
type InventoryReserver interface {
	// Reserve 在调用方事务内从 available 扣减指定数量。
	Reserve(ctx context.Context, tx db.Tx, skuID string, qty int64) error
	// Release 在调用方事务内把已预占的数量归还（下单后续步骤失败时用于补偿）。
	// 注意：正常情况下不需要补偿——事务回滚会把扣减一并撤销。保留该能力是
	// 为了 M3 的异步流程，M1 不在下单路径调用。
	Release(ctx context.Context, tx db.Tx, skuID string, qty int64) error
}

// ReservationWriter 是创建预占记录的端口（由 inventory 模块的 app 提供）。
type ReservationWriter interface {
	// CreateReservation 在调用方事务内写入预占与明细。
	//
	// reservationID 与 PaymentCreator 同理：由调用方在事务外生成，重试时复用，
	// 避免重试产生多个 ID 而让日志与对账无法对应。
	CreateReservation(ctx context.Context, tx db.Tx, reservationID, orderID string, items []ReservationLine) error
}

// ReservationLine 是预占明细行，与订单明细一一对应。
type ReservationLine struct {
	SKUID    string
	Quantity int64
}

// PaymentCreator 是创建支付单的端口（由 payment 模块的 app 提供）。
type PaymentCreator interface {
	// CreatePayment 在调用方事务内创建待支付单。
	//
	// paymentID 由调用方生成并传入，而不是由实现返回：TransactionManager 会重试
	// 整个事务，若每次尝试都生成新 ID，幂等快照里记录的 payment_id 就可能与
	// 最终落库的那一行不一致。ID 在事务外生成一次，重试复用同一个值。
	CreatePayment(ctx context.Context, tx db.Tx, paymentID, orderID string, amountCents int64) error
}
