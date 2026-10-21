package domain

// OrderStatus 是普通订单的状态。
//
// 常量值与迁移文件里的 VARCHAR 取值、需求 §4.3 的状态机一一对应。
type OrderStatus string

const (
	// StatusPendingPayment 表示已创建、等待支付。
	StatusPendingPayment OrderStatus = "PENDING_PAYMENT"
	// StatusCompleted 表示支付成功、库存已确认。
	StatusCompleted OrderStatus = "COMPLETED"
	// StatusCancelled 表示用户或系统主动取消。
	StatusCancelled OrderStatus = "CANCELLED"
	// StatusPaymentFailed 表示支付失败。
	StatusPaymentFailed OrderStatus = "PAYMENT_FAILED"
	// StatusPaymentExpired 表示支付超时。
	StatusPaymentExpired OrderStatus = "PAYMENT_EXPIRED"
)

// Valid 判断状态取值是否合法，用于反序列化时拒绝脏数据。
func (s OrderStatus) Valid() bool {
	switch s {
	case StatusPendingPayment, StatusCompleted, StatusCancelled, StatusPaymentFailed, StatusPaymentExpired:
		return true
	}
	return false
}

// IsTerminal 表示该状态不会再迁移。
//
// 仓储层与用例层用它跳过无意义写入；也是"取消与支付并发只有一个终态"的判据。
func (s OrderStatus) IsTerminal() bool {
	switch s {
	case StatusCompleted, StatusCancelled, StatusPaymentFailed, StatusPaymentExpired:
		return true
	}
	return false
}

// Order 是订单聚合。
//
// 状态迁移只能通过 Complete/Cancel/FailPayment/ExpirePayment 四个方法，
// 字段不导出，避免 handler 或反序列化直接赋值绕过状态机。
//
// 幂等语义（需求 §8.4）：
//   - 重复同一终态是 no-op，返回 nil（重复取消 200、重复回调 200）；
//   - 迁到**另一个**终态返回 ErrInvalidStateTransition（409）。
//
// 两者不矛盾：前者是重放，业务结果与第一次相同；后者是真实冲突。
type Order struct {
	id          string
	userID      string
	status      OrderStatus
	items       []OrderItem
	totalCents  int64
	currency    string
	closeReason string
	version     int64
}

// currencyCNY 是一期唯一支持的币种（需求 §4.5）。
const currencyCNY = "CNY"

// NewOrder 构造待支付订单，在此完成全部输入校验并做价格快照。
//
// 重复 SKU 一律拒绝而不是合并：合并需要在领域层决定"哪一行的单价胜出"，
// 而这个决定应当由调用方（下单用例读到的是同一 SKU 的同一价格）做出。
// 拒绝让问题在构造时暴露，而不是变成两行不同单价被静默合并。
func NewOrder(orderID, userID string, items []OrderItem) (*Order, error) {
	if orderID == "" {
		return nil, ErrInvalidOrderID
	}
	if userID == "" {
		return nil, ErrInvalidUserID
	}
	if len(items) == 0 {
		return nil, ErrEmptyOrder
	}

	// 复制明细：调用方常复用同一个切片，不复制会让之后对它的修改
	// 静默改变订单总额。
	copied := make([]OrderItem, len(items))
	copy(copied, items)

	seen := make(map[string]struct{}, len(items))
	amounts := make([]int64, 0, len(items))
	for _, it := range copied {
		if _, dup := seen[it.skuID]; dup {
			return nil, ErrDuplicateSKU
		}
		seen[it.skuID] = struct{}{}
		amounts = append(amounts, it.amount)
	}

	total, err := SumAmounts(amounts)
	if err != nil {
		return nil, err
	}

	return &Order{
		id:         orderID,
		userID:     userID,
		status:     StatusPendingPayment,
		items:      copied,
		totalCents: total,
		currency:   currencyCNY,
		version:    0,
	}, nil
}

// RehydrateOrder 从持久化状态重建订单。
//
// 与 NewOrder 的区别：不校验明细是否重复、不重新计算总额。存量数据应当原样
// 呈现——若历史订单的总额与明细不符，那是需要排查的数据问题，重新计算会把它
// 掩盖掉。但状态必须是已知值，否则所有状态判断都会失效。
func RehydrateOrder(orderID, userID string, status OrderStatus, items []OrderItem,
	totalCents int64, currency, closeReason string, version int64) (*Order, error) {
	if !status.Valid() {
		return nil, ErrInvalidStateTransition
	}
	copied := make([]OrderItem, len(items))
	copy(copied, items)
	return &Order{
		id:          orderID,
		userID:      userID,
		status:      status,
		items:       copied,
		totalCents:  totalCents,
		currency:    currency,
		closeReason: closeReason,
		version:     version,
	}, nil
}

// ID 返回订单 ID。
func (o *Order) ID() string { return o.id }

// UserID 返回下单用户。
func (o *Order) UserID() string { return o.userID }

// Status 返回当前状态。
func (o *Order) Status() OrderStatus { return o.status }

// TotalCents 返回总额快照。
func (o *Order) TotalCents() int64 { return o.totalCents }

// Currency 返回币种。
func (o *Order) Currency() string { return o.currency }

// CloseReason 返回进入终态的原因；非终态为空。
func (o *Order) CloseReason() string { return o.closeReason }

// Version 返回版本，供仓储条件更新使用。
func (o *Order) Version() int64 { return o.version }

// Items 返回明细副本，调用方无法原地修改订单内容。
func (o *Order) Items() []OrderItem {
	out := make([]OrderItem, len(o.items))
	copy(out, o.items)
	return out
}

// Complete 把订单推进到 COMPLETED。重复调用是幂等 no-op。
func (o *Order) Complete() error {
	return o.transition(StatusCompleted, "")
}

// Cancel 取消订单。重复取消是幂等 no-op（需求 §8.4）。
func (o *Order) Cancel(reason string) error {
	return o.transition(StatusCancelled, reason)
}

// FailPayment 标记支付失败。
func (o *Order) FailPayment(reason string) error {
	return o.transition(StatusPaymentFailed, reason)
}

// ExpirePayment 标记支付超时。
func (o *Order) ExpirePayment() error {
	return o.transition(StatusPaymentExpired, "支付超时")
}

// transition 是所有状态迁移的唯一入口。
//
// 两种情况返回 nil：
//   - 已经在目标状态：重放，业务结果不变；
//   - 仍可迁移：正常迁移。
//
// 其余（终态迁往另一个终态）返回 ErrInvalidStateTransition。
// 集中在一处保证四条迁移方法的语义不可能出现分叉。
func (o *Order) transition(target OrderStatus, reason string) error {
	if o.status == target {
		// 幂等重放：不递增版本，否则每次重复回调都会在审计里留下一次"变更"。
		return nil
	}
	if o.status.IsTerminal() {
		return ErrInvalidStateTransition
	}

	o.status = target
	o.closeReason = reason
	o.version++
	return nil
}
