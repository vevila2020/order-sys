//go:build integration

package db_test

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/testdb"
)

// newGorm 用 harness 提供的 DSN 打开 GORM 连接。
func newGorm(t *testing.T, tdb *testdb.DB) *gorm.DB {
	t.Helper()

	gdb, err := db.Open(context.Background(), mysqlConfig(tdb))
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })
	return gdb
}

// TestDoCommitsAndRollsBack 验证事务边界的两个基本方向。
func TestDoCommitsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)

	// 提交：写入后能在事务外看到。
	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO skus (sku_id, name, price_cents, status, created_at, updated_at)
		                VALUES ('sku-commit', 'commit', 100, 'ACTIVE', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`).Error
	})
	if err != nil {
		t.Fatalf("Do 提交失败: %v", err)
	}
	if n := countSkus(t, gdb, "sku-commit"); n != 1 {
		t.Fatalf("提交后行数 = %d, want 1", n)
	}

	// 回滚：fn 返回错误后，事务内写入必须不可见。
	sentinel := errors.New("business failed")
	err = tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO skus (sku_id, name, price_cents, status, created_at, updated_at)
		                   VALUES ('sku-rollback', 'rollback', 100, 'ACTIVE', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`).Error; err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Do 应原样返回 fn 的错误，got %v", err)
	}
	if n := countSkus(t, gdb, "sku-rollback"); n != 0 {
		t.Fatalf("回滚后行数 = %d, want 0", n)
	}
}

