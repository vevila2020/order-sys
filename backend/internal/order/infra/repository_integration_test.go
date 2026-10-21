//go:build integration

package infra_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/vevila2020/order-sys/internal/order/app"
	"github.com/vevila2020/order-sys/internal/order/domain"
	"github.com/vevila2020/order-sys/internal/order/infra"
	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/outbox"
	"github.com/vevila2020/order-sys/internal/platform/testdb"
)

func newGorm(t *testing.T, tdb *testdb.DB) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(context.Background(), configx.MySQLConfig{
		DSN:          tdb.DSN,
		MaxOpenConns: 5,
		MaxIdleConns: 2,
	})
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })
	return gdb
}

// newOrder 构造一个含两条明细的待支付订单。
func newOrder(t *testing.T, orderID, userID string) *domain.Order {
	t.Helper()
	item1, err := domain.NewOrderItem("sku-a", 2, 1500)
	if err != nil {
		t.Fatalf("构造明细: %v", err)
	}
	item2, err := domain.NewOrderItem("sku-b", 1, 800)
	if err != nil {
		t.Fatalf("构造明细: %v", err)
	}
	o, err := domain.NewOrder(orderID, userID, []domain.OrderItem{item1, item2})
	if err != nil {
		t.Fatalf("构造订单: %v", err)
	}
	return o
}

// eventFor 构造与订单关联的 Outbox 事件。
func eventFor(orderID string) outbox.Event {
	return outbox.Event{
		EventID:       uuid.NewString(),
		EventType:     "order.created",
		AggregateType: "order",
		AggregateID:   orderID,
		OccurredAt:    time.Now().UTC(),
		Data:          json.RawMessage(`{"order_id":"` + orderID + `"}`),
	}
}

func countRows(t *testing.T, gdb *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := gdb.Raw(query, args...).Scan(&n).Error; err != nil {
		t.Fatalf("统计: %v", err)
	}
	return n
}

// 订单与明细同事务写入，读回时明细按 sku_id 有序。
func TestCreateAndGetOrder(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewOrderRepository()

	o := newOrder(t, "ord-1", "u1")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, o)
	}); err != nil {
		t.Fatalf("写入订单: %v", err)
	}

	var got *domain.Order
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		got, err = repo.GetByID(ctx, tx, "ord-1")
		return err
	}); err != nil {
		t.Fatalf("读取订单: %v", err)
	}

	if got.UserID() != "u1" || got.Status() != domain.StatusPendingPayment {
		t.Fatalf("订单头 = %s/%s", got.UserID(), got.Status())
	}
	if got.TotalCents() != 3800 {
		t.Fatalf("总额 = %d, want 3800", got.TotalCents())
	}
	items := got.Items()
	if len(items) != 2 {
		t.Fatalf("明细 %d 条, want 2", len(items))
	}
	// 明细按 sku_id 排序：调用方依赖这个顺序做固定加锁。
	if items[0].SKUID() != "sku-a" || items[1].SKUID() != "sku-b" {
		t.Fatalf("明细未按 sku_id 排序: %s, %s", items[0].SKUID(), items[1].SKUID())
	}
}

// 明细写入失败时订单头必须一并回滚：不能留下没有（或只有部分）明细的订单。
//
// 构造方式：第二条明细的 sku_id 超过 CHAR(36)，MySQL 严格模式会拒绝写入。
// 这发生在订单头已插入之后，正好验证"头也要回滚"。
func TestCreateRollsBackHeaderWhenItemFails(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewOrderRepository()

	okItem, err := domain.NewOrderItem("sku-ok", 1, 100)
	if err != nil {
		t.Fatalf("构造明细: %v", err)
	}
	// 40 个字符 > CHAR(36)：Strict Mode 下写入报 1406，而非静默截断。
	tooLong, err := domain.NewOrderItem(strings.Repeat("x", 40), 1, 100)
	if err != nil {
		t.Fatalf("构造明细: %v", err)
	}
	o, err := domain.NewOrder("ord-item-fail", "u1", []domain.OrderItem{okItem, tooLong})
	if err != nil {
		t.Fatalf("构造订单: %v", err)
	}

	err = tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, o)
	})
	if err == nil {
		t.Fatal("超长 sku_id 应导致写入失败")
	}

	// 订单头与第一条明细都必须消失：事务整体回滚。
	if n := countRows(t, gdb, `SELECT COUNT(*) FROM orders WHERE order_id = 'ord-item-fail'`); n != 0 {
		t.Fatalf("订单头残留 %d 行：出现了没有完整明细的订单", n)
	}
	if n := countRows(t, gdb, `SELECT COUNT(*) FROM order_items WHERE order_id = 'ord-item-fail'`); n != 0 {
		t.Fatalf("明细残留 %d 行", n)
	}
}

