package db

import (
	"context"
	"errors"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// 退避与重试逻辑的正确性不依赖数据库，因此用单元测试覆盖：
// - 集成测试验证"真的死锁能被重试掉"，但很难构造 3 次连续死锁；
// - 单元测试用可控的 sleep 与错误注入，验证序列本身。

// newTestManager 构造只用于测试退避逻辑的管理器。
// db 为 nil：这些用例不会真正开启事务，调用的是与 db 无关的辅助方法。
func newTestManager(policy RetryPolicy) *transactionManager {
	return &transactionManager{
		sleep:  func(context.Context, time.Duration) error { return nil },
		jitter: func(float64) float64 { return 1 },
		policy: retryPolicyOrDefault(policy),
	}
}

func TestBackoffSequence(t *testing.T) {
	m := newTestManager(RetryPolicy{MaxRetries: 3, BaseDelay: 10 * time.Millisecond})

	// 10ms / 20ms / 40ms：每次翻倍。抖动被固定为 1，序列完全确定。
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	for i, expect := range want {
		if got := m.backoff(i + 1); got != expect {
			t.Errorf("第 %d 次退避 = %v, want %v", i+1, got, expect)
		}
	}
}

func TestBackoffJitterStaysInBand(t *testing.T) {
	m := newTestManager(RetryPolicy{MaxRetries: 3, BaseDelay: 100 * time.Millisecond, Jitter: 0.2})

	// ±20% 抖动必须落在 [80ms, 120ms]；越界意味着退避可能变成 0 或远超预算。
	for i := 0; i < 200; i++ {
		got := m.backoff(1)
		if got < 80*time.Millisecond || got > 120*time.Millisecond {
			t.Fatalf("抖动后退避 %v 超出 ±20%% 区间 [80ms, 120ms]", got)
		}
	}
}

func TestDefaultRetryPolicy(t *testing.T) {
	// 零值配置必须补成默认值，否则"忘了设置"会静默变成"永不重试"。
	got := retryPolicyOrDefault(RetryPolicy{})
	want := DefaultRetryPolicy()
	if got != want {
		t.Fatalf("零值策略 = %+v, want %+v", got, want)
	}
	if want.MaxRetries != 3 {
		t.Fatalf("默认重试次数 = %d, want 3（ADR-0003）", want.MaxRetries)
	}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"死锁 1213", &mysqldriver.MySQLError{Number: 1213}, true},
		{"锁等待超时 1205", &mysqldriver.MySQLError{Number: 1205}, true},
		{"唯一约束 1062", &mysqldriver.MySQLError{Number: 1062}, false},
		{"表不存在 1146", &mysqldriver.MySQLError{Number: 1146}, false},
		{"普通错误", errors.New("boom"), false},
		{"包装后的死锁", wrappedDeadlock{}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsRetryable(c.err); got != c.want {
				t.Fatalf("IsRetryable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestRetryVeto 验证"幂等键 INSERT 上的锁等待超时"能否决重试。
//
// 这是 ADR-0006 第 4 节要求的行为：等待同一个幂等键的另一个请求时，重试整事务
// 只会再次排到队尾，应直接返回 IDEMPOTENCY_IN_PROGRESS 让客户端退避。
func TestRetryVeto(t *testing.T) {
	deadlock := &mysqldriver.MySQLError{Number: 1205}

	if !retryAllowed(deadlock) {
		t.Fatal("没有否决者时，1205 应可重试")
	}
	if retryAllowed(vetoedError{cause: deadlock}) {
		t.Fatal("实现 VetoRetry 的错误不应触发重试")
	}
	// 否决只影响重试，不改变错误本身的可识别性：调用方仍需能 errors.As 到原始错误。
	var me *mysqldriver.MySQLError
	if !errors.As(vetoedError{cause: deadlock}, &me) || me.Number != 1205 {
		t.Fatal("否决包装必须保留原始错误链")
	}
}

func TestUniformJitterZero(t *testing.T) {
	// Jitter 为 0 时不做随机化，避免除零或返回 0 导致退避消失。
	if got := uniformJitter(0); got != 1 {
		t.Fatalf("jitter=0 时系数 = %v, want 1", got)
	}
}

func TestSleepContextHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := sleepContext(ctx, 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，got %v", err)
	}
	// 必须立刻返回，而不是等满 5 秒。
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("取消后仍等待了 %v", elapsed)
	}
}

// wrappedDeadlock 用标准 %w 包装，验证 errors.As 能穿透。
type wrappedDeadlock struct{}

func (wrappedDeadlock) Error() string {
	return "repo: " + (&mysqldriver.MySQLError{Number: 1213}).Error()
}
func (wrappedDeadlock) Unwrap() error { return &mysqldriver.MySQLError{Number: 1213} }

// vetoedError 模拟 platform/idempotency 对重试的否决。
type vetoedError struct{ cause error }

func (e vetoedError) Error() string { return "idempotency in progress: " + e.cause.Error() }
func (e vetoedError) Unwrap() error { return e.cause }
func (vetoedError) VetoRetry() bool { return true }