// TestDoPanicsAfterRollback 验证 panic 会先回滚再向上传播。
//
// 如果这里把 panic 吞掉，调用方会收到 nil 并以为事务成功——这是最危险的
// 错误形态：数据没写进去，但接口返回 200。
func TestDoPanicsAfterRollback(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("panic 被吞掉了，调用方会误以为事务成功")
		}
		if r != "boom" {
			t.Fatalf("panic 值 = %v, want boom", r)
		}
		// 回滚必须已完成：defer 执行时事务应已结束。
		if n := countSkus(t, gdb, "sku-panic"); n != 0 {
			t.Fatalf("panic 回滚后行数 = %d, want 0", n)
		}
	}()

	_ = tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO skus (sku_id, name, price_cents, status, created_at, updated_at)
		                   VALUES ('sku-panic', 'panic', 100, 'ACTIVE', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`).Error; err != nil {
			return err
		}
		panic("boom")
	})
}

// TestDoDoesNotRetryBusinessErrors 验证非争用错误只执行一次。
//
// 重试应当只用于死锁与锁等待超时。若把唯一约束冲突也重试，幂等路径会多跑
// 两遍业务逻辑，可能产生难以复现的副作用。
func TestDoDoesNotRetryBusinessErrors(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)

	if err := gdb.Exec(`INSERT INTO skus (sku_id, name, price_cents, status, created_at, updated_at)
	                    VALUES ('sku-dup', 'dup', 100, 'ACTIVE', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`).Error; err != nil {
		t.Fatalf("预置数据: %v", err)
	}

	var calls atomic.Int32
	err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
		calls.Add(1)
		// 1062 唯一约束冲突。
		return tx.Exec(`INSERT INTO skus (sku_id, name, price_cents, status, created_at, updated_at)
		                VALUES ('sku-dup', 'dup2', 100, 'ACTIVE', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`).Error
	})
	if err == nil {
		t.Fatal("期望唯一约束冲突")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("唯一约束冲突执行了 %d 次, want 1（不该重试）", got)
	}
	if db.IsRetryable(err) {
		t.Fatalf("1062 不应被判定为可重试: %v", err)
	}
}

// TestDoRetriesRealDeadlock 用两个事务制造真实死锁，验证自动重试恢复。
//
// 构造方式：两个连接按相反顺序更新同一批行。这比"注入假错误"更可信——
// 它验证的是 IsRetryable 能识别 MySQL 真实返回的错误，以及重试确实重新执行了
// 整个事务。ADR-0003 第 5 节的验证方式就要求这一条。
func TestDoRetriesRealDeadlock(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)
	tm := db.NewTransactionManager(gdb)

	// 两张独立的表充当两把锁，避免依赖 skus 的约束。
	for _, ddl := range []string{
		`CREATE TABLE lock_a (id INT PRIMARY KEY, v INT NOT NULL)`,
		`CREATE TABLE lock_b (id INT PRIMARY KEY, v INT NOT NULL)`,
		`INSERT INTO lock_a VALUES (1, 0)`,
		`INSERT INTO lock_b VALUES (1, 0)`,
	} {
		if err := gdb.Exec(ddl).Error; err != nil {
			t.Fatalf("准备死锁场景: %v", err)
		}
	}

	// 两个事务都通过 tm.Do 执行，方向相反：A 先 a 后 b，B 先 b 后 a。
	//
	// 为什么两边都要走 tm.Do：MySQL 选择回滚哪个事务作为死锁牺牲者是不确定的
	// （通常回滚"undo 量小"的一方）。若只有 A 走 tm.Do、B 用裸 Transaction，
	// 很可能被杀的是 B，于是 A 根本没有发生重试——初版就因此误判为"死锁没形成"。
	// 两边都具备重试能力后，无论谁被杀，幸存者都会看到对方回滚并继续，测试稳定。
	//
	// 屏障设计：每个事务先拿到自己的第一把锁，然后等待"对方也拿到了第一把锁"
	// 才去申请第二把。这样环路在两边都进入第二步时必然形成，不依赖 sleep 时序。
	gotFirst := make(chan string, 2)
	bothHaveFirst := make(chan struct{})

	var firstCount atomic.Int32
	announceFirst := func(name string) {
		gotFirst <- name
		if firstCount.Add(1) == 2 {
			close(bothHaveFirst)
		}
	}

	type result struct {
		name    string
		attempt int32
		err     error
	}
	results := make(chan result, 2)

	run := func(name, firstLock, secondLock string) {
		var attempts atomic.Int32
		err := tm.Do(ctx, func(ctx context.Context, tx *gorm.DB) error {
			n := attempts.Add(1)
			if err := tx.Exec(`UPDATE ` + firstLock + ` SET v = v + 1 WHERE id = 1`).Error; err != nil {
				return err
			}
			if n == 1 {
				// 只有首轮参与制造环路；重试时必须一次性走完，
				// 否则会再次进入屏障而永远等不到第二次配对。
				announceFirst(name)
				select {
				case <-bothHaveFirst:
				case <-time.After(5 * time.Second):
					return errors.New("对方事务未就绪，死锁场景构造失败")
				}
			}
			return tx.Exec(`UPDATE ` + secondLock + ` SET v = v + 1 WHERE id = 1`).Error
		})
		results <- result{name: name, attempt: attempts.Load(), err: err}
	}

	go run("A", "lock_a", "lock_b")
	go run("B", "lock_b", "lock_a")

	var deadlockSeen bool
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			// 一方可能仍未成功（例如重试预算在极端调度下耗尽）。
			// 那属于可接受结果，但不能是"非死锁类"错误。
			if !db.IsRetryable(r.err) && !isRetryExhausted(r.err) {
				t.Fatalf("事务 %s 出现非死锁错误: %v", r.name, r.err)
			}
			continue
		}
		if r.attempt > 1 {
			deadlockSeen = true
		}
	}
	if !deadlockSeen {
		t.Fatal("两个事务都没有重试，说明没有形成真实死锁，本测试未验证到任何东西")
	}
}

// isRetryExhausted 判断错误是否为重试耗尽包装。
func isRetryExhausted(err error) bool {
	var e *db.RetryExhaustedError
	return errors.As(err, &e)
}

// TestDoRespectsContextDeadline 验证剩余预算不足时立刻放弃，不睡满退避。
func TestDoRespectsContextDeadline(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)

	var slept []time.Duration
	tm := db.NewTransactionManager(gdb,
		db.WithRetryPolicy(db.RetryPolicy{MaxRetries: 3, BaseDelay: time.Second}),
		db.WithSleep(func(context.Context, time.Duration) error {
			return errors.New("不应进入等待：预算已不足")
		}),
	)

	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	var calls atomic.Int32
	err := tm.Do(shortCtx, func(ctx context.Context, tx *gorm.DB) error {
		calls.Add(1)
		// 制造可重试错误：用一个不存在的表触发 1146 不满足条件，因此这里改用
		// 直接返回包装后的可重试错误更可控——见下方 retryableStub。
		return retryableStub{}
	})
	if err == nil {
		t.Fatal("期望失败")
	}
	if len(slept) != 0 {
		t.Fatalf("预算不足时不应调用 sleep，实际: %v", slept)
	}
	var exhausted *db.RetryExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("期望 *RetryExhaustedError，got %T: %v", err, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("预算不足时应只尝试一次，实际 %d 次", calls.Load())
	}
}

// TestDoCancelledContextStopsRetries 验证退避期间 context 取消会立即停止。
func TestDoCancelledContextStopsRetries(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	gdb := newGorm(t, tdb)

	cancelCtx, cancel := context.WithCancel(ctx)

	tm := db.NewTransactionManager(gdb,
		db.WithRetryPolicy(db.RetryPolicy{MaxRetries: 3, BaseDelay: 10 * time.Millisecond}),
		db.WithSleep(func(context.Context, time.Duration) error {
			cancel() // 退避期间用户按下 Ctrl-C 或客户端断开
			return context.Canceled
		}),
	)

	err := tm.Do(cancelCtx, func(ctx context.Context, tx *gorm.DB) error {
		return retryableStub{}
	})
	var exhausted *db.RetryExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("期望 *RetryExhaustedError，got %T: %v", err, err)
	}
	if exhausted.Attempts != 1 {
		t.Fatalf("Attempts = %d, want 1", exhausted.Attempts)
	}
}

// TestRetryExhaustedMapsTo503 验证重试耗尽的对外映射。
//
// 必须是 DEPENDENCY_UNAVAILABLE(503) 而不是 CONFLICT(409)：客户端把 409 当成
// "业务前提不成立"会直接放弃，而瞬时争用本应退避重试（ADR-0003 第 5 节）。
func TestRetryExhaustedMapsTo503(t *testing.T) {
	err := &db.RetryExhaustedError{Attempts: 4, Last: errors.New("deadlock")}
	appErr := err.AsAppError()
	if appErr.Status() != 503 {
		t.Fatalf("状态码 = %d, want 503", appErr.Status())
	}
	if appErr.RetryAfter <= 0 {
		t.Fatal("应携带 Retry-After 建议")
	}
	// 断言的是**返回给客户端**的错误体而不是 AppError.Error()：
	// Error() 是给日志用的，本来就会带内部原因（这正是排查所需）。
	// 客户端的 errorBody 只序列化 Code/Message/Details，不包含 err 字段。
	if containsAny(appErr.Message, "deadlock", "SELECT", "dsn=") {
		t.Fatalf("对外文案泄露内部信息: %s", appErr.Message)
	}
	if appErr.Details != nil {
		t.Fatalf("重试耗尽不应携带内部详情，got %v", appErr.Details)
	}
}

// TestErrorDoesNotLeakDSN 验证错误文本不含 DSN。
func TestErrorDoesNotLeakDSN(t *testing.T) {
	_, err := db.Open(context.Background(), mysqlConfigWithDSN("baduser:badpass@tcp(127.0.0.1:1)/nope?timeout=1ms"))
	if err == nil {
		t.Fatal("期望连接失败")
	}
	if containsAny(err.Error(), "badpass") {
		t.Fatalf("错误信息泄露密码: %v", err)
	}
}

// TestOpenRejectsIdleGreaterThanOpen 验证池参数校验。
func TestOpenRejectsIdleGreaterThanOpen(t *testing.T) {
	cfg := mysqlConfigWithDSN("user:pass@tcp(127.0.0.1:1)/x")
	cfg.MaxOpenConns = 5
	cfg.MaxIdleConns = 10
	if _, err := db.Open(context.Background(), cfg); err == nil {
		t.Fatal("MaxIdle > MaxOpen 应被拒绝")
	}
}

// TestOpenRejectsEmptyDSN 验证空 DSN 的明确报错。
func TestOpenRejectsEmptyDSN(t *testing.T) {
	if _, err := db.Open(context.Background(), mysqlConfigWithDSN("")); err == nil {
		t.Fatal("空 DSN 应被拒绝")
	}
}

// TestOpenAgainstConfiguredDSN 用调用方提供的真实库验证 Open 的完整路径：
// 连接、启动自检、连接池统计，以及一条真实的读查询。
//
// 未设置 MYSQL_DSN 时跳过，因此它不会出现在 CI 的默认集成测试里。
// 用途是在本机确认"隧道 + 远程库"这条链路真的能用项目自己的驱动跑通，
// 而不是只有 Navicat 能连：
//
//	$env:MYSQL_DSN = 'dev:<密码>@tcp(127.0.0.1:13306)/order_sys?parseTime=true&loc=UTC'
//	go test -tags=integration ./internal/platform/db -run TestOpenAgainstConfiguredDSN -v
//
// 该测试**只读**，不建表、不写数据、不执行迁移，因此可以安全地指向开发库。
func TestOpenAgainstConfiguredDSN(t *testing.T) {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		t.Skip("未设置 MYSQL_DSN，跳过真实库连通性检查")
	}

	ctx := context.Background()
	gdb, err := db.Open(ctx, mysqlConfigWithDSN(dsn))
	if err != nil {
		// 失败信息里只允许出现脱敏 DSN。
		t.Fatalf("连接失败（dsn=%s）: %v", testdb.EscapeDSNForLog(dsn), err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })

	// 连接池参数必须真的生效，而不是被驱动忽略。
	stats, err := db.Stats(gdb)
	if err != nil {
		t.Fatalf("读取池统计: %v", err)
	}
	if stats.MaxOpenConnections != 5 {
		t.Fatalf("MaxOpenConnections = %d, want 5", stats.MaxOpenConnections)
	}

	var version, collation string
	err = gdb.Raw("SELECT VERSION(), @@collation_database").Row().Scan(&version, &collation)
	if err != nil {
		t.Fatalf("查询版本: %v", err)
	}
	t.Logf("已连接 MySQL %s，库排序规则 %s", version, collation)
	if collation != "utf8mb4_unicode_ci" {
		t.Errorf("库排序规则 = %s, want utf8mb4_unicode_ci（与迁移和部署环境一致）", collation)
	}
}
