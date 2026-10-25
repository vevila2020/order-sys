//go:build integration

package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	inventoryapp "github.com/vevila2020/order-sys/internal/inventory/app"
	inventoryinfra "github.com/vevila2020/order-sys/internal/inventory/infra"
	orderapp "github.com/vevila2020/order-sys/internal/order/app"
	orderinfra "github.com/vevila2020/order-sys/internal/order/infra"
	paymentapp "github.com/vevila2020/order-sys/internal/payment/app"
	paymentinfra "github.com/vevila2020/order-sys/internal/payment/infra"
	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/idempotency"
	"github.com/vevila2020/order-sys/internal/platform/outbox"
	"github.com/vevila2020/order-sys/internal/platform/testdb"
)

// 下单用例的验收场景（M1 计划 T13 的六类）全部在这里，跑真实 MySQL。
//
// 组装方式与 cmd/api/main.go 一致：仓储 → 服务 → 适配器。测试不复制装配逻辑，
// 而是复用同一套构造函数——否则"测试通过但 main 装配错"就无从发现。

type harness struct {
	gdb *gorm.DB
	svc *orderapp.Service
}

func newHarness(t *testing.T, tdb *testdb.DB) *harness {
	t.Helper()

	gdb, err := db.Open(context.Background(), configx.MySQLConfig{
		DSN:          tdb.DSN,
		MaxOpenConns: 10, // 并发用例需要在行锁上竞争，而不是在连接池里排队
		MaxIdleConns: 10,
	})
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })

	tm := db.NewTransactionManager(gdb)
	inventorySvc := inventoryapp.NewService(tm,
		inventoryinfra.NewSKURepository(),
		inventoryinfra.NewInventoryRepository(),
		inventoryinfra.NewReservationRepository(),
	)
	paymentSvc := paymentapp.NewService(paymentinfra.NewPaymentRepository())
	adapter := orderinfra.NewInventoryAdapter(inventorySvc)
	paymentAdapter := orderinfra.NewPaymentAdapter(paymentSvc)

	svc := orderapp.NewService(
		tm,
		orderinfra.NewOrderRepository(),
		idempotency.NewStore(time.Hour),
		outbox.NewWriter(),
		adapter, adapter, adapter,
		paymentAdapter,
		paymentAdapter,
		adapter,
	)
	return &harness{gdb: gdb, svc: svc}
}

