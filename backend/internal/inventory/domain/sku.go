package domain

import "strings"

// SKUStatus 是 SKU 的启用状态。
type SKUStatus string

const (
	// SKUActive 表示可售。
	SKUActive SKUStatus = "ACTIVE"
	// SKUInactive 表示已停用，不可下单但历史订单不受影响。
	SKUInactive SKUStatus = "INACTIVE"
)

// Valid 判断状态取值是否合法，用于反序列化时拒绝脏数据。
func (s SKUStatus) Valid() bool {
	return s == SKUActive || s == SKUInactive
}

// SKU 是商品定义。价格用 int64 表示最小货币单位，禁止浮点。
//
// 价格只是"当前售价"：历史订单保存自己的单价快照，改价不影响已建订单。
// 这是领域层的语义约定，由 OrderItem 在构造时复制价格实现。
type SKU struct {
	id      string
	name    string
	price   int64
	status  SKUStatus
	version int64
}

// NewSKU 构造 SKU。零价格合法（赠品、测试数据）；负价格没有意义。
func NewSKU(skuID, name string, priceCents int64) (*SKU, error) {
	if strings.TrimSpace(skuID) == "" {
		return nil, ErrInvalidSKUID
	}
	if priceCents < 0 {
		return nil, ErrInvalidPrice
	}
	return &SKU{
		id:      skuID,
		name:    name,
		price:   priceCents,
		status:  SKUActive,
		version: 0,
	}, nil
}

// RehydrateSKU 从持久化状态重建 SKU，不做业务校验。
//
// 供 infra 层把数据库行还原为领域对象使用。它与 NewSKU 的区别是：数据已经过
// 历史校验，重新校验会把存量脏数据变成"读取即失败"，掩盖真正的问题来源。
// 状态仍会检查合法值，因为未知状态会让后续状态判断全部失效。
func RehydrateSKU(skuID, name string, priceCents int64, status SKUStatus, version int64) (*SKU, error) {
	if !status.Valid() {
		return nil, ErrInvalidSKUID
	}
	return &SKU{id: skuID, name: name, price: priceCents, status: status, version: version}, nil
}

// ID 返回业务 ID。
func (s *SKU) ID() string { return s.id }

// Name 返回商品名。
func (s *SKU) Name() string { return s.name }

// PriceCents 返回当前售价（最小货币单位）。
func (s *SKU) PriceCents() int64 { return s.price }

// Status 返回当前状态。
func (s *SKU) Status() SKUStatus { return s.status }

// Version 返回乐观锁版本。
func (s *SKU) Version() int64 { return s.version }

// UpdatePrice 修改售价并递增版本。
//
// 版本递增是丢失更新检测的判据：仓储层更新时带 version 条件，影响行数为 0
// 表示已被他人修改，app 层据此返回 409。
func (s *SKU) UpdatePrice(priceCents int64) error {
	if priceCents < 0 {
		return ErrInvalidPrice
	}
	s.price = priceCents
	s.version++
	return nil
}

// Rename 修改商品名并递增版本。空名称不合法。
func (s *SKU) Rename(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrInvalidSKUID
	}
	s.name = name
	s.version++
	return nil
}

// Deactivate 停用 SKU。重复调用是幂等的 no-op，不递增版本也不报错。
//
// 幂等的原因：运营在后台重复点击、或客户端重试都不该收到 409；
// 停用已经停用的商品，业务结果与第一次相同。
func (s *SKU) Deactivate() error {
	if s.status == SKUInactive {
		return nil
	}
	s.status = SKUInactive
	s.version++
	return nil
}

// Activate 启用 SKU。重复调用同样是 no-op。
func (s *SKU) Activate() error {
	if s.status == SKUActive {
		return nil
	}
	s.status = SKUActive
	s.version++
	return nil
}

// IsAvailable 表示该 SKU 是否允许下单。
func (s *SKU) IsAvailable() bool { return s.status == SKUActive }
