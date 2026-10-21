package domain_test

import (
	"errors"
	"testing"

	"github.com/vevila2020/order-sys/internal/inventory/domain"
)

func TestReservationStateTransitions(t *testing.T) {
	t.Run("RESERVED -> CONFIRMED", func(t *testing.T) {
		r := newReservation(t)
		if err := r.Confirm(); err != nil {
			t.Fatalf("确认失败: %v", err)
		}
		if r.Status() != domain.ReservationConfirmed {
			t.Fatalf("status = %s, want CONFIRMED", r.Status())
		}
	})

	t.Run("RESERVED -> RELEASED", func(t *testing.T) {
		r := newReservation(t)
		if err := r.Release(); err != nil {
			t.Fatalf("释放失败: %v", err)
		}
		if r.Status() != domain.ReservationReleased {
			t.Fatalf("status = %s, want RELEASED", r.Status())
		}
	})
}

// TestReservationTransitionsOnlyOnce 验证终态不可再迁移。
//
// 这是"释放最多一次"的领域层保证：即便上层因为并发或重试重复调用，
// 第二次也必须被拒绝，而不是静默成功导致库存被多还一次。
func TestReservationTransitionsOnlyOnce(t *testing.T) {
	cases := []struct {
		name  string
		first func(*domain.Reservation) error
		then  func(*domain.Reservation) error
	}{
		{"确认后再确认", (*domain.Reservation).Confirm, (*domain.Reservation).Confirm},
		{"确认后再释放", (*domain.Reservation).Confirm, (*domain.Reservation).Release},
		{"释放后再释放", (*domain.Reservation).Release, (*domain.Reservation).Release},
		{"释放后再确认", (*domain.Reservation).Release, (*domain.Reservation).Confirm},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newReservation(t)
			if err := c.first(r); err != nil {
				t.Fatalf("首次迁移失败: %v", err)
			}
			err := c.then(r)
			if !errors.Is(err, domain.ErrInvalidReservationState) {
				t.Fatalf("第二次迁移应被拒绝，got %v", err)
			}
		})
	}
}

// TestReservationIsTerminal 验证终态判定，供仓储层决定是否跳过写入。
func TestReservationIsTerminal(t *testing.T) {
	r := newReservation(t)
	if r.IsTerminal() {
		t.Fatal("RESERVED 不是终态")
	}
	if err := r.Confirm(); err != nil {
		t.Fatal(err)
	}
	if !r.IsTerminal() {
		t.Fatal("CONFIRMED 应为终态")
	}
}

func TestNewReservationValidatesItems(t *testing.T) {
	cases := []struct {
		name    string
		items   []domain.ReservationItem
		orderID string
		wantErr error
	}{
		{
			name:    "正常",
			orderID: "order-1",
			items:   []domain.ReservationItem{{SKUID: "sku-1", Quantity: 1}},
		},
		{
			name:    "空明细拒绝",
			orderID: "order-1",
			items:   nil,
			wantErr: domain.ErrInvalidQuantity,
		},
		{
			name:    "数量为 0 拒绝",
			orderID: "order-1",
			items:   []domain.ReservationItem{{SKUID: "sku-1", Quantity: 0}},
			wantErr: domain.ErrInvalidQuantity,
		},
		{
			name:    "空 order_id 拒绝",
			orderID: "",
			items:   []domain.ReservationItem{{SKUID: "sku-1", Quantity: 1}},
			wantErr: domain.ErrInvalidOrderID,
		},
		{
			name:    "重复 SKU 拒绝",
			orderID: "order-1",
			items: []domain.ReservationItem{
				{SKUID: "sku-1", Quantity: 1},
				{SKUID: "sku-1", Quantity: 2},
			},
			wantErr: domain.ErrDuplicateSKU,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := domain.NewReservation(c.orderID, c.items)
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("意外错误: %v", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

// TestReservationItemsAreCopied 验证构造时复制明细。
//
// 调用方通常会传入一个切片，之后可能复用或修改它。若不复制，订单明细被改写会
// 静默改变已有预占的内容——释放时按错误的 SKU/数量回填库存。
func TestReservationItemsAreCopied(t *testing.T) {
	items := []domain.ReservationItem{{SKUID: "sku-1", Quantity: 1}}
	r := newReservationWith(t, items)

	items[0].Quantity = 99
	if got := r.Items()[0].Quantity; got != 1 {
		t.Fatalf("构造后修改源切片影响了预占: quantity = %d, want 1", got)
	}
}

func newReservation(t *testing.T) *domain.Reservation {
	t.Helper()
	return newReservationWith(t, []domain.ReservationItem{{SKUID: "sku-1", Quantity: 1}})
}

func newReservationWith(t *testing.T, items []domain.ReservationItem) *domain.Reservation {
	t.Helper()

	r, err := domain.NewReservation("order-1", items)
	if err != nil {
		t.Fatalf("构造预占失败: %v", err)
	}
	return r
}
