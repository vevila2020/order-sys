package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/vevila2020/order-sys/internal/inventory/app"
	"github.com/vevila2020/order-sys/internal/inventory/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// reservationRepo 是 ReservationRepository 的 MySQL 实现。
//
// 预占头与明细必须同事务写入：只写入头会让释放时无从知道该归还多少，
// 只写入明细则违反外键约束。两者都在调用方传入的 tx 上执行。
type reservationRepo struct{}

// NewReservationRepository 构造预占仓储。无状态，与库存仓储相同。
func NewReservationRepository() app.ReservationRepository { return reservationRepo{} }

// Create 实现 ReservationRepository.Create。
func (reservationRepo) Create(ctx context.Context, tx db.Tx, r *domain.Reservation) error {
	if tx == nil {
		return errors.New("inventory: tx 不能为空")
	}
	if r == nil {
		return errors.New("inventory: 预占对象不能为空")
	}
	if r.ID() == "" {
		// ID 由 app 层生成（业务标识），仓储不负责发明标识符：否则重试或
		// 补数据时会凭空产生新 ID，无法与外部系统对账。
		return errors.New("inventory: 预占 ID 不能为空")
	}

	err := tx.WithContext(ctx).Exec(
		`INSERT INTO reservations (reservation_id, order_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`,
		r.ID(), r.OrderID(), string(r.Status()),
	).Error
	if err != nil {
		// 1062 有两种来源：reservation_id 重复（调用方生成了重复 ID，属于缺陷）
		// 与 order_id 重复（并发重复下单，属于预期内的并发结果）。
		// 唯一索引 uk_reservations_order_id 的存在让后者成为主要来源；
		// 这里统一报"订单已有预占"，因为对调用方而言结论相同：不要再建了。
		if db.IsDuplicateKey(err) {
			return fmt.Errorf("%w: order_id=%s", app.ErrReservationExists, r.OrderID())
		}
		return fmt.Errorf("写入预占: %w", err)
	}

	items := r.Items()
	// 逐行插入而不是批量拼接 VALUES：明细数量很小（订单行数），可读性优先；
	// 也避免手工拼装占位符出错。
	for _, it := range items {
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO reservation_items (reservation_id, sku_id, quantity)
			 VALUES (?, ?, ?)`,
			r.ID(), it.SKUID, it.Quantity,
		).Error; err != nil {
			// 重复 SKU 已在 domain.NewReservation 拒绝；走到这里说明数据绕过
			// 领域层写入，属于缺陷，原样返回让事务回滚。
			return fmt.Errorf("写入预占明细 sku=%s: %w", it.SKUID, err)
		}
	}
	return nil
}

// GetByOrderID 实现 ReservationRepository.GetByOrderID。
func (reservationRepo) GetByOrderID(ctx context.Context, tx db.Tx, orderID string) (*domain.Reservation, error) {
	if tx == nil {
		return nil, errors.New("inventory: tx 不能为空")
	}

	var head struct {
		ReservationID string `gorm:"column:reservation_id"`
		OrderID       string `gorm:"column:order_id"`
		Status        string `gorm:"column:status"`
	}
	err := tx.WithContext(ctx).
		Raw(`SELECT reservation_id, order_id, status FROM reservations WHERE order_id = ?`, orderID).
		Scan(&head).Error
	if err != nil {
		return nil, fmt.Errorf("查询预占 order_id=%s: %w", orderID, err)
	}
	if head.ReservationID == "" {
		return nil, fmt.Errorf("%w: order_id=%s", app.ErrReservationNotFound, orderID)
	}

	var rows []struct {
		SKUID    string `gorm:"column:sku_id"`
		Quantity int64  `gorm:"column:quantity"`
	}
	if err := tx.WithContext(ctx).
		Raw(`SELECT sku_id, quantity FROM reservation_items WHERE reservation_id = ? ORDER BY sku_id`,
			head.ReservationID).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询预占明细 reservation_id=%s: %w", head.ReservationID, err)
	}
	// 按 sku_id 排序返回：确认/释放要按固定顺序处理（ADR-0003 第 3 节），
	// 排序在这里完成，调用方不必再排一次，也就不会因为漏排而产生交叉锁。
	items := make([]domain.ReservationItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, domain.ReservationItem{SKUID: row.SKUID, Quantity: row.Quantity})
	}

	return domain.RehydrateReservation(head.ReservationID, head.OrderID, domain.ReservationStatus(head.Status), items)
}

// Transition 实现 ReservationRepository.Transition。
//
// 条件更新要求当前状态必须是 RESERVED：终态不可再迁移。影响行数为 0 表示
// 已被取消、支付回调或超时任务推进过，属于并发结果而非故障——调用方按稳定
// 结果返回（ADR-0006/ADR-0003 的同一原则）。
func (reservationRepo) Transition(ctx context.Context, tx db.Tx, reservationID string, target domain.ReservationStatus) error {
	if tx == nil {
		return errors.New("inventory: tx 不能为空")
	}
	// 允许的迁移只有 RESERVED → CONFIRMED / RELEASED；回到 RESERVED 没有业务含义。
	if target == domain.ReservationReserved {
		return errors.New("inventory: 目标状态不能是 RESERVED")
	}
	if !target.Valid() {
		return domain.ErrInvalidReservationState
	}

	return updateOne(ctx, tx,
		`UPDATE reservations SET status = ?, updated_at = UTC_TIMESTAMP(6)
		  WHERE reservation_id = ? AND status = ?`,
		[]any{string(target), reservationID, string(domain.ReservationReserved)},
		fmt.Errorf("%w: reservation_id=%s", app.ErrReservationStateConflict, reservationID),
	)
}
