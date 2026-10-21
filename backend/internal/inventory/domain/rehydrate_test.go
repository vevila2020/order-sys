package domain_test

import (
	"errors"
	"testing"

	"github.com/vevila2020/order-sys/internal/inventory/domain"
)

// Rehydrate* 系列是 infra 层把数据库行还原为领域对象的入口。
//
// 它们不经过 New* 的业务校验（存量数据不该因为新增校验而读不出来），但必须
// 守住两条底线：
//  1. 未知枚举值直接拒绝，否则后续状态判断会全部失效；
//  2. 库存恢复后立即校验恒等式，损坏的数据要在读取时暴露。

func TestRehydrateSKU(t *testing.T) {
	t.Run("恢复合法状态", func(t *testing.T) {
		sku, err := domain.RehydrateSKU("sku-1", "名字", 500, domain.SKUInactive, 7)
		if err != nil {
			t.Fatalf("恢复失败: %v", err)
		}
		if sku.ID() != "sku-1" || sku.Name() != "名字" {
			t.Fatalf("字段不正确: %s / %s", sku.ID(), sku.Name())
		}
		if sku.PriceCents() != 500 || sku.Version() != 7 {
			t.Fatalf("price=%d version=%d, want 500/7", sku.PriceCents(), sku.Version())
		}
		if sku.IsAvailable() {
			t.Fatal("INACTIVE 的 SKU 不应可售")
		}
	})

	t.Run("未知状态拒绝", func(t *testing.T) {
		// 未知状态会让 IsAvailable、停用判断等全部落到错误分支，
		// 在读取时就失败比带着脏状态跑下去更好。
		if _, err := domain.RehydrateSKU("sku-1", "名字", 1, "ARCHIVED", 0); err == nil {
			t.Fatal("未知状态应被拒绝")
		}
	})

	t.Run("Rename 校验空名称", func(t *testing.T) {
		sku, err := domain.RehydrateSKU("sku-1", "名字", 1, domain.SKUActive, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := sku.Rename("  "); err == nil {
			t.Fatal("空白名称应被拒绝")
		}
		before := sku.Version()
		if err := sku.Rename("新名字"); err != nil {
			t.Fatal(err)
		}
		if sku.Name() != "新名字" || sku.Version() != before+1 {
			t.Fatalf("改名后 name=%s version=%d", sku.Name(), sku.Version())
		}
	})
}

func TestRehydrateInventory(t *testing.T) {
	t.Run("恢复一致的库存", func(t *testing.T) {
		inv, err := domain.RehydrateInventory("sku-1", 100, 60, 10, 20, 10, 3)
		if err != nil {
			t.Fatalf("恢复失败: %v", err)
		}
		if inv.SKUID() != "sku-1" || inv.Total() != 100 || inv.Version() != 3 {
			t.Fatalf("字段不正确: %+v", inv)
		}
		if inv.Available() != 60 || inv.Allocated() != 10 || inv.Reserved() != 20 || inv.Sold() != 10 {
			t.Fatalf("分桶不正确: %+v", inv)
		}
	})

	t.Run("恒等式被破坏时拒绝", func(t *testing.T) {
		// 60+10+20+10 = 100 != total 101
		_, err := domain.RehydrateInventory("sku-1", 101, 60, 10, 20, 10, 0)
		if !errors.Is(err, domain.ErrInvariantViolated) {
			t.Fatalf("got %v, want ErrInvariantViolated", err)
		}
	})

	t.Run("负数拒绝", func(t *testing.T) {
		// 负数即使恰好让等式成立（-1+1+0+0=0）也不合法。
		_, err := domain.RehydrateInventory("sku-1", 0, -1, 1, 0, 0, 0)
		if !errors.Is(err, domain.ErrInvariantViolated) {
			t.Fatalf("got %v, want ErrInvariantViolated", err)
		}
	})
}

func TestRehydrateReservation(t *testing.T) {
	t.Run("恢复合法状态", func(t *testing.T) {
		r, err := domain.RehydrateReservation("resv-1", "order-1", domain.ReservationConfirmed,
			[]domain.ReservationItem{{SKUID: "sku-1", Quantity: 2}})
		if err != nil {
			t.Fatalf("恢复失败: %v", err)
		}
		if r.ID() != "resv-1" || r.OrderID() != "order-1" {
			t.Fatalf("字段不正确: %s / %s", r.ID(), r.OrderID())
		}
		if !r.IsTerminal() {
			t.Fatal("CONFIRMED 应为终态")
		}
	})

	t.Run("未知状态拒绝", func(t *testing.T) {
		_, err := domain.RehydrateReservation("resv-1", "order-1", "EXPIRED", nil)
		if !errors.Is(err, domain.ErrInvalidReservationState) {
			t.Fatalf("got %v, want ErrInvalidReservationState", err)
		}
	})

	t.Run("明细被复制", func(t *testing.T) {
		items := []domain.ReservationItem{{SKUID: "sku-1", Quantity: 2}}
		r, err := domain.RehydrateReservation("resv-1", "order-1", domain.ReservationReserved, items)
		if err != nil {
			t.Fatal(err)
		}
		items[0].Quantity = 99
		if r.Items()[0].Quantity != 2 {
			t.Fatal("恢复时未复制明细")
		}
	})
}

// TestReservationStatusValid 覆盖状态枚举的合法值判定。
func TestReservationStatusValid(t *testing.T) {
	for _, s := range []domain.ReservationStatus{
		domain.ReservationReserved, domain.ReservationConfirmed, domain.ReservationReleased,
	} {
		if !s.Valid() {
			t.Errorf("%q 应合法", s)
		}
	}
	for _, s := range []domain.ReservationStatus{"", "reserved", "EXPIRED"} {
		if s.Valid() {
			t.Errorf("%q 不应合法", s)
		}
	}
}

// TestReleaseUnknownSourceFallsBackToAvailable 验证未知来源的回填策略。
//
// 未知来源按普通订单处理，理由是：把额度塞进 allocated 会让它永久锁死在一个
// 可能已结束的活动里，而回到 available 至少还能重新销售。宁可多卖也不锁死。
func TestReleaseUnknownSourceFallsBackToAvailable(t *testing.T) {
	inv := newInventory(t, 10)
	if err := inv.Reserve(3); err != nil {
		t.Fatal(err)
	}
	if err := inv.Release(3, domain.OrderSource("SOMETHING_NEW")); err != nil {
		t.Fatalf("未知来源应能释放: %v", err)
	}
	if inv.Available() != 10 {
		t.Fatalf("available = %d, want 10（应回可售池）", inv.Available())
	}
	if inv.Allocated() != 0 {
		t.Fatalf("allocated = %d, want 0", inv.Allocated())
	}
}

// TestReserveFromAllocatedRejectsInsufficient 补足活动额度预占的失败分支。
func TestReserveFromAllocatedRejectsInsufficient(t *testing.T) {
	inv := newInventory(t, 10)
	if err := inv.Allocate(2); err != nil {
		t.Fatal(err)
	}
	if err := inv.ReserveFromAllocated(3); !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("got %v, want ErrInsufficientStock（available 充足但 allocated 不足）", err)
	}
	if err := inv.ReserveFromAllocated(0); !errors.Is(err, domain.ErrInvalidQuantity) {
		t.Fatalf("got %v, want ErrInvalidQuantity", err)
	}
}