// seedSKU 建立 SKU 与库存。
func (h *harness) seedSKU(t *testing.T, skuID string, price, stock int64) {
	t.Helper()
	if err := h.gdb.Exec(
		`INSERT INTO skus (sku_id, name, price_cents, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'ACTIVE', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, skuID, skuID, price).Error; err != nil {
		t.Fatalf("插入 SKU: %v", err)
	}
	if err := h.gdb.Exec(
		`INSERT INTO inventories (sku_id, total, available, allocated, reserved, sold, version, updated_at)
		 VALUES (?, ?, ?, 0, 0, 0, 0, UTC_TIMESTAMP(6))`, skuID, stock, stock).Error; err != nil {
		t.Fatalf("插入库存: %v", err)
	}
}

func (h *harness) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := h.gdb.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("统计: %v", err)
	}
	return n
}

// stock 返回某 SKU 的 (available, reserved, sold)。
func (h *harness) stock(t *testing.T, skuID string) (int64, int64, int64) {
	t.Helper()
	var row struct {
		Available int64 `gorm:"column:available"`
		Reserved  int64 `gorm:"column:reserved"`
		Sold      int64 `gorm:"column:sold"`
	}
	if err := h.gdb.Raw(`SELECT available, reserved, sold FROM inventories WHERE sku_id = ?`, skuID).
		Scan(&row).Error; err != nil {
		t.Fatalf("读取库存: %v", err)
	}
	return row.Available, row.Reserved, row.Sold
}

// inputFor 构造一份下单输入；hash 由调用方指定，便于模拟"同键异体"。
func inputFor(userID, key, hash string, items ...orderapp.PlaceOrderItem) orderapp.PlaceOrderInput {
	return orderapp.PlaceOrderInput{
		UserID:         userID,
		IdempotencyKey: key,
		RequestHash:    hash,
		Items:          items,
	}
}

// 成功路径：订单 PENDING_PAYMENT、预占 RESERVED、支付单 PENDING，两条 Outbox 事件。
func TestPlaceOrderSuccess(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 1500, 10)
	h.seedSKU(t, "sku-b", 800, 5)

	view, err := h.svc.PlaceOrder(ctx, inputFor("u1", "key-success1", "hash-1",
		orderapp.PlaceOrderItem{SKUID: "sku-a", Quantity: 2},
		orderapp.PlaceOrderItem{SKUID: "sku-b", Quantity: 1},
	))
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}

	if view.Status != "PENDING_PAYMENT" || view.TotalCents != 3800 || view.Replayed {
		t.Fatalf("view = %+v", view)
	}
	if view.PaymentStatus != "PENDING" || view.PaymentID == "" {
		t.Fatalf("支付单字段 = %s/%s", view.PaymentID, view.PaymentStatus)
	}

	if n := h.count(t, `SELECT COUNT(*) FROM orders WHERE order_id = ?`, view.OrderID); n != 1 {
		t.Fatalf("订单行数 = %d", n)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM reservations WHERE order_id = ? AND status = 'RESERVED'`, view.OrderID); n != 1 {
		t.Fatalf("预占行数 = %d", n)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM payments WHERE order_id = ? AND status = 'PENDING'`, view.OrderID); n != 1 {
		t.Fatalf("支付单行数 = %d", n)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = ? AND event_type = 'order.created'`, view.OrderID); n != 1 {
		t.Fatalf("order.created 事件数 = %d", n)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM idempotency_records WHERE idem_key = 'key-success1' AND status = 'COMPLETED'`); n != 1 {
		t.Fatalf("幂等记录数 = %d", n)
	}

	availA, resvA, _ := h.stock(t, "sku-a")
	if availA != 8 || resvA != 2 {
		t.Fatalf("sku-a available=%d reserved=%d, want 8/2", availA, resvA)
	}
}

// 同键同体重试：200 语义（Replayed=true），同一 order_id，库存只扣一次。
func TestPlaceOrderReplaySameKey(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 1500, 10)

	in := inputFor("u1", "key-replay01", "hash-1",
		orderapp.PlaceOrderItem{SKUID: "sku-a", Quantity: 2})

	first, err := h.svc.PlaceOrder(ctx, in)
	if err != nil {
		t.Fatalf("首次下单失败: %v", err)
	}
	second, err := h.svc.PlaceOrder(ctx, in)
	if err != nil {
		t.Fatalf("重放下单失败: %v", err)
	}

	if !second.Replayed {
		t.Fatal("第二次必须标记为 Replayed")
	}
	if second.OrderID != first.OrderID {
		t.Fatalf("重放返回了不同订单: %s vs %s", second.OrderID, first.OrderID)
	}
	if second.TotalCents != first.TotalCents {
		t.Fatalf("重放总额不同: %d vs %d", second.TotalCents, first.TotalCents)
	}

	// 关键断言：库存只扣一次，且只产生一笔订单与一条预占。
	avail, resv, _ := h.stock(t, "sku-a")
	if avail != 8 || resv != 2 {
		t.Fatalf("库存被重复扣减: available=%d reserved=%d", avail, resv)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM orders`); n != 1 {
		t.Fatalf("订单数 = %d, want 1", n)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM reservations`); n != 1 {
		t.Fatalf("预占数 = %d, want 1", n)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM outbox_events`); n != 2 {
		t.Fatalf("事件数 = %d, want 2（重放不得重复写事件）", n)
	}
}

