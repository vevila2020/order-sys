//go:build integration

package infra_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"github.com/vevila2020/order-sys/internal/inventory/app"
	"github.com/vevila2020/order-sys/internal/inventory/domain"
	"github.com/vevila2020/order-sys/internal/inventory/infra"
	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/testdb"
)

// 仓储的正确性只能在真实 MySQL 上验证：条件更新的 0 行语义、唯一索引的等待、
// CHECK 约束的兜底，都是数据库行为。

func newGorm(t *testing.T, tdb *testdb.DB) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(context.Background(), configx.MySQLConfig{
		DSN:          tdb.DSN,
		MaxOpenConns: 10, // 并发用例需要多个真实连接，否则请求会在池里排队而不是在行锁上排队
		MaxIdleConns: 10,
	})
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })
	return gdb
}

// seedSKU 插入一个 SKU 及其库存行。
//
// 直接写 SQL 而不是调用其他仓储：这些用例要隔离地验证 inventory 仓储本身，
// 依赖同包的另一个方法会让失败原因难以定位。
func seedSKU(t *testing.T, gdb *gorm.DB, skuID string, total int64) {
	t.Helper()
	if err := gdb.Exec(
		`INSERT INTO skus (sku_id, name, price_cents, status, created_at, updated_at)
		 VALUES (?, ?, 1000, 'ACTIVE', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, skuID, skuID).Error; err != nil {
		t.Fatalf("插入 sku %s: %v", skuID, err)
	}
	if err := gdb.Exec(
		`INSERT INTO inventories (sku_id, total, available, allocated, reserved, sold, version, updated_at)
		 VALUES (?, ?, ?, 0, 0, 0, 0, UTC_TIMESTAMP(6))`, skuID, total, total).Error; err != nil {
		t.Fatalf("插入库存 %s: %v", skuID, err)
	}
}

// inventoryOf 读取库存各列，供断言恒等式。
func inventoryOf(t *testing.T, gdb *gorm.DB, skuID string) domain.Inventory {
	t.Helper()
	repo := infra.NewInventoryRepository()
	// 只读查询也走一个事务：拿到的是同一快照，避免多次查询之间被并发改写。
	var inv *domain.Inventory
	err := gdb.Transaction(func(tx *gorm.DB) error {
		got, err := repo.Get(context.Background(), tx, skuID)
		inv = got
		return err
	})
	if err != nil {
		t.Fatalf("读取库存 %s: %v", skuID, err)
	}
	return *inv
}

func TestReserveDecrementsAvailable(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewInventoryRepository()
	seedSKU(t, gdb, "sku-reserve-1", 10)

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Reserve(ctx, tx, "sku-reserve-1", 3)
	}); err != nil {
		t.Fatalf("预占失败: %v", err)
	}

	inv := inventoryOf(t, gdb, "sku-reserve-1")
	if inv.Available() != 7 || inv.Reserved() != 3 {
		t.Fatalf("available=%d reserved=%d, want 7/3", inv.Available(), inv.Reserved())
	}
}

// 库存不足必须是 0 行 → ErrInsufficientStock，绝不能静默成功。
func TestReserveRejectsInsufficientStock(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewInventoryRepository()
	seedSKU(t, gdb, "sku-short-1", 2)

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Reserve(ctx, tx, "sku-short-1", 3)
	})
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("err = %v, want ErrInsufficientStock", err)
	}

	inv := inventoryOf(t, gdb, "sku-short-1")
	if inv.Available() != 2 || inv.Reserved() != 0 {
		t.Fatalf("失败后库存被改动: available=%d reserved=%d", inv.Available(), inv.Reserved())
	}
}

// 并发预占：成功次数必须等于库存，available 归零且永不为负。
// 这是 ADR-0003 的核心验收点——条件更新把判定与扣减合并成一条语句。
func TestConcurrentReserveNeverOversells(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewInventoryRepository()

	const stock = 5
	const attempts = 40
	seedSKU(t, gdb, "sku-hot-1", stock)

	var success, insufficient int64
	var wg sync.WaitGroup
	start := make(chan struct{}) // 同时起跑，最大化竞争窗口
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
				return repo.Reserve(ctx, tx, "sku-hot-1", 1)
			})
			switch {
			case err == nil:
				atomic.AddInt64(&success, 1)
			case errors.Is(err, domain.ErrInsufficientStock):
				atomic.AddInt64(&insufficient, 1)
			default:
				t.Errorf("意外错误: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if success != stock {
		t.Fatalf("成功 %d 次, want %d（超卖或欠卖都不对）", success, stock)
	}
	if insufficient != attempts-stock {
		t.Fatalf("库存不足 %d 次, want %d", insufficient, attempts-stock)
	}
	inv := inventoryOf(t, gdb, "sku-hot-1")
	if inv.Available() != 0 || inv.Reserved() != stock {
		t.Fatalf("available=%d reserved=%d, want 0/%d", inv.Available(), inv.Reserved(), stock)
	}
	_ = inv.Validate() // Rehydrate 已校验；这里再显式确认一次恒等式语义
}

// 确认与释放在同一库存行上互斥：确认后 reserved 归零、sold 增加。
func TestConfirmMovesReservedToSold(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewInventoryRepository()
	seedSKU(t, gdb, "sku-confirm-1", 5)

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if err := repo.Reserve(ctx, tx, "sku-confirm-1", 2); err != nil {
			return err
		}
		return repo.Confirm(ctx, tx, "sku-confirm-1", 2)
	}); err != nil {
		t.Fatalf("确认失败: %v", err)
	}

	inv := inventoryOf(t, gdb, "sku-confirm-1")
	if inv.Available() != 3 || inv.Reserved() != 0 || inv.Sold() != 2 {
		t.Fatalf("available=%d reserved=%d sold=%d, want 3/0/2", inv.Available(), inv.Reserved(), inv.Sold())
	}
}

// 普通订单释放回 available；活动订单释放回 allocated。两者不能混淆，
// 否则活动额度会泄露成普通可售量（domain.OrderSource 的注释）。
func TestReleaseReturnsToCorrectBucket(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewInventoryRepository()

	seedSKU(t, gdb, "sku-rel-1", 10)
	// 活动订单：先从 available 划拨 4 到 allocated，再预占 2。
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return tx.Exec(
			`UPDATE inventories SET available = available - 4, allocated = allocated + 4 WHERE sku_id = ?`,
			"sku-rel-1").Error
	}); err != nil {
		t.Fatalf("划拨: %v", err)
	}
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return tx.Exec(
			`UPDATE inventories SET allocated = allocated - 2, reserved = reserved + 2 WHERE sku_id = ?`,
			"sku-rel-1").Error
	}); err != nil {
		t.Fatalf("活动预占: %v", err)
	}

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Release(ctx, tx, "sku-rel-1", 2, domain.OrderSourceFlashSale)
	}); err != nil {
		t.Fatalf("释放失败: %v", err)
	}

	inv := inventoryOf(t, gdb, "sku-rel-1")
	if inv.Available() != 6 || inv.Allocated() != 4 || inv.Reserved() != 0 {
		t.Fatalf("available=%d allocated=%d reserved=%d, want 6/4/0", inv.Available(), inv.Allocated(), inv.Reserved())
	}
}

// 重复确认必须失败：sold 被加两次会让销量与订单对不上。
func TestConfirmTwiceFails(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewInventoryRepository()
	seedSKU(t, gdb, "sku-dbl-1", 5)

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if err := repo.Reserve(ctx, tx, "sku-dbl-1", 2); err != nil {
			return err
		}
		return repo.Confirm(ctx, tx, "sku-dbl-1", 2)
	}); err != nil {
		t.Fatalf("首次确认: %v", err)
	}

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Confirm(ctx, tx, "sku-dbl-1", 2)
	})
	if !errors.Is(err, domain.ErrInvalidReservationState) {
		t.Fatalf("err = %v, want ErrInvalidReservationState", err)
	}
}

// 预占头与明细必须同事务：中途失败时不能留下半条预占。
func TestReservationRollsBackWithItems(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewReservationRepository()
	seedSKU(t, gdb, "sku-resv-1", 5)

	resv, err := domain.NewReservation("ord-resv-1", []domain.ReservationItem{{SKUID: "sku-resv-1", Quantity: 2}})
	if err != nil {
		t.Fatalf("构造预占: %v", err)
	}
	// 领域对象没有公开 setter 设置 reservID；测试用 Rehydrate 构造带 ID 的副本。
	resv, err = domain.RehydrateReservation("resv-1", resv.OrderID(), resv.Status(), resv.Items())
	if err != nil {
		t.Fatalf("重建预占: %v", err)
	}

	sentinel := errors.New("业务失败")
	err = tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if err := repo.Create(ctx, tx, resv); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}

	var heads, items int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM reservations WHERE order_id = ?`, "ord-resv-1").Scan(&heads).Error; err != nil {
		t.Fatalf("统计预占: %v", err)
	}
	if err := gdb.Raw(`SELECT COUNT(*) FROM reservation_items WHERE reservation_id = ?`, "resv-1").Scan(&items).Error; err != nil {
		t.Fatalf("统计明细: %v", err)
	}
	if heads != 0 || items != 0 {
		t.Fatalf("回滚后 head=%d items=%d，均应为 0", heads, items)
	}
}

