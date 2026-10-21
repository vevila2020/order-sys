package app

import (
	"context"

	"github.com/vevila2020/order-sys/internal/inventory/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// 本文件定义 inventory 模块对外的端口（由 infra 实现）与对内的协作契约。
//
// 端口签名使用 db.Tx 而不是 *gorm.DB：app 层不得 import gorm（archtest 规则），
// 但事务句柄必须传得下去。别名的使用约束见 platform/db.Tx——只透传，不调用。

// ReservationRepository 持久化预占及其明细。
//
// 所有方法都在调用方的事务内执行：预占、库存扣减与订单写入必须原子提交
// （ADR-0003 第 1 节）。仓储自己开事务会让"扣了库存但订单回滚"成为可能。
type ReservationRepository interface {
	// Create 写入预占与明细。同一订单重复写入命中 uk_reservations_order_id，
	// 返回 ErrReservationExists——这是并发结果，不是故障。
	Create(ctx context.Context, tx db.Tx, r *domain.Reservation) error
	// GetByOrderID 读取订单对应的预占；不存在返回 ErrReservationNotFound。
	GetByOrderID(ctx context.Context, tx db.Tx, orderID string) (*domain.Reservation, error)
	// Transition 把预占从 RESERVED 迁移到 target。
	//
	// 使用条件更新（WHERE status = 'RESERVED'）。影响行数为 0 表示已被其他流程
	// 推进过，返回 ErrReservationStateConflict，由调用方按稳定结果处理。
	Transition(ctx context.Context, tx db.Tx, reservationID string, target domain.ReservationStatus) error
}

// InventoryRepository 执行库存的条件更新。
//
// 每个方法对应一条 SQL，返回"本次是否真的命中并扣减"：0 行不是错误，而是
// 并发结果（ADR-0003 第 2 节），由调用方翻译成领域错误。
type InventoryRepository interface {
	// Reserve 从 available 扣减并加到 reserved。
	// 返回 domain.ErrInsufficientStock 表示 available 不足或 SKU 不存在。
	Reserve(ctx context.Context, tx db.Tx, skuID string, qty int64) error
	// Confirm 把 reserved 转为 sold。返回 domain.ErrInvalidReservationState
	// 表示 reserved 不足。
	Confirm(ctx context.Context, tx db.Tx, skuID string, qty int64) error
	// Release 从 reserved 归还到 source 指定的桶。
	Release(ctx context.Context, tx db.Tx, skuID string, qty int64, source domain.OrderSource) error
	// Get 读取权威库存，供查询与校验恒等式使用。
	Get(ctx context.Context, tx db.Tx, skuID string) (*domain.Inventory, error)
	// Create 初始化某 SKU 的库存行（总量全部可售）。
	Create(ctx context.Context, tx db.Tx, inv *domain.Inventory) error
}
