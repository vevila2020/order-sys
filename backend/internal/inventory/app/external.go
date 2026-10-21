package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/vevila2020/order-sys/internal/inventory/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// 本文件提供"在别人的事务里执行"的能力，供 order 模块的下单用例调用。
//
// 与 Service 中的方法区别在于：这里的方法接收调用方的事务句柄，自己不开事务。
// 下单用例必须让库存预占、订单写入、支付单与 Outbox 落在同一个事务里，
// 因此只能由调用方掌控事务边界（inventory/app 包文档：不自行开启事务）。

// SKUPrice 是下单所需的价格与可售量。
type SKUPrice struct {
	SKUID      string
	PriceCents int64
	// Available 是当前可售量。只用于快速失败：真正的扣减由条件更新保证，
	// 这里读到的值随时可能被并发请求改变。
	Available int64
}

// ReadForOrder 在调用方事务内读取 SKU 的价格与库存，供下单做价格快照。
//
// 已停用的 SKU 返回 ErrSKUUnavailable：停用意味着不可下单，但历史订单不受影响
// （订单明细保存的是下单时的价格快照）。
func (s *Service) ReadForOrder(ctx context.Context, tx db.Tx, skuID string) (SKUPrice, error) {
	sku, err := s.skus.Get(ctx, tx, skuID)
	if err != nil {
		return SKUPrice{}, err
	}
	if !sku.IsAvailable() {
		return SKUPrice{}, fmt.Errorf("%w: sku_id=%s status=%s", ErrSKUUnavailable, skuID, sku.Status())
	}

	price := SKUPrice{SKUID: sku.ID(), PriceCents: sku.PriceCents()}
	inv, err := s.inventories.Get(ctx, tx, skuID)
	if err != nil {
		if errors.Is(err, ErrInventoryNotFound) {
			// 有商品没有库存行：视为可售量为 0，让下单以"库存不足"失败，
			// 而不是报一个内部错误。这是数据缺失最常见的原因（上架漏建库存）。
			return price, nil
		}
		return SKUPrice{}, err
	}
	price.Available = inv.Available()
	return price, nil
}

// ReserveIn 在调用方事务内从可售量预占。
//
// 不在这里把 ErrInsufficientStock 包装成别的错误：调用方（order 的适配器）
// 需要识别它并翻译成自己模块的哨兵错误，跨模块的错误映射集中在一处。
func (s *Service) ReserveIn(ctx context.Context, tx db.Tx, skuID string, qty int64) error {
	return s.inventories.Reserve(ctx, tx, skuID, qty)
}

// CreateReservation 在调用方事务内写入预占记录。
func (s *Service) CreateReservation(ctx context.Context, tx db.Tx, reservationID, orderID string,
	lines []ReservationLine) error {
	items := make([]domain.ReservationItem, 0, len(lines))
	for _, line := range lines {
		items = append(items, domain.ReservationItem{SKUID: line.SKUID, Quantity: line.Quantity})
	}
	resv, err := domain.NewReservation(orderID, items)
	if err != nil {
		return err
	}
	// domain.NewReservation 不接受外部 ID（它负责生成"新的"预占语义），
	// 而这里的 ID 由调用方在事务外生成以保证重试稳定，因此用 Rehydrate 重建
	// 一份带该 ID 的等价对象。
	resv, err = domain.RehydrateReservation(reservationID, resv.OrderID(), resv.Status(), resv.Items())
	if err != nil {
		return err
	}
	return s.reservations.Create(ctx, tx, resv)
}

// ReservationLine 是预占明细行，与订单明细一一对应。
type ReservationLine struct {
	SKUID    string
	Quantity int64
}

// ErrSKUUnavailable 表示 SKU 已停用，不允许下单。
var ErrSKUUnavailable = fmt.Errorf("%w: SKU 已停用", domain.ErrInvalidSKUID)