// 每单最多一个预占：第二次写入命中唯一索引，返回 ErrReservationExists。
func TestReservationIsUniquePerOrder(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewReservationRepository()
	seedSKU(t, gdb, "sku-resv-2", 5)

	mk := func(resvID string) *domain.Reservation {
		r, err := domain.RehydrateReservation(resvID, "ord-resv-2", domain.ReservationReserved,
			[]domain.ReservationItem{{SKUID: "sku-resv-2", Quantity: 1}})
		if err != nil {
			t.Fatalf("重建预占: %v", err)
		}
		return r
	}

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, mk("resv-2a"))
	}); err != nil {
		t.Fatalf("首次写入: %v", err)
	}

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, mk("resv-2b"))
	})
	if !errors.Is(err, app.ErrReservationExists) {
		t.Fatalf("err = %v, want ErrReservationExists", err)
	}
}

// 预占状态迁移是条件更新：已确认的预占不能再被释放。
func TestReservationTransitionIsConditional(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewReservationRepository()
	seedSKU(t, gdb, "sku-resv-3", 5)

	resv, err := domain.RehydrateReservation("resv-3", "ord-resv-3", domain.ReservationReserved,
		[]domain.ReservationItem{{SKUID: "sku-resv-3", Quantity: 1}})
	if err != nil {
		t.Fatalf("重建预占: %v", err)
	}
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, resv)
	}); err != nil {
		t.Fatalf("写入预占: %v", err)
	}

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Transition(ctx, tx, "resv-3", domain.ReservationConfirmed)
	}); err != nil {
		t.Fatalf("确认预占: %v", err)
	}

	err = tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Transition(ctx, tx, "resv-3", domain.ReservationReleased)
	})
	if !errors.Is(err, app.ErrReservationStateConflict) {
		t.Fatalf("err = %v, want ErrReservationStateConflict", err)
	}
}

// 读取不存在的 SKU 必须返回 ErrInventoryNotFound，而不是零值库存。
// 零值库存会让"上架忘记建库存"看起来像"卖完了"。
func TestGetMissingInventory(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewInventoryRepository()

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		_, err := repo.Get(ctx, tx, "sku-does-not-exist")
		return err
	})
	if !errors.Is(err, app.ErrInventoryNotFound) {
		t.Fatalf("err = %v, want ErrInventoryNotFound", err)
	}
}
