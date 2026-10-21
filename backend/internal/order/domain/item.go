package domain

// OrderItem 是订单中的一行明细。
//
// 价格是**下单时的快照**：构造后即固定，之后修改 SKU 售价不影响已有订单。
// 这是"历史订单不受改价影响"这一业务约束的实现点（需求 §4.2）。
// OrderItem 是值类型且字段不可导出，外部无法绕过构造校验修改数量或单价。
type OrderItem struct {
	skuID     string
	quantity  int64
	unitPrice int64
	amount    int64
}

// NewOrderItem 构造明细并计算行金额。
//
// 数量必须为正：0 数量的明细是无意义的噪音，负数会反向减少总额。
// 单价可以为 0（赠品、试用）。
func NewOrderItem(skuID string, quantity, unitPriceCents int64) (OrderItem, error) {
	if skuID == "" {
		return OrderItem{}, ErrInvalidSKUID
	}
	if quantity <= 0 {
		return OrderItem{}, ErrInvalidQuantity
	}
	if unitPriceCents < 0 {
		return OrderItem{}, ErrInvalidPrice
	}

	amount, err := MulAmount(quantity, unitPriceCents)
	if err != nil {
		return OrderItem{}, err
	}

	return OrderItem{
		skuID:     skuID,
		quantity:  quantity,
		unitPrice: unitPriceCents,
		amount:    amount,
	}, nil
}

// RehydrateOrderItem 从持久化状态重建明细。
//
// 金额直接采用存储值而不是重新计算：若历史数据与当前算法不一致，重新计算会
// 掩盖差异；读取方应当看到数据库里真实记录的数字。
func RehydrateOrderItem(skuID string, quantity, unitPriceCents, amountCents int64) OrderItem {
	return OrderItem{skuID: skuID, quantity: quantity, unitPrice: unitPriceCents, amount: amountCents}
}

// SKUID 返回商品 ID。
func (i OrderItem) SKUID() string { return i.skuID }

// Quantity 返回数量。
func (i OrderItem) Quantity() int64 { return i.quantity }

// UnitPriceCents 返回下单时的单价快照。
func (i OrderItem) UnitPriceCents() int64 { return i.unitPrice }

// AmountCents 返回行金额（quantity × unit_price）。
func (i OrderItem) AmountCents() int64 { return i.amount }