// Outbox 与订单同事务：回滚后不得留下事件。
func TestOutboxRollsBackWithOrder(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewOrderRepository()
	writer := outbox.NewWriter()

	o := newOrder(t, "ord-ob-1", "u1")
	ev := eventFor("ord-ob-1")
	sentinel := errors.New("后续步骤失败")

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if err := repo.Create(ctx, tx, o); err != nil {
			return err
		}
		if err := writer.Write(ctx, tx, ev); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}

	if n := countRows(t, gdb, `SELECT COUNT(*) FROM orders WHERE order_id = 'ord-ob-1'`); n != 0 {
		t.Fatalf("订单未回滚: %d 行", n)
	}
	if n := countRows(t, gdb, `SELECT COUNT(*) FROM outbox_events WHERE event_id = ?`, ev.EventID); n != 0 {
		t.Fatalf("应无事件残留，实际 %d 行（幽灵事件）", n)
	}
}

// 提交后订单与事件同时可见，且事件是 PENDING、payload 带 schema_version。
func TestOutboxCommitsWithOrder(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewOrderRepository()
	writer := outbox.NewWriter()

	o := newOrder(t, "ord-ob-2", "u1")
	ev := eventFor("ord-ob-2")
	ev.TraceID = "trace-abc"

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if err := repo.Create(ctx, tx, o); err != nil {
			return err
		}
		return writer.Write(ctx, tx, ev)
	}); err != nil {
		t.Fatalf("事务失败: %v", err)
	}

	var row struct {
		EventType string `gorm:"column:event_type"`
		Status    string `gorm:"column:status"`
		Attempts  int64  `gorm:"column:attempts"`
		Payload   []byte `gorm:"column:payload"`
		Aggregate string `gorm:"column:aggregate_id"`
	}
	if err := gdb.Raw(`SELECT event_type, status, attempts, payload, aggregate_id
	                     FROM outbox_events WHERE event_id = ?`, ev.EventID).Scan(&row).Error; err != nil {
		t.Fatalf("读取事件: %v", err)
	}
	if row.EventType != "order.created" || row.Status != "PENDING" || row.Attempts != 0 {
		t.Fatalf("事件 = %s/%s/%d", row.EventType, row.Status, row.Attempts)
	}
	if row.Aggregate != "ord-ob-2" {
		t.Fatalf("aggregate_id = %s", row.Aggregate)
	}

	// MySQL 的 JSON 列会规范化空白，按语义比较。
	var payload map[string]any
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("payload 不是合法 JSON: %v", err)
	}
	if payload["schema_version"] != float64(outbox.SchemaVersion) {
		t.Fatalf("schema_version = %v", payload["schema_version"])
	}
	if payload["trace_id"] != "trace-abc" {
		t.Fatalf("trace_id = %v", payload["trace_id"])
	}
}

// 同一 event_id 重复写入必须被唯一索引拦下：重复事件会让消费方多执行一次。
func TestOutboxRejectsDuplicateEventID(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	writer := outbox.NewWriter()

	ev := eventFor("ord-dup-1")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return writer.Write(ctx, tx, ev)
	}); err != nil {
		t.Fatalf("首次写入: %v", err)
	}

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return writer.Write(ctx, tx, ev)
	})
	if !errors.Is(err, outbox.ErrDuplicateEvent) {
		t.Fatalf("err = %v, want ErrDuplicateEvent", err)
	}
}

