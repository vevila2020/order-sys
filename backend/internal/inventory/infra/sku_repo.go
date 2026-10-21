package infra

import (
	"context"
	"errors"
	"fmt"

	"github.com/vevila2020/order-sys/internal/inventory/app"
	"github.com/vevila2020/order-sys/internal/inventory/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// skuRepo 是 SKURepository 的 MySQL 实现。
type skuRepo struct{}

// NewSKURepository 构造 SKU 仓储。无状态，事务句柄按调用传入。
func NewSKURepository() app.SKURepository { return skuRepo{} }

// Create 实现 SKURepository.Create。
func (skuRepo) Create(ctx context.Context, tx db.Tx, s *domain.SKU) error {
	if tx == nil {
		return errors.New("inventory: tx 不能为空")
	}
	if s == nil {
		return errors.New("inventory: SKU 对象不能为空")
	}
	err := tx.WithContext(ctx).Exec(
		`INSERT INTO skus (sku_id, name, price_cents, status, version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`,
		s.ID(), s.Name(), s.PriceCents(), string(s.Status()), s.Version(),
	).Error
	if err != nil {
		if db.IsDuplicateKey(err) {
			return fmt.Errorf("%w: sku_id=%s", app.ErrSKUExists, s.ID())
		}
		return fmt.Errorf("写入 SKU %s: %w", s.ID(), err)
	}
	return nil
}

// Get 实现 SKURepository.Get。
func (skuRepo) Get(ctx context.Context, tx db.Tx, skuID string) (*domain.SKU, error) {
	if tx == nil {
		return nil, errors.New("inventory: tx 不能为空")
	}
	var row struct {
		SKUID      string `gorm:"column:sku_id"`
		Name       string `gorm:"column:name"`
		PriceCents int64  `gorm:"column:price_cents"`
		Status     string `gorm:"column:status"`
		Version    int64  `gorm:"column:version"`
	}
	err := tx.WithContext(ctx).
		Raw(`SELECT sku_id, name, price_cents, status, version FROM skus WHERE sku_id = ?`, skuID).
		Scan(&row).Error
	if err != nil {
		return nil, fmt.Errorf("查询 SKU %s: %w", skuID, err)
	}
	if row.SKUID == "" {
		return nil, fmt.Errorf("%w: sku_id=%s", app.ErrSKUNotFound, skuID)
	}
	return domain.RehydrateSKU(row.SKUID, row.Name, row.PriceCents, domain.SKUStatus(row.Status), row.Version)
}

// Update 实现 SKURepository.Update。
//
// 乐观锁用 version 等值匹配：0 行表示读取之后有人改过。不重试、不覆盖——
// 运营改了两次价格，第二次基于旧版本，直接合并会让第一次的意图被静默丢弃。
func (skuRepo) Update(ctx context.Context, tx db.Tx, s *domain.SKU, expectedVersion int64) error {
	if tx == nil {
		return errors.New("inventory: tx 不能为空")
	}
	if s == nil {
		return errors.New("inventory: SKU 对象不能为空")
	}

	result := tx.WithContext(ctx).Exec(
		`UPDATE skus
		    SET name = ?, price_cents = ?, status = ?, version = ?, updated_at = UTC_TIMESTAMP(6)
		  WHERE sku_id = ? AND version = ?`,
		s.Name(), s.PriceCents(), string(s.Status()), s.Version(), s.ID(), expectedVersion,
	)
	if result.Error != nil {
		return fmt.Errorf("更新 SKU %s: %w", s.ID(), result.Error)
	}
	switch result.RowsAffected {
	case 1:
		return nil
	case 0:
		return fmt.Errorf("%w: sku_id=%s expected_version=%d", app.ErrSKUVersionConflict, s.ID(), expectedVersion)
	default:
		return fmt.Errorf("inventory: 更新影响了 %d 行（期望 1），sku_id 唯一索引可能已损坏", result.RowsAffected)
	}
}

// 编译期断言。
var _ app.SKURepository = skuRepo{}
