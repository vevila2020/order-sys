//go:build integration

package idempotency_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/idempotency"
	"github.com/vevila2020/order-sys/internal/platform/testdb"
)

// 这些用例必须在真实 MySQL 上跑：唯一索引上的等待语义、1062 的触发时机、
// 事务回滚后记录消失，都是数据库行为，fake 无法表达。

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

// assertSameJSON 按语义比较两段 JSON。
//
// 不能按字节比较：response_snapshot 是 MySQL 的 JSON 列，写入后会被服务端
// 规范化（键排序、冒号后补空格），往返结果与写入原文不同。这个差异不影响
// 重放语义——客户端解析 JSON 得到相同数据——因此断言必须比较语义而非字节。
func assertSameJSON(t *testing.T, got []byte, want string) {
	t.Helper()

	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("快照不是合法 JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantVal); err != nil {
		t.Fatalf("期望值不是合法 JSON: %v", err)
	}
	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Fatalf("快照 = %s, want %s", got, want)
	}
}

// recordFor 构造一条合法的记录，避免每个用例重复填写固定字段。
func recordFor(key, hash string) idempotency.Record {
	return idempotency.Record{
		Scope:       "order.create",
		UserID:      "u1",
		Key:         key,
		RequestHash: hash,
	}
}

// 首次 Begin 应占用键；Complete 之后同键同体应重放快照。
func TestBeginAndCompleteThenReplay(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	store := idempotency.NewStore(time.Hour)

	rec := recordFor("key-aaaaaaa1", "hash-1")
	snapshot := []byte(`{"order_id":"ord-1"}`)

	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		existing, err := store.Begin(ctx, tx, rec)
		if err != nil {
			return err
		}
		if existing != nil {
			t.Fatalf("首次 Begin 不应返回既有记录: %+v", existing)
		}
		return store.Complete(ctx, tx, rec, snapshot)
	}); err != nil {
		t.Fatalf("首次事务失败: %v", err)
	}

	// 重放路径是只读的：Begin 返回既有记录，调用方据此直接响应，不再执行业务写。
	var existing *idempotency.Record
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		got, err := store.Begin(ctx, tx, rec)
		existing = got
		return err
	}); err != nil {
		t.Fatalf("重放事务失败: %v", err)
	}
	if existing == nil {
		t.Fatal("同键同体第二次 Begin 应返回既有记录")
	}
	if existing.Status != idempotency.StatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", existing.Status)
	}
	assertSameJSON(t, existing.Snapshot, string(snapshot))
}

// 同键异体必须返回 ErrKeyReused，且不能读到首次的快照。
func TestBeginRejectsDifferentFingerprint(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	store := idempotency.NewStore(time.Hour)

	first := recordFor("key-aaaaaaa2", "hash-first")
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if _, err := store.Begin(ctx, tx, first); err != nil {
			return err
		}
		return store.Complete(ctx, tx, first, []byte(`{"order_id":"ord-1"}`))
	}); err != nil {
		t.Fatalf("首次事务失败: %v", err)
	}

	second := recordFor("key-aaaaaaa2", "hash-second")
	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		_, err := store.Begin(ctx, tx, second)
		return err
	})
	if !errors.Is(err, idempotency.ErrKeyReused) {
		t.Fatalf("err = %v, want ErrKeyReused", err)
	}
}

// 不同用户使用相同键互不影响：作用域是三元组，不是 key 本身。
func TestBeginIsScopedPerUser(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	store := idempotency.NewStore(time.Hour)

	recA := idempotency.Record{Scope: "order.create", UserID: "u1", Key: "key-shared1", RequestHash: "h"}
	recB := idempotency.Record{Scope: "order.create", UserID: "u2", Key: "key-shared1", RequestHash: "h"}

	for _, rec := range []idempotency.Record{recA, recB} {
		rec := rec
		if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
			existing, err := store.Begin(ctx, tx, rec)
			if err != nil {
				return err
			}
			if existing != nil {
				t.Fatalf("user=%s 不应命中其他用户的键", rec.UserID)
			}
			return store.Complete(ctx, tx, rec, []byte(`{}`))
		}); err != nil {
			t.Fatalf("user=%s 事务失败: %v", rec.UserID, err)
		}
	}
}

