package domain

import "fmt"

// OrderSource 表示消耗库存的订单来源，决定释放时回填到哪个桶。
//
// 为什么 M1 就要区分：普通订单释放回 available（重新可售），
// 活动订单必须回 allocated（活动额度，只能被该活动继续使用）。
// 若两者都回 available，一次活动订单取消就会把活动专属额度泄露成普通可售量。
// 现在不预留这个参数，M3 引入活动时就必须改签名并波及所有调用方。
type OrderSource string

const (
	// OrderSourceNormal 是普通订单，M1 唯一来源。
	OrderSourceNormal OrderSource = "NORMAL"
	// OrderSourceFlashSale 是秒杀活动订单（M3 启用）。
	OrderSourceFlashSale OrderSource = "FLASH_SALE"
)

// Inventory 是某个 SKU 的权威库存。
//
// 恒等式：available + allocated + reserved + sold = total，四项均非负。
//
//	available: 普通订单可售量
//	allocated: 已划拨给活动、尚未被活动订单消耗的额度
//	reserved:  已预占（普通与活动合计）
//	sold:      已确认销量
//
// 所有变更方法都在修改后调用 Validate，任何一个分支漏改一列都会立刻暴露，
// 而不是等到数据库 CHECK 报错（那时错误信息指向 SQL，排查成本高得多）。
type Inventory struct {
	skuID     string
	total     int64
	available int64
	allocated int64
	reserved  int64
	sold      int64
	version   int64
}

// NewInventory 构造初始库存：全部可售，未划拨、未预占、未售出。
func NewInventory(skuID string, total int64) (Inventory, error) {
	if total < 0 {
		return Inventory{}, ErrInvalidQuantity
	}
	inv := Inventory{
		skuID:     skuID,
		total:     total,
		available: total,
	}
	if err := inv.Validate(); err != nil {
		return Inventory{}, err
	}
	return inv, nil
}

// RehydrateInventory 从持久化状态重建库存。
//
// 不做业务校验但**校验恒等式**：如果存量数据已破坏恒等式，继续用它会放大问题，
// 应当在读取时就失败并让人去查数据，而不是拿一个自相矛盾的库存继续扣减。
func RehydrateInventory(skuID string, total, available, allocated, reserved, sold, version int64) (Inventory, error) {
	inv := Inventory{
		skuID:     skuID,
		total:     total,
		available: available,
		allocated: allocated,
		reserved:  reserved,
		sold:      sold,
		version:   version,
	}
	if err := inv.Validate(); err != nil {
		return Inventory{}, err
	}
	return inv, nil
}

// SKUID 返回所属 SKU。
func (i *Inventory) SKUID() string { return i.skuID }

// Total 返回权威总量。
func (i *Inventory) Total() int64 { return i.total }

// Available 返回普通订单可售量。
func (i *Inventory) Available() int64 { return i.available }

// Allocated 返回已划拨给活动、尚未消耗的额度。
func (i *Inventory) Allocated() int64 { return i.allocated }

// Reserved 返回当前预占量。
func (i *Inventory) Reserved() int64 { return i.reserved }

// Sold 返回已确认销量。
func (i *Inventory) Sold() int64 { return i.sold }

// Version 返回乐观锁版本。
func (i *Inventory) Version() int64 { return i.version }

// Validate 检查恒等式与各项非负。
func (i *Inventory) Validate() error {
	if i.available < 0 || i.allocated < 0 || i.reserved < 0 || i.sold < 0 || i.total < 0 {
		return fmt.Errorf("%w: 存在负数 (avail=%d alloc=%d resv=%d sold=%d total=%d)",
			ErrInvariantViolated, i.available, i.allocated, i.reserved, i.sold, i.total)
	}
	if sum := i.available + i.allocated + i.reserved + i.sold; sum != i.total {
		return fmt.Errorf("%w: %d+%d+%d+%d = %d, total = %d",
			ErrInvariantViolated, i.available, i.allocated, i.reserved, i.sold, sum, i.total)
	}
	return nil
}

// Reserve 从可售量预占，供普通订单使用。
//
// 校验顺序是"数量合法性 → 库存充足"，不能反：qty=0 应报参数错误而不是库存不足，
// 否则客户端会去等补货，而不是修正请求。
func (i *Inventory) Reserve(qty int64) error {
	if qty <= 0 {
		return ErrInvalidQuantity
	}
	if i.available < qty {
		return ErrInsufficientStock
	}
	i.available -= qty
	i.reserved += qty
	i.version++
	return i.Validate()
}

// ReserveFromAllocated 从活动额度预占（M3 使用，M1 仅测试覆盖）。
//
// 与 Reserve 分开是为了让"从哪个桶扣"成为显式选择，而不是隐含在参数里。
func (i *Inventory) ReserveFromAllocated(qty int64) error {
	if qty <= 0 {
		return ErrInvalidQuantity
	}
	if i.allocated < qty {
		return ErrInsufficientStock
	}
	i.allocated -= qty
	i.reserved += qty
	i.version++
	return i.Validate()
}

// Allocate 把可售量划拨给活动（M3 使用）。
func (i *Inventory) Allocate(qty int64) error {
	if qty <= 0 {
		return ErrInvalidQuantity
	}
	if i.available < qty {
		return ErrInsufficientStock
	}
	i.available -= qty
	i.allocated += qty
	i.version++
	return i.Validate()
}

// Confirm 把预占转为已售，供订单完成时调用。
//
// 确认的数量不能超过当前预占量：超出部分从未被预占，直接确认等于凭空扣库存。
func (i *Inventory) Confirm(qty int64) error {
	if qty <= 0 {
		return ErrInvalidQuantity
	}
	if i.reserved < qty {
		return ErrInvalidReservationState
	}
	i.reserved -= qty
	i.sold += qty
	i.version++
	return i.Validate()
}

// Release 释放预占，回到 source 决定的桶。
//
// 普通订单回 available（商品重新可售）；活动订单回 allocated（额度留给同活动）。
// 释放数量同样不能超过预占量，否则会凭空增加总量。
func (i *Inventory) Release(qty int64, source OrderSource) error {
	if qty <= 0 {
		return ErrInvalidQuantity
	}
	if i.reserved < qty {
		return ErrInvalidReservationState
	}

	i.reserved -= qty
	switch source {
	case OrderSourceFlashSale:
		i.allocated += qty
	default:
		// 未知来源按普通订单处理：宁可回到可售池，也不要把额度塞进活动桶——
		// 后者会让额度永久锁死在一个可能已结束的活动里。
		i.available += qty
	}
	i.version++
	return i.Validate()
}