// 同键异体：必须 409（ErrKeyReused），且不产生第二笔订单。
func TestPlaceOrderSameKeyDifferentBody(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 1500, 10)

	if _, err := h.svc.PlaceOrder(ctx, inputFor("u1", "key-conflict", "hash-1",
		orderapp.PlaceOrderItem{SKUID: "sku-a", Quantity: 1})); err != nil {
		t.Fatalf("首次下单失败: %v", err)
	}

	_, err := h.svc.PlaceOrder(ctx, inputFor("u1", "key-conflict", "hash-2",
		orderapp.PlaceOrderItem{SKUID: "sku-a", Quantity: 2}))
	if !errors.Is(err, idempotency.ErrKeyReused) {
		t.Fatalf("err = %v, want ErrKeyReused", err)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM orders`); n != 1 {
		t.Fatalf("订单数 = %d, want 1", n)
	}
}

// 并发同键：一个成功，其余得到 ErrInProgress（或在先到者完成后重放）。
func TestPlaceOrderConcurrentSameKey(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 1500, 100)

	const racers = 8
	in := inputFor("u1", "key-conc0001", "hash-1",
		orderapp.PlaceOrderItem{SKUID: "sku-a", Quantity: 1})

	var created, replayed, inProgress int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			view, err := h.svc.PlaceOrder(ctx, in)
			switch {
			case err == nil && view.Replayed:
				atomic.AddInt64(&replayed, 1)
			case err == nil:
				atomic.AddInt64(&created, 1)
			case errors.Is(err, idempotency.ErrInProgress):
				atomic.AddInt64(&inProgress, 1)
			default:
				t.Errorf("意外错误: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	// 恰好一个请求真正创建订单；其余要么重放（先到者已提交），要么被告知处理中。
	if created != 1 {
		t.Fatalf("创建 %d 次, want 1", created)
	}
	if created+replayed+inProgress != racers {
		t.Fatalf("结果合计 = %d, want %d", created+replayed+inProgress, racers)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM orders`); n != 1 {
		t.Fatalf("订单数 = %d, want 1", n)
	}
	avail, resv, _ := h.stock(t, "sku-a")
	if avail != 99 || resv != 1 {
		t.Fatalf("库存 = %d/%d, want 99/1（并发同键只扣一次）", avail, resv)
	}
}

// 库存不足：409，订单/预占/支付/Outbox 全部不落库，幂等键被释放。
func TestPlaceOrderInsufficientStock(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 1500, 1)

	_, err := h.svc.PlaceOrder(ctx, inputFor("u1", "key-short001", "hash-1",
		orderapp.PlaceOrderItem{SKUID: "sku-a", Quantity: 5}))
	if !errors.Is(err, orderapp.ErrInsufficientStock) {
		t.Fatalf("err = %v, want ErrInsufficientStock", err)
	}

	// 全链路回滚：任何一张表都不应留下痕迹。
	for _, query := range []string{
		`SELECT COUNT(*) FROM orders`,
		`SELECT COUNT(*) FROM order_items`,
		`SELECT COUNT(*) FROM reservations`,
		`SELECT COUNT(*) FROM reservation_items`,
		`SELECT COUNT(*) FROM payments`,
		`SELECT COUNT(*) FROM outbox_events`,
		`SELECT COUNT(*) FROM idempotency_records`,
	} {
		if n := h.count(t, query); n != 0 {
			t.Fatalf("%s = %d, want 0（失败请求不得留下任何行）", query, n)
		}
	}

	// 键被释放后，同一键可以正常下单。
	h.seedSKU(t, "sku-c", 100, 10)
	if _, err := h.svc.PlaceOrder(ctx, inputFor("u1", "key-short001", "hash-2",
		orderapp.PlaceOrderItem{SKUID: "sku-c", Quantity: 1})); err != nil {
		t.Fatalf("键未被释放，重试失败: %v", err)
	}
}