// 事务回滚后记录必须一并消失：这是单事务方案的核心收益（ADR-0006 第 3 节），
// 也是"不存在永远处理中的键"这一结论的实证。
func TestRollbackLeavesNoRecord(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	store := idempotency.NewStore(time.Hour)

	rec := recordFor("key-rollback1", "hash-1")
	sentinel := errors.New("库存不足")
	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if _, err := store.Begin(ctx, tx, rec); err != nil {
			return err
		}
		return sentinel // 业务失败：整体回滚，键被释放
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}

	var count int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM idempotency_records WHERE scope = ? AND user_id = ? AND idem_key = ?`,
		rec.Scope, rec.UserID, rec.Key).Scan(&count).Error; err != nil {
		t.Fatalf("统计记录: %v", err)
	}
	if count != 0 {
		t.Fatalf("回滚后仍有 %d 条记录：客户端会被永久卡在 IN_PROGRESS", count)
	}

	// 同键可以重新下单，并得到与首次无关的结果。
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if _, err := store.Begin(ctx, tx, rec); err != nil {
			return err
		}
		return store.Complete(ctx, tx, rec, []byte(`{"order_id":"ord-retry"}`))
	}); err != nil {
		t.Fatalf("回滚后同键重试失败: %v", err)
	}
}

// 并发同键：先到事务持有键时，后到者的 INSERT 在唯一索引上等待；等待窗口内
// ctx 耗尽应映射为 ErrInProgress，而不是 TIMEOUT 或重试耗尽。
func TestConcurrentSameKeyReportsInProgress(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)
	store := idempotency.NewStore(time.Hour)

	rec := recordFor("key-concurrent", "hash-1")

	// 先到者：占用键后挂起，直到测试显式放行。
	hold := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
			if _, err := store.Begin(ctx, tx, rec); err != nil {
				return err
			}
			<-hold
			return store.Complete(ctx, tx, rec, []byte(`{"order_id":"ord-first"}`))
		})
	}()

	// 给先到者一点时间真正拿到锁。没有这个间隔，后到者可能在先到者 INSERT
	// 之前就完成，测试会偶发地测不到等待路径。
	time.Sleep(200 * time.Millisecond)

	// 后到者：等待窗口内 ctx 到期。
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	err := tm.Do(waitCtx, func(ctx context.Context, tx *gorm.DB) error {
		_, err := store.Begin(ctx, tx, rec)
		return err
	})
	if !errors.Is(err, idempotency.ErrInProgress) {
		t.Fatalf("err = %v, want ErrInProgress（锁等待超时不得走整事务重试）", err)
	}

	close(hold)
	if err := <-firstDone; err != nil {
		t.Fatalf("先到事务失败: %v", err)
	}

	// 先到者提交后，后到者重试应拿到快照。
	var existing *idempotency.Record
	if err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		got, err := store.Begin(ctx, tx, rec)
		existing = got
		return err
	}); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	if existing == nil || existing.Status != idempotency.StatusCompleted {
		t.Fatalf("重试应拿到 COMPLETED 记录, got %+v", existing)
	}
	assertSameJSON(t, existing.Snapshot, `{"order_id":"ord-first"}`)
}

// ErrInProgress 必须否决事务级重试。若它被 TransactionManager 当成可重试错误，
// 后到者会反复排到同一个队尾，把预算耗光后返回 DEPENDENCY_UNAVAILABLE——
// 客户端得到的是错误结论（以为是依赖故障，而不是"稍后重试即可"）。
//
// 否决行为本身在 retry_veto_internal_test.go 里断言（需要构造未导出的错误类型）；
// 这里补一条端到端观察：等待超时后 fn 只被执行一次。
func TestInProgressDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)

	store := idempotency.NewStore(time.Hour)
	rec := recordFor("key-veto00001", "hash-1")

	tm := db.NewTransactionManager(gdb,
		db.WithRetryPolicy(db.RetryPolicy{MaxRetries: 3, BaseDelay: time.Millisecond}),
	)

	// 先占用该键并挂起，制造出一个真实的等待窗口。
	hold := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
			if _, err := store.Begin(ctx, tx, rec); err != nil {
				return err
			}
			<-hold
			return store.Complete(ctx, tx, rec, []byte(`{}`))
		})
	}()
	time.Sleep(200 * time.Millisecond)

	// 统计 fn 的进入次数：若无否决，重试会让它进入多次。
	var attempts int
	var mu sync.Mutex
	waitCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	err := tm.Do(waitCtx, func(ctx context.Context, tx *gorm.DB) error {
		mu.Lock()
		attempts++
		mu.Unlock()
		_, err := store.Begin(ctx, tx, rec)
		return err
	})
	if !errors.Is(err, idempotency.ErrInProgress) {
		t.Fatalf("err = %v, want ErrInProgress（不得返回 RetryExhaustedError）", err)
	}

	close(hold)
	if err := <-holderDone; err != nil {
		t.Fatalf("持有者事务失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Fatalf("fn 被执行 %d 次：ErrInProgress 未否决事务级重试", attempts)
	}
}
