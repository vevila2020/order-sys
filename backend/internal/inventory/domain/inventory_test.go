package domain_test

import (
	"errors"
	"testing"

	"github.com/vevila2020/order-sys/internal/inventory/domain"
)

// newInventory 构造一个库存全在 available 的初始状态，便于各用例从同一基线出发。
func newInventory(t *testing.T, total int64) domain.Inventory {
	t.Helper()

	inv, err := domain.NewInventory("sku-1", total)
	if err != nil {
		t.Fatalf("构造库存失败: %v", err)
	}
	return inv
}

func TestNewInventory(t *testing.T) {
	cases := []struct {
		name    string
		total   int64
		wantErr bool
	}{
		{"正常", 100, false},
		{"零库存合法（售罄商品仍可存在）", 0, false},
		{"负数拒绝", -1, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := domain.NewInventory("sku-1", c.total)
			if c.wantErr && err == nil {
				t.Fatal("期望错误")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("意外错误: %v", err)
			}
		})
	}
}

// TestInvariantHoldsAcrossOperations 验证恒等式在每次成功操作后都成立。
//
// 恒等式 available + allocated + reserved + sold = total 是整张表的守护条件，
// 数据库层面有 CHECK 兜底，但领域层必须在每次变更后自己保证——否则错误会推迟到
// 写入时才暴露，错误信息指向 SQL 而不是业务逻辑。
func TestInvariantHoldsAcrossOperations(t *testing.T) {
	inv := newInventory(t, 100)

	steps := []struct {
		name string
		op   func() error
	}{
		{"预占 10", func() error { return inv.Reserve(10) }},
		{"预占 20", func() error { return inv.Reserve(20) }},
		{"确认 10", func() error { return inv.Confirm(10) }},
		{"释放 20", func() error { return inv.Release(20, domain.OrderSourceNormal) }},
	}

	for _, s := range steps {
		if err := s.op(); err != nil {
			t.Fatalf("%s 失败: %v", s.name, err)
		}
		if err := inv.Validate(); err != nil {
			t.Fatalf("%s 后不变量被破坏: %v", s.name, err)
		}
	}

	// 100 = available(90) + allocated(0) + reserved(0) + sold(10)
	if inv.Available() != 90 {
		t.Errorf("available = %d, want 90", inv.Available())
	}
	if inv.Sold() != 10 {
		t.Errorf("sold = %d, want 10", inv.Sold())
	}
	if inv.Reserved() != 0 {
		t.Errorf("reserved = %d, want 0", inv.Reserved())
	}
}

// TestReserveRejectsWhenInsufficient 验证库存不足时返回领域错误且不改状态。
func TestReserveRejectsWhenInsufficient(t *testing.T) {
	inv := newInventory(t, 5)

	// 边界：正好等于可售量应成功。
	if err := inv.Reserve(5); err != nil {
		t.Fatalf("预占恰好等于可售量应成功: %v", err)
	}

	// 再预占 1 必须失败。
	before := snapshot(inv)
	err := inv.Reserve(1)
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("期望 ErrInsufficientStock，got %v", err)
	}
	if after := snapshot(inv); after != before {
		t.Fatalf("失败的操作改变了状态: before=%+v after=%+v", before, after)
	}
}

func TestReserveRejectsNonPositive(t *testing.T) {
	inv := newInventory(t, 10)

	for _, qty := range []int64{0, -1} {
		if err := inv.Reserve(qty); !errors.Is(err, domain.ErrInvalidQuantity) {
			t.Errorf("Reserve(%d) = %v, want ErrInvalidQuantity", qty, err)
		}
	}
	// 数量校验必须先于库存校验：否则 qty=0 会被当成"库存不足"而不是参数错误，
	// 客户端会去等补货而不是修正请求。
	if inv.Available() != 10 {
		t.Fatalf("available = %d, want 10", inv.Available())
	}
}

// TestConfirmRequiresReservation 验证确认不能超过预占量。
func TestConfirmRequiresReservation(t *testing.T) {
	inv := newInventory(t, 10)

	if err := inv.Confirm(1); !errors.Is(err, domain.ErrInvalidReservationState) {
		t.Fatalf("无预占时确认应失败，got %v", err)
	}

	if err := inv.Reserve(3); err != nil {
		t.Fatalf("预占失败: %v", err)
	}
	// 确认超过预占量：即便库存充足也不能确认，因为这部分从未被预占。
	if err := inv.Confirm(4); !errors.Is(err, domain.ErrInvalidReservationState) {
		t.Fatalf("确认超过预占量应失败，got %v", err)
	}
	if err := inv.Confirm(3); err != nil {
		t.Fatalf("确认全部预占应成功: %v", err)
	}
	if inv.Sold() != 3 || inv.Reserved() != 0 {
		t.Fatalf("sold=%d reserved=%d, want 3/0", inv.Sold(), inv.Reserved())
	}
}