// TestAllocateRejectsNonPositive 补足划拨的数量校验分支。
func TestAllocateRejectsNonPositive(t *testing.T) {
	inv := newInventory(t, 10)
	for _, q := range []int64{0, -1} {
		if err := inv.Allocate(q); !errors.Is(err, domain.ErrInvalidQuantity) {
			t.Errorf("Allocate(%d) = %v, want ErrInvalidQuantity", q, err)
		}
	}
}

// TestConfirmRejectsNonPositive 补足确认的数量校验分支。
func TestConfirmRejectsNonPositive(t *testing.T) {
	inv := newInventory(t, 10)
	if err := inv.Reserve(2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []int64{0, -1} {
		if err := inv.Confirm(q); !errors.Is(err, domain.ErrInvalidQuantity) {
			t.Errorf("Confirm(%d) = %v, want ErrInvalidQuantity", q, err)
		}
	}
}

// TestReleaseRejectsNonPositive 补足释放的数量校验分支。
func TestReleaseRejectsNonPositive(t *testing.T) {
	inv := newInventory(t, 10)
	if err := inv.Reserve(2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []int64{0, -1} {
		if err := inv.Release(q, domain.OrderSourceNormal); !errors.Is(err, domain.ErrInvalidQuantity) {
			t.Errorf("Release(%d) = %v, want ErrInvalidQuantity", q, err)
		}
	}
}

// TestNewReservationRejectsEmptySKUID 补足明细中 sku_id 为空的校验。
func TestNewReservationRejectsEmptySKUID(t *testing.T) {
	_, err := domain.NewReservation("order-1", []domain.ReservationItem{{SKUID: "", Quantity: 1}})
	if !errors.Is(err, domain.ErrInvalidSKUID) {
		t.Fatalf("got %v, want ErrInvalidSKUID", err)
	}
}
