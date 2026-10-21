package domain

// ReservationStatus 是预占的状态。
type ReservationStatus string

const (
	// ReservationReserved 表示已预占，等待订单终态。
	ReservationReserved ReservationStatus = "RESERVED"
	// ReservationConfirmed 表示已确认（订单完成，库存转为已售）。
	ReservationConfirmed ReservationStatus = "CONFIRMED"
	// ReservationReleased 表示已释放（订单取消/失败/超时，库存已归还）。
	ReservationReleased ReservationStatus = "RELEASED"
)

// Valid 判断状态取值是否合法。
func (s ReservationStatus) Valid() bool {
	return s == ReservationReserved || s == ReservationConfirmed || s == ReservationReleased
}

// IsTerminal 表示该状态不会再迁移。
//
// 仓储层用它区分"需要写入"与"已经处理过"：终态的预占再次收到确认/释放请求时，
// 应当在应用层直接返回稳定结果（幂等 no-op），而不是尝试写入并得到影响行数 0。
func (s ReservationStatus) IsTerminal() bool {
	return s == ReservationConfirmed || s == ReservationReleased
}

// ReservationItem 是预占中的单行明细。
type ReservationItem struct {
	SKUID    string
	Quantity int64
}

// Reservation 记录一笔订单对各 SKU 的预占。
//
// 每个订单最多一个预占（数据库有 uk_reservations_order_id 兜底）。状态迁移
// RESERVED → CONFIRMED / RELEASED 只能发生一次：确认与释放各自会改变库存，
// 重复执行等于重复扣减或重复归还。
type Reservation struct {
	id      string
	orderID string
	status  ReservationStatus
	items   []ReservationItem
}

// NewReservation 构造预占，并在此完成明细校验。
//
// 三类校验都放在构造期而不是留给数据库：
//   - 空明细：没有明细的预占释放时不知道该还什么，等于凭空锁住订单状态；
//   - 非正数量：0 数量的明细是无意义的噪音，负数会反向增加库存；
//   - 重复 SKU：确认/释放会对同一 SKU 执行两次，导致库存算错。
func NewReservation(orderID string, items []ReservationItem) (*Reservation, error) {
	if orderID == "" {
		return nil, ErrInvalidOrderID
	}
	if len(items) == 0 {
		return nil, ErrInvalidQuantity
	}

	seen := make(map[string]struct{}, len(items))
	// 复制明细：调用方通常复用同一个切片，不复制会让之后对切片的修改
	// 静默改变已有预占的内容。
	copied := make([]ReservationItem, len(items))
	for idx, it := range items {
		if it.SKUID == "" {
			return nil, ErrInvalidSKUID
		}
		if it.Quantity <= 0 {
			return nil, ErrInvalidQuantity
		}
		if _, dup := seen[it.SKUID]; dup {
			return nil, ErrDuplicateSKU
		}
		seen[it.SKUID] = struct{}{}
		copied[idx] = it
	}

	return &Reservation{
		orderID: orderID,
		status:  ReservationReserved,
		items:   copied,
	}, nil
}

// RehydrateReservation 从持久化状态重建预占。
func RehydrateReservation(reservationID, orderID string, status ReservationStatus, items []ReservationItem) (*Reservation, error) {
	if !status.Valid() {
		return nil, ErrInvalidReservationState
	}
	copied := make([]ReservationItem, len(items))
	copy(copied, items)
	return &Reservation{id: reservationID, orderID: orderID, status: status, items: copied}, nil
}

// ID 返回预占 ID。
func (r *Reservation) ID() string { return r.id }

// OrderID 返回所属订单。
func (r *Reservation) OrderID() string { return r.orderID }

// Status 返回当前状态。
func (r *Reservation) Status() ReservationStatus { return r.status }

// IsTerminal 表示预占是否已进入终态。
func (r *Reservation) IsTerminal() bool { return r.status.IsTerminal() }

// Items 返回明细的副本。
//
// 返回副本而不是内部切片：调用方直接拿到内部切片后可以原地修改数量，
// 绕过所有校验。
func (r *Reservation) Items() []ReservationItem {
	out := make([]ReservationItem, len(r.items))
	copy(out, r.items)
	return out
}

// Confirm 把预占标记为已确认。已经是终态时返回 ErrInvalidReservationState。
func (r *Reservation) Confirm() error {
	if r.status != ReservationReserved {
		return ErrInvalidReservationState
	}
	r.status = ReservationConfirmed
	return nil
}

// Release 把预占标记为已释放。已经是终态时返回 ErrInvalidReservationState。
func (r *Reservation) Release() error {
	if r.status != ReservationReserved {
		return ErrInvalidReservationState
	}
	r.status = ReservationReleased
	return nil
}
