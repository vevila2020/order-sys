//go:build integration

package infra_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/vevila2020/order-sys/internal/payment/app"
	"github.com/vevila2020/order-sys/internal/payment/domain"
	"github.com/vevila2020/order-sys/internal/payment/infra"
	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/db"
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

// seedOrder 写入支付单外键需要的订单头。
//
// payments 在 000001 里没有声明外键，但后续若要补上，测试数据应当已经满足
// 引用完整性，否则迁移会卡在有孤儿支付单的库上。
func seedOrder(t *testing.T, gdb *gorm.DB, orderID string) {
	t.Helper()
	if err := gdb.Exec(
		`INSERT INTO orders (order_id, user_id, status, total_cents, currency, version, created_at, updated_at)
		 VALUES (?, 'u1', 'PENDING_PAYMENT', 1000, 'CNY', 0, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, orderID).Error; err != nil {
		t.Fatalf("写入订单 %s: %v", orderID, err)
	}
}

func newPayment(t *testing.T, paymentID, orderID string) *domain.Payment {
	t.Helper()
	p, err := domain.NewPayment(paymentID, orderID, 1000)
	if err != nil {
		t.Fatalf("构造支付单: %v", err)
	}
	return p
}

// 写入后可分别按 payment_id（回调路径）与 order_id（订单查询路径）读回。
func TestCreateAndGetPayment(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewPaymentRepository()

	seedOrder(t, gdb, "ord-pay-1")
	p := newPayment(t, "pay-1", "ord-pay-1")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, p)
	}); err != nil {
		t.Fatalf("写入支付单: %v", err)
	}

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		byID, err := repo.GetByID(ctx, tx, "pay-1")
		if err != nil {
			return err
		}
		if byID.Status() != domain.StatusPending || byID.AmountCents() != 1000 {
			t.Fatalf("按 ID 读回 = %s/%d", byID.Status(), byID.AmountCents())
		}
		byOrder, err := repo.GetByOrderID(ctx, tx, "ord-pay-1")
		if err != nil {
			return err
		}
		if byOrder.ID() != "pay-1" {
			t.Fatalf("按订单读回 = %s", byOrder.ID())
		}
		return nil
	}); err != nil {
		t.Fatalf("读取: %v", err)
	}
}

// 每单最多一个支付单：第二次写入命中 uk_payments_order_id。
func TestCreateRejectsDuplicateOrder(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewPaymentRepository()

	seedOrder(t, gdb, "ord-pay-2")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, newPayment(t, "pay-2a", "ord-pay-2"))
	}); err != nil {
		t.Fatalf("首次写入: %v", err)
	}

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, newPayment(t, "pay-2b", "ord-pay-2"))
	})
	if !errors.Is(err, app.ErrPaymentExists) {
		t.Fatalf("err = %v, want ErrPaymentExists", err)
	}
}

// 状态迁移是条件更新：迁移成功后，用旧的 from 再迁一次必须 0 行。
func TestTransitionIsConditional(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewPaymentRepository()

	seedOrder(t, gdb, "ord-pay-3")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, newPayment(t, "pay-3", "ord-pay-3"))
	}); err != nil {
		t.Fatalf("写入支付单: %v", err)
	}

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Transition(ctx, tx, "pay-3", domain.StatusPending, domain.StatusSucceeded, "ref-1", "")
	}); err != nil {
		t.Fatalf("迁移到 SUCCEEDED: %v", err)
	}

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Transition(ctx, tx, "pay-3", domain.StatusPending, domain.StatusFailed, "", "late callback")
	})
	if !errors.Is(err, app.ErrPaymentStateConflict) {
		t.Fatalf("err = %v, want ErrPaymentStateConflict", err)
	}

	// 终态不可被相反结果覆盖。
	var got *domain.Payment
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		got, err = repo.GetByID(ctx, tx, "pay-3")
		return err
	}); err != nil {
		t.Fatalf("读取: %v", err)
	}
	if got.Status() != domain.StatusSucceeded {
		t.Fatalf("status = %s, want SUCCEEDED", got.Status())
	}
	if got.FinishedAt().IsZero() {
		t.Fatal("终态必须记录 finished_at")
	}
}

// 失败原因与版本都要落库：前者用于事后追溯，后者用于审计迁移次数。
func TestTransitionPersistsFailureReasonAndVersion(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewPaymentRepository()

	seedOrder(t, gdb, "ord-pay-4")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, newPayment(t, "pay-4", "ord-pay-4"))
	}); err != nil {
		t.Fatalf("写入支付单: %v", err)
	}
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Transition(ctx, tx, "pay-4", domain.StatusPending, domain.StatusFailed, "", "余额不足")
	}); err != nil {
		t.Fatalf("迁移到 FAILED: %v", err)
	}

	var got *domain.Payment
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		got, err = repo.GetByID(ctx, tx, "pay-4")
		return err
	}); err != nil {
		t.Fatalf("读取: %v", err)
	}
	if got.FailureReason() != "余额不足" {
		t.Fatalf("failure_reason = %q", got.FailureReason())
	}
	if got.Version() != 1 {
		t.Fatalf("version = %d, want 1", got.Version())
	}
}

// 不存在的支付单必须返回 ErrPaymentNotFound，回调路径据此返回 404 而不是 500。
func TestGetMissingPayment(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewPaymentRepository()

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		_, err := repo.GetByID(ctx, tx, "pay-missing")
		return err
	})
	if !errors.Is(err, app.ErrPaymentNotFound) {
		t.Fatalf("err = %v, want ErrPaymentNotFound", err)
	}
}

// 终态支付单不能被再次迁移：仓储在发 SQL 之前就该挡住，避免一次无意义的写往返。
func TestTransitionRejectsTerminalFrom(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewPaymentRepository()

	seedOrder(t, gdb, "ord-pay-5")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Create(ctx, tx, newPayment(t, "pay-5", "ord-pay-5"))
	}); err != nil {
		t.Fatalf("写入支付单: %v", err)
	}

	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return repo.Transition(ctx, tx, "pay-5", domain.StatusSucceeded, domain.StatusCancelled, "", "")
	})
	if !errors.Is(err, domain.ErrInvalidStateTransition) {
		t.Fatalf("err = %v, want ErrInvalidStateTransition", err)
	}
}

// Rehydrate 会把 finished_at 统一成 UTC：DATETIME(6) 不保存时区，
// 驱动按 DSN 的 loc 还原，若不归一化，同一时刻会在 API 上显示成不同字符串。
func TestFinishedAtIsUTCAfterRead(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	repo := infra.NewPaymentRepository()

	seedOrder(t, gdb, "ord-pay-6")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if err := repo.Create(ctx, tx, newPayment(t, "pay-6", "ord-pay-6")); err != nil {
			return err
		}
		return repo.Transition(ctx, tx, "pay-6", domain.StatusPending, domain.StatusSucceeded, "ref", "")
	}); err != nil {
		t.Fatalf("写入并迁移: %v", err)
	}

	var got *domain.Payment
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		var err error
		got, err = repo.GetByID(ctx, tx, "pay-6")
		return err
	}); err != nil {
		t.Fatalf("读取: %v", err)
	}
	if loc := got.FinishedAt().Location(); loc != time.UTC {
		t.Fatalf("finished_at 时区 = %v, want UTC", loc)
	}
	if time.Since(got.FinishedAt()) > time.Minute {
		t.Fatalf("finished_at = %v 与当前时间相差过大", got.FinishedAt())
	}
}