// TestReleaseReturnsToCorrectBucket 验证释放回填目标由订单来源决定。
//
// M1 只有普通订单（回 available），但接口必须预留 source 参数：
// M3 的活动订单要回 allocated，否则活动额度会被普通订单吃掉。
// 现在不预留，将来就得改签名并波及所有调用方。
func TestReleaseReturnsToCorrectBucket(t *testing.T) {
	t.Run("普通订单回 available", func(t *testing.T) {
		inv := newInventory(t, 10)
		if err := inv.Reserve(4); err != nil {
			t.Fatal(err)
		}
		if err := inv.Release(4, domain.OrderSourceNormal); err != nil {
			t.Fatal(err)
		}
		if inv.Available() != 10 {
			t.Fatalf("available = %d, want 10", inv.Available())
		}
	})

	t.Run("活动订单回 allocated", func(t *testing.T) {
		inv := newInventory(t, 10)
		// 先把 5 划拨给活动，再从活动额度预占 2。
		if err := inv.Allocate(5); err != nil {
			t.Fatal(err)
		}
		if err := inv.ReserveFromAllocated(2); err != nil {
			t.Fatal(err)
		}
		if err := inv.Release(2, domain.OrderSourceFlashSale); err != nil {
			t.Fatal(err)
		}
		// 活动额度应回到 5，而不是流进 available。
		if inv.Allocated() != 5 {
			t.Fatalf("allocated = %d, want 5", inv.Allocated())
		}
		if inv.Available() != 5 {
			t.Fatalf("available = %d, want 5（不应增加）", inv.Available())
		}
	})
}

func TestReleaseRequiresReservation(t *testing.T) {
	inv := newInventory(t, 10)

	if err := inv.Release(1, domain.OrderSourceNormal); !errors.Is(err, domain.ErrInvalidReservationState) {
		t.Fatalf("无预占时释放应失败，got %v", err)
	}
	if err := inv.Reserve(2); err != nil {
		t.Fatal(err)
	}
	if err := inv.Release(3, domain.OrderSourceNormal); !errors.Is(err, domain.ErrInvalidReservationState) {
		t.Fatalf("释放超过预占量应失败，got %v", err)
	}
	if inv.Available() != 8 {
		t.Fatalf("available = %d, want 8（失败的释放不应改变状态）", inv.Available())
	}
}

func TestAllocateRequiresAvailable(t *testing.T) {
	inv := newInventory(t, 10)

	if err := inv.Allocate(4); err != nil {
		t.Fatalf("划拨失败: %v", err)
	}
	if inv.Available() != 6 || inv.Allocated() != 4 {
		t.Fatalf("available=%d allocated=%d, want 6/4", inv.Available(), inv.Allocated())
	}
	if err := inv.Allocate(7); !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("超过可售量划拨应失败，got %v", err)
	}
}

// TestVersionBumpsOnEveryMutation 验证每次成功变更都递增版本。
//
// 版本是乐观锁的判据：仓储层更新时带 version 条件，版本不变就等于丢失更新检测
// 失效。失败的变更不应递增，否则并发方会误判"已被别人改过"。
func TestVersionBumpsOnEveryMutation(t *testing.T) {
	inv := newInventory(t, 10)
	start := inv.Version()

	if err := inv.Reserve(1); err != nil {
		t.Fatal(err)
	}
	if inv.Version() != start+1 {
		t.Fatalf("预占后 version = %d, want %d", inv.Version(), start+1)
	}

	// 失败的变更不递增。
	before := inv.Version()
	if err := inv.Reserve(100); err == nil {
		t.Fatal("期望库存不足")
	}
	if inv.Version() != before {
		t.Fatalf("失败的操作递增了版本: %d -> %d", before, inv.Version())
	}
}

// snapshot 用于断言"失败操作不改变状态"。
func snapshot(inv domain.Inventory) domain.Inventory {
	return inv
}
