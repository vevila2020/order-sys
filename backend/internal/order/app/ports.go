package app

import (
	"context"
	"time"

	"github.com/vevila2020/order-sys/internal/order/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
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
