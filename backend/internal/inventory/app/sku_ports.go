package app

import (
	"context"

	"github.com/vevila2020/order-sys/internal/inventory/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// SKURepository 持久化商品定义。
//
// 与库存仓储分开：SKU 是参考数据（可软删除、可改价），库存是交易数据
// （只允许条件更新）。把两者塞进一个接口会让"哪些方法能在交易路径里调用"
// 变得模糊。
type SKURepository interface {
	// Create 写入 SKU。sku_id 重复命中唯一索引，返回 ErrSKUExists。
	Create(ctx context.Context, tx db.Tx, s *domain.SKU) error
	// Get 读取 SKU；不存在返回 ErrSKUNotFound。
	Get(ctx context.Context, tx db.Tx, skuID string) (*domain.SKU, error)
	// Update 以乐观锁写入 name/price/status。
	//
	// expectedVersion 是读取时的版本：影响行数为 0 表示期间已被他人修改，
	// 返回 ErrSKUVersionConflict，由 app 映射为 409。这是"丢失更新检测"，
	// 与库存那种高频条件更新不同——SKU 管理是低频操作，冲突应当让运营知道。
	Update(ctx context.Context, tx db.Tx, s *domain.SKU, expectedVersion int64) error
}
