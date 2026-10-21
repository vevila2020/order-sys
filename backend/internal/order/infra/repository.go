package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/vevila2020/order-sys/internal/order/app"
	"github.com/vevila2020/order-sys/internal/order/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// orderRepo 是 OrderRepository 的 MySQL 实现。
//
// 本包独占 orders / order_items 的写入权：其他模块不得直接读写这两张表
// （ADR-0002 的数据所有权）。需要改变订单状态时调 order 的 app 接口。
type orderRepo struct{}

// NewOrderRepository 构造订单仓储。无状态：事务句柄按调用传入，
// 仓储不持有连接池，也就不可能悄悄开启一个独立事务。
func NewOrderRepository() app.OrderRepository { return orderRepo{} }

// Create 实现 OrderRepository.Create。
func (orderRepo) Create(ctx context.Context, tx db.Tx, o *domain.Order) error {
	if tx == nil {
		return errors.New("order: tx 不能为空")
	}
	if o == nil {
		return errors.New("order: 订单对象不能为空")
	}

	err := tx.WithContext(ctx).Exec(
		`INSERT INTO orders (order_id, user_id, status, total_cents, currency, close_reason, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`,
		o.ID(), o.UserID(), string(o.Status()), o.TotalCents(), o.Currency(), o.CloseReason(), o.Version(),
	).Error
	if err != nil {
		// 唯一索引冲突只可能来自 order_id：并发重放或调用方复用了 ID。
		// 对调用方而言结论相同——不要再建了——因此统一映射。
		if db.IsDuplicateKey(err) {
			return fmt.Errorf("%w: order_id=%s", app.ErrOrderExists, o.ID())
		}
		return fmt.Errorf("写入订单 %s: %w", o.ID(), err)
	}

	// 明细逐行插入：订单行数很小，可读性优先于往返次数。与订单头同事务，
	// 中途失败时订单头一并回滚——不会留下"没有明细的订单"。
	for _, it := range o.Items() {
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO order_items (order_id, sku_id, quantity, unit_price_cents, amount_cents)
			 VALUES (?, ?, ?, ?, ?)`,
			o.ID(), it.SKUID(), it.Quantity(), it.UnitPriceCents(), it.AmountCents(),
		).Error; err != nil {
			return fmt.Errorf("写入订单明细 order_id=%s sku=%s: %w", o.ID(), it.SKUID(), err)
		}
	}
	return nil
}

// GetByID 实现 OrderRepository.GetByID。
func (r orderRepo) GetByID(ctx context.Context, tx db.Tx, orderID string) (*domain.Order, error) {
	return r.load(ctx, tx, orderID, "")
}

// GetByIDForUpdate 实现 OrderRepository.GetByIDForUpdate。
//
// FOR UPDATE 在 RR 隔离下取当前读并加记录锁：读到的状态与随后的条件更新之间
// 不会插入其他事务的修改。只在确实需要"读状态 → 判断 → 写"的用例里使用。
func (r orderRepo) GetByIDForUpdate(ctx context.Context, tx db.Tx, orderID string) (*domain.Order, error) {
	return r.load(ctx, tx, orderID, " FOR UPDATE")
}

// load 读取订单头与明细。suffix 用于追加 FOR UPDATE。
func (orderRepo) load(ctx context.Context, tx db.Tx, orderID, suffix string) (*domain.Order, error) {
	if tx == nil {
		return nil, errors.New("order: tx 不能为空")
	}

	// 列名显式标注：GORM 的字段名推断与迁移里的列名不一致时会静默留下零值。
	var head struct {
		OrderID     string  `gorm:"column:order_id"`
		UserID      string  `gorm:"column:user_id"`
		Status      string  `gorm:"column:status"`
		TotalCents  int64   `gorm:"column:total_cents"`
		Currency    string  `gorm:"column:currency"`
		CloseReason *string `gorm:"column:close_reason"`
		Version     int64   `gorm:"column:version"`
	}
	err := tx.WithContext(ctx).
		Raw(`SELECT order_id, user_id, status, total_cents, currency, close_reason, version
		       FROM orders WHERE order_id = ?`+suffix, orderID).
		Scan(&head).Error
	if err != nil {
		return nil, fmt.Errorf("查询订单 %s: %w", orderID, err)
	}
	if head.OrderID == "" {
		return nil, fmt.Errorf("%w: order_id=%s", app.ErrOrderNotFound, orderID)
	}

	// 明细按 sku_id 排序：确认/释放库存时按固定顺序处理（ADR-0003 第 3 节），
	// 在这里排好序，调用方不必再排一次、也不会漏排。
	var rows []struct {
		SKUID         string `gorm:"column:sku_id"`
		Quantity      int64  `gorm:"column:quantity"`
		UnitPriceCent int64  `gorm:"column:unit_price_cents"`
		AmountCents   int64  `gorm:"column:amount_cents"`
	}
	if err := tx.WithContext(ctx).
		Raw(`SELECT sku_id, quantity, unit_price_cents, amount_cents
		       FROM order_items WHERE order_id = ? ORDER BY sku_id`, orderID).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询订单明细 order_id=%s: %w", orderID, err)
	}
	items := make([]domain.OrderItem, 0, len(rows))
	for _, row := range rows {
		// 用 Rehydrate 而不是 NewOrderItem：金额直接采用存储值，
		// 重新计算会掩盖历史数据与当前算法的差异。
		items = append(items, domain.RehydrateOrderItem(row.SKUID, row.Quantity, row.UnitPriceCent, row.AmountCents))
	}

	closeReason := ""
	if head.CloseReason != nil {
		closeReason = *head.CloseReason
	}
	return domain.RehydrateOrder(head.OrderID, head.UserID, domain.OrderStatus(head.Status),
		items, head.TotalCents, head.Currency, closeReason, head.Version)
}

// Transition 实现 OrderRepository.Transition。
//
// 条件更新要求当前状态等于 from：0 行表示订单已被其他流程推进（取消与支付
// 并发），属于预期内的并发结果，返回 ErrOrderStateConflict 而不是报错。
// version 同步自增，便于审计与排查。
func (orderRepo) Transition(ctx context.Context, tx db.Tx, orderID string, from, to domain.OrderStatus, closeReason string) error {
	if tx == nil {
		return errors.New("order: tx 不能为空")
	}
	if !from.Valid() || !to.Valid() {
		return domain.ErrInvalidStateTransition
	}
	// 迁移的法律性由领域层判断；仓储只负责"当状态仍是 from 时才写"。
	// 这里再挡一次"from == to"，避免把 no-op 写成一条无意义的状态变更记录。
	if from == to {
		return errors.New("order: from 与 to 相同，无需迁移")
	}

	result := tx.WithContext(ctx).Exec(
		`UPDATE orders
		    SET status = ?, close_reason = NULLIF(?, ''), version = version + 1, updated_at = UTC_TIMESTAMP(6)
		  WHERE order_id = ? AND status = ?`,
		string(to), closeReason, orderID, string(from),
	)
	if result.Error != nil {
		return fmt.Errorf("迁移订单状态 %s: %w", orderID, result.Error)
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return fmt.Errorf("%w: order_id=%s from=%s", app.ErrOrderStateConflict, orderID, from)
	default:
		return fmt.Errorf("order: 状态迁移影响了 %d 行（期望 1），order_id 唯一索引可能已损坏", result.RowsAffected)
	}
}

// ListByUser 实现 OrderRepository.ListByUser。
//
// 游标分页（created_at DESC, order_id DESC）：并发写入时不会像 offset 那样
// 漏单或重复，且翻页成本不随页码增长。索引 idx_orders_user_created 的前两列
// 正好是 user_id 与 created_at，order_id 在索引外回表但只影响同一时刻的并列行。
func (r orderRepo) ListByUser(ctx context.Context, tx db.Tx, userID string, limit int, cursor app.Cursor) ([]*domain.Order, error) {
	if tx == nil {
		return nil, errors.New("order: tx 不能为空")
	}
	if limit <= 0 {
		return nil, errors.New("order: limit 必须为正")
	}

	query := `SELECT order_id, user_id, status, total_cents, currency, close_reason, version
	            FROM orders WHERE user_id = ?`
	args := []any{userID}
	if !cursor.IsZero() {
		// 同一时刻可能有多条订单，必须用 (created_at, order_id) 做严格小于，
		// 只比 created_at 会漏掉与游标同一时刻的其余行。
		query += ` AND (created_at < ? OR (created_at = ? AND order_id < ?))`
		args = append(args, cursor.CreatedAt.UTC(), cursor.CreatedAt.UTC(), cursor.OrderID)
	}
	query += ` ORDER BY created_at DESC, order_id DESC LIMIT ?`
	args = append(args, limit)

	var rows []struct {
		OrderID     string  `gorm:"column:order_id"`
		UserID      string  `gorm:"column:user_id"`
		Status      string  `gorm:"column:status"`
		TotalCents  int64   `gorm:"column:total_cents"`
		Currency    string  `gorm:"column:currency"`
		CloseReason *string `gorm:"column:close_reason"`
		Version     int64   `gorm:"column:version"`
	}
	if err := tx.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询用户订单 user_id=%s: %w", userID, err)
	}

	// 列表页不加载明细：一次翻 20 条就要 20 次明细查询，而列表只展示总额。
	// 需要明细的详情页走 GetByID。
	out := make([]*domain.Order, 0, len(rows))
	for _, row := range rows {
		closeReason := ""
		if row.CloseReason != nil {
			closeReason = *row.CloseReason
		}
		o, err := domain.RehydrateOrder(row.OrderID, row.UserID, domain.OrderStatus(row.Status),
			nil, row.TotalCents, row.Currency, closeReason, row.Version)
		if err != nil {
			return nil, fmt.Errorf("还原订单 %s: %w", row.OrderID, err)
		}
		out = append(out, o)
	}
	return out, nil
}

// 编译期断言：实现必须与端口一致，签名漂移在构建时暴露而不是在装配 main 时。
var _ app.OrderRepository = orderRepo{}
