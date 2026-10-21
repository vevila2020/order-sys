package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/vevila2020/order-sys/internal/inventory/app"
	"github.com/vevila2020/order-sys/internal/inventory/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// inventoryRepo 是 InventoryRepository 的 MySQL 实现。
//
// 所有写操作都是单条条件更新，不使用"先查再写"：并发下先查后写必然失效
// （ADR-0003 背景）。RowsAffected 是并发结果的判据，0 行必须被翻译成领域错误——
// 静默成功是最危险的形态，客户端会以为扣减生效了。
type inventoryRepo struct{}

// NewInventoryRepository 构造库存仓储。
//
// 无状态：事务句柄由调用方按调用传入，仓储不持有连接池。这样同一个实例可以
// 在任意事务中使用，也不会出现"仓储自己开了一个事务"的隐式行为。
func NewInventoryRepository() app.InventoryRepository { return inventoryRepo{} }

// Reserve 实现 InventoryRepository.Reserve。
//
// SQL 与需求 §4.5 一致。WHERE available >= ? 放在同一条语句里由数据库做原子
// 判定：它同时表达"库存够不够"与"本次扣减"，两者之间没有窗口。
// 不用 version 条件：库存是热点行，版本冲突会把大量请求变成重试风暴
// （ADR-0003 备选方案第 2 条）。version 仍然自增，供审计与排查。
func (inventoryRepo) Reserve(ctx context.Context, tx db.Tx, skuID string, qty int64) error {
	if qty <= 0 {
		// 数量校验在领域层已做过；这里再挡一次是因为负数量会让 SET 变成加法，
		// 直接绕过 available >= ? 的语义。
		return domain.ErrInvalidQuantity
	}
	return updateOne(ctx, tx,
		`UPDATE inventories
		    SET available = available - ?, reserved = reserved + ?, version = version + 1, updated_at = UTC_TIMESTAMP(6)
		  WHERE sku_id = ? AND available >= ?`,
		[]any{qty, qty, skuID, qty},
		domain.ErrInsufficientStock,
	)
}

// Confirm 实现 InventoryRepository.Confirm。
//
// reserved 不足时说明预占记录与库存行不一致（或已被确认过）。用具领域错误
// 而不是"0 行算成功"：重复确认会让 sold 被加两次，对账时无法追溯。
func (inventoryRepo) Confirm(ctx context.Context, tx db.Tx, skuID string, qty int64) error {
	if qty <= 0 {
		return domain.ErrInvalidQuantity
	}
	return updateOne(ctx, tx,
		`UPDATE inventories
		    SET reserved = reserved - ?, sold = sold + ?, version = version + 1, updated_at = UTC_TIMESTAMP(6)
		  WHERE sku_id = ? AND reserved >= ?`,
		[]any{qty, qty, skuID, qty},
		domain.ErrInvalidReservationState,
	)
}

// Release 实现 InventoryRepository.Release。
//
// 回填目标由 source 决定：普通订单回到 available（重新可售），活动订单回到
// allocated（额度只能被同活动继续使用）。这条区分如果放到应用层，会变成
// "调用方记得传对参数"的纪律问题；放在 SQL 里则只有两个分支，评审时一目了然。
func (inventoryRepo) Release(ctx context.Context, tx db.Tx, skuID string, qty int64, source domain.OrderSource) error {
	if qty <= 0 {
		return domain.ErrInvalidQuantity
	}

	targetColumn := "available"
	if source == domain.OrderSourceFlashSale {
		targetColumn = "allocated"
	}

	// targetColumn 来自上面的白名单分支，不是用户输入，因此可以拼进 SQL；
	// 其余参数一律走占位符。
	return updateOne(ctx, tx,
		`UPDATE inventories
		    SET reserved = reserved - ?, `+targetColumn+` = `+targetColumn+` + ?,
		        version = version + 1, updated_at = UTC_TIMESTAMP(6)
		  WHERE sku_id = ? AND reserved >= ?`,
		[]any{qty, qty, skuID, qty},
		domain.ErrInvalidReservationState,
	)
}

// Get 实现 InventoryRepository.Get。
func (inventoryRepo) Get(ctx context.Context, tx db.Tx, skuID string) (*domain.Inventory, error) {
	// 列名显式标注：GORM 的默认命名策略在结构体字段与数据库列之间做推断，
	// 一旦不一致就会静默留下零值。这里逐列写明，让映射错误变成编译期可见的
	// 结构体定义，而不是运行期的空数据。
	var row struct {
		SKUID     string `gorm:"column:sku_id"`
		Total     int64  `gorm:"column:total"`
		Available int64  `gorm:"column:available"`
		Allocated int64  `gorm:"column:allocated"`
		Reserved  int64  `gorm:"column:reserved"`
		Sold      int64  `gorm:"column:sold"`
		Version   int64  `gorm:"column:version"`
	}
	err := tx.WithContext(ctx).
		Raw(`SELECT sku_id, total, available, allocated, reserved, sold, version
		       FROM inventories WHERE sku_id = ?`, skuID).
		Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("查询库存 %s: %w", skuID, err)
	}
	if row.SKUID == "" {
		return nil, app.ErrInventoryNotFound
	}
	// RehydrateInventory 返回的是值类型（库存是可变的值对象，领域层的方法用
	// 指针接收者），这里取地址后交给接口，调用方读到的仍是一份快照。
	inv, err := domain.RehydrateInventory(row.SKUID, row.Total, row.Available, row.Allocated, row.Reserved, row.Sold, row.Version)
	if err != nil {
		return nil, fmt.Errorf("还原库存 %s: %w", skuID, err)
	}
	return &inv, nil
}

// Create 实现 InventoryRepository.Create。
//
// 重复创建命中 uk_inventories_sku_id，翻译成 ErrInventoryExists 之外的
// 唯一冲突语义由调用方判断；这里保留原始 1062 让 db.IsDuplicateKey 可用。
func (inventoryRepo) Create(ctx context.Context, tx db.Tx, inv *domain.Inventory) error {
	if inv == nil {
		return errors.New("inventory: 库存对象不能为空")
	}
	return tx.WithContext(ctx).Exec(
		`INSERT INTO inventories (sku_id, total, available, allocated, reserved, sold, version, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, UTC_TIMESTAMP(6))`,
		inv.SKUID(), inv.Total(), inv.Available(), inv.Allocated(), inv.Reserved(), inv.Sold(), inv.Version(),
	).Error
}

// updateOne 执行单条条件更新，并要求恰好影响一行。
//
// 0 行统一翻译成 conflict：调用方据此返回业务冲突，而不是 500。
// 大于 1 行只可能发生在 sku_id 唯一索引被破坏时，属于数据完整性问题，
// 必须显式失败——静默接受会让后续对账无从下手。
func updateOne(ctx context.Context, tx db.Tx, query string, args []any, conflict error) error {
	if tx == nil {
		return errors.New("inventory: tx 不能为空")
	}
	result := tx.WithContext(ctx).Exec(query, args...)
	if result.Error != nil {
		return result.Error
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return conflict
	default:
		return fmt.Errorf("inventory: 条件更新影响了 %d 行（期望 1），唯一索引可能已被破坏", result.RowsAffected)
	}
}