// 多 SKU 中一行不足：整单回滚，所有库存保持原值。
func TestPlaceOrderPartialStockFailureRollsBackAll(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	// sku-a 充足（排在前），sku-z 不足（排在后）。
	h.seedSKU(t, "sku-a", 100, 10)
	h.seedSKU(t, "sku-z", 100, 1)

	_, err := h.svc.PlaceOrder(ctx, inputFor("u1", "key-partial1", "hash-1",
		orderapp.PlaceOrderItem{SKUID: "sku-a", Quantity: 2},
		orderapp.PlaceOrderItem{SKUID: "sku-z", Quantity: 3},
	))
	if !errors.Is(err, orderapp.ErrInsufficientStock) {
		t.Fatalf("err = %v, want ErrInsufficientStock", err)
	}

	// 快速失败发生在扣减之前，因此 sku-a 也不应被扣。
	availA, resvA, _ := h.stock(t, "sku-a")
	if availA != 10 || resvA != 0 {
		t.Fatalf("sku-a 被部分扣减: available=%d reserved=%d", availA, resvA)
	}
	availZ, _, _ := h.stock(t, "sku-z")
	if availZ != 1 {
		t.Fatalf("sku-z available=%d, want 1", availZ)
	}
}

// 已停用的 SKU 不可下单：这是"下架"的业务含义，而不是库存不足。
func TestPlaceOrderRejectsInactiveSKU(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-off", 100, 10)
	if err := h.gdb.Exec(`UPDATE skus SET status = 'INACTIVE' WHERE sku_id = 'sku-off'`).Error; err != nil {
		t.Fatalf("停用 SKU: %v", err)
	}

	_, err := h.svc.PlaceOrder(ctx, inputFor("u1", "key-inactive", "hash-1",
		orderapp.PlaceOrderItem{SKUID: "sku-off", Quantity: 1}))
	if err == nil {
		t.Fatal("停用的 SKU 应拒绝下单")
	}
	if n := h.count(t, `SELECT COUNT(*) FROM orders`); n != 0 {
		t.Fatalf("订单数 = %d, want 0", n)
	}
}

// 价格快照取自事务内读取的 SKU：之后改价不影响已下单的订单。
func TestPlaceOrderSnapshotsPrice(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-p", 100, 10)

	view, err := h.svc.PlaceOrder(ctx, inputFor("u1", "key-price001", "hash-1",
		orderapp.PlaceOrderItem{SKUID: "sku-p", Quantity: 3}))
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	if view.TotalCents != 300 {
		t.Fatalf("总额 = %d, want 300", view.TotalCents)
	}

	// 改价后订单明细与总额都不变。
	if err := h.gdb.Exec(`UPDATE skus SET price_cents = 999 WHERE sku_id = 'sku-p'`).Error; err != nil {
		t.Fatalf("改价: %v", err)
	}
	var row struct {
		UnitPrice int64 `gorm:"column:unit_price_cents"`
		Amount    int64 `gorm:"column:amount_cents"`
	}
	if err := h.gdb.Raw(`SELECT unit_price_cents, amount_cents FROM order_items WHERE order_id = ?`, view.OrderID).
		Scan(&row).Error; err != nil {
		t.Fatalf("读取明细: %v", err)
	}
	if row.UnitPrice != 100 || row.Amount != 300 {
		t.Fatalf("价格快照被改价影响: unit=%d amount=%d", row.UnitPrice, row.Amount)
	}
}

// 幂等快照只保存 data 部分：重放返回的 order_id 与首次一致，
// 且快照里不含 request_id（它属于单次请求）。
func TestPlaceOrderSnapshotContents(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 100, 10)

	in := inputFor("u1", "key-snapshot", "hash-1",
		orderapp.PlaceOrderItem{SKUID: "sku-a", Quantity: 1})
	first, err := h.svc.PlaceOrder(ctx, in)
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}

	// JSON 列扫描到 string：驱动返回 []uint8，直接 Scan 到 []byte 会被
	// GORM 当成单值转换（报 "converting ... to a uint8"），不是拼字节。
	var raw string
	if err := h.gdb.Raw(`SELECT response_snapshot FROM idempotency_records WHERE idem_key = ?`, "key-snapshot").
		Scan(&raw).Error; err != nil {
		t.Fatalf("读取快照: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("快照不是合法 JSON: %v", err)
	}
	if _, ok := payload["request_id"]; ok {
		t.Fatal("快照不得包含 request_id：重放必须生成新的请求 ID")
	}
	if payload["order_id"] != first.OrderID {
		t.Fatalf("快照 order_id = %v, want %s", payload["order_id"], first.OrderID)
	}
}