// 状态迁移是条件更新：前提不成立时返回冲突，而不是静默成功。
func TestTransitionConditional(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewOrderRepository()

	o := newOrder(t, "ord-tr-1", "u1")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, o)
	}); err != nil {
		t.Fatalf("写入订单: %v", err)
	}

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Transition(ctx, tx, "ord-tr-1", domain.StatusPendingPayment, domain.StatusCompleted, "")
	}); err != nil {
		t.Fatalf("迁移到 COMPLETED: %v", err)
	}

	// 再用旧的 from 迁移：必须 0 行，而不是覆盖掉 COMPLETED。
	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Transition(ctx, tx, "ord-tr-1", domain.StatusPendingPayment, domain.StatusCancelled, "user cancel")
	})
	if !errors.Is(err, app.ErrOrderStateConflict) {
		t.Fatalf("err = %v, want ErrOrderStateConflict", err)
	}

	var got *domain.Order
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		got, err = repo.GetByID(ctx, tx, "ord-tr-1")
		return err
	}); err != nil {
		t.Fatalf("读取: %v", err)
	}
	if got.Status() != domain.StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED（条件更新覆盖了终态）", got.Status())
	}
	if got.Version() != 1 {
		t.Fatalf("version = %d, want 1", got.Version())
	}
}

// 行锁读取：GetByIDForUpdate 在事务内可重复读且能阻止并发迁移。
func TestGetByIDForUpdate(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewOrderRepository()

	o := newOrder(t, "ord-lock-1", "u1")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, o)
	}); err != nil {
		t.Fatalf("写入订单: %v", err)
	}

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		got, err := repo.GetByIDForUpdate(ctx, tx, "ord-lock-1")
		if err != nil {
			return err
		}
		if got.Status() != domain.StatusPendingPayment {
			t.Fatalf("status = %s", got.Status())
		}
		return repo.Transition(ctx, tx, "ord-lock-1", got.Status(), domain.StatusCancelled, "test")
	}); err != nil {
		t.Fatalf("读后写: %v", err)
	}
}

// 游标分页：按 created_at 倒序，翻页不重不漏。
func TestListByUserCursorPagination(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewOrderRepository()

	// 插入 5 条订单，created_at 显式错开，避免同一时刻的并列让断言依赖 order_id。
	for i := 0; i < 5; i++ {
		id := "ord-list-" + string(rune('a'+i))
		o := newOrder(t, id, "u-list")
		if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
			return repo.Create(ctx, tx, o)
		}); err != nil {
			t.Fatalf("写入 %s: %v", id, err)
		}
		// 每次写入后推进一格时间，保证顺序确定。
		if err := gdb.Exec(`UPDATE orders SET created_at = DATE_ADD('2026-10-21 00:00:00', INTERVAL ? SECOND) WHERE order_id = ?`,
			i, id).Error; err != nil {
			t.Fatalf("调整时间: %v", err)
		}
	}

	var firstPage []*domain.Order
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		firstPage, err = repo.ListByUser(ctx, tx, "u-list", 2, app.Cursor{})
		return err
	}); err != nil {
		t.Fatalf("第一页: %v", err)
	}
	if len(firstPage) != 2 {
		t.Fatalf("第一页 %d 条, want 2", len(firstPage))
	}
	// 倒序：最新的是 i=4（'e'）。
	if firstPage[0].ID() != "ord-list-e" || firstPage[1].ID() != "ord-list-d" {
		t.Fatalf("第一页顺序 = %s, %s", firstPage[0].ID(), firstPage[1].ID())
	}

	// 游标必须是第一页的**最后一条**（ord-list-d，i=3），而不是第一条：
	// 用第一条做游标会把同页剩余的行跳过，这正是游标分页最容易写错的地方。
	cursor := app.Cursor{
		CreatedAt: time.Date(2026, 10, 21, 0, 0, 3, 0, time.UTC),
		OrderID:   "ord-list-d",
	}
	var secondPage []*domain.Order
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		secondPage, err = repo.ListByUser(ctx, tx, "u-list", 2, cursor)
		return err
	}); err != nil {
		t.Fatalf("第二页: %v", err)
	}
	if len(secondPage) != 2 {
		t.Fatalf("第二页 %d 条, want 2", len(secondPage))
	}
	if secondPage[0].ID() != "ord-list-c" || secondPage[1].ID() != "ord-list-b" {
		t.Fatalf("第二页顺序 = %s, %s", secondPage[0].ID(), secondPage[1].ID())
	}
}

// 不存在的订单返回 ErrOrderNotFound，而不是零值订单。
func TestGetMissingOrder(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewOrderRepository()

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		_, err := repo.GetByID(ctx, tx, "ord-missing")
		return err
	})
	if !errors.Is(err, app.ErrOrderNotFound) {
		t.Fatalf("err = %v, want ErrOrderNotFound", err)
	}
}
