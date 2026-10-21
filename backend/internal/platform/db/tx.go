package db

import (
	"context"
	"math/rand/v2"
	"time"

	"gorm.io/gorm"
)

// RetryPolicy 描述死锁重试的预算与退避参数。
//
// 默认值来自 ADR-0003 第 5 节。测试通过替换 sleep 与 Policy 验证重试次数与
// 退避序列，不依赖真实时钟（否则测试要么很慢，要么在 CI 上不稳定）。
type RetryPolicy struct {
	// MaxRetries 是首次尝试之外的额外尝试次数。总计最多 MaxRetries+1 次。
	MaxRetries int
	// BaseDelay 是第一次退避时长，之后每次翻倍。
	BaseDelay time.Duration
	// Jitter 是退避的抖动比例（0.2 表示 ±20%）。
	//
	// 为什么需要抖动：同一批请求在同一个死锁上同时重试，会在退避结束后再次
	// 同时撞车，形成规律性的重试风暴。随机化把它们的重试时刻打散。
	Jitter float64
}

// DefaultRetryPolicy 是生产默认值：3 次重试，10ms 起步翻倍，±20% 抖动。
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxRetries: 3, BaseDelay: 10 * time.Millisecond, Jitter: 0.2}
}

// retryPolicyOrDefault 把零值配置补成默认值，避免"忘了设置就永远不重试"。
func retryPolicyOrDefault(p RetryPolicy) RetryPolicy {
	if p.MaxRetries == 0 && p.BaseDelay == 0 {
		return DefaultRetryPolicy()
	}
	return p
}

// TransactionManager 在单个数据库事务内执行用例。
//
// 实现约定（ADR-0003）：
//   - fn 内的唯一合法句柄是传入的 tx；使用连接池上的 *gorm.DB 会跑在事务外，
//     回滚时留下脏数据。
//   - 死锁与锁等待超时会重试整个事务，因此 **fn 必须可重入**：不得在闭包外累积
//     状态，不得产生数据库之外的副作用（发消息、写文件、加内存锁）。
//   - 重试耗尽返回 *RetryExhaustedError，由 HTTP 层映射为 DEPENDENCY_UNAVAILABLE。
//
// # 已知的抽象泄漏：签名里出现 *gorm.DB
//
// 按分层规则，app 层不得 import gorm（internal/archtest 会拦下直接 import），
// 但这里的 fn 签名把 *gorm.DB 交到了 app 手里 —— 这是一次**有意的、被记录在案的**
// 泄漏，不是疏漏。取舍：
//
//   - 选它：仓储端口需要执行带条件的原生 SQL、读取 RowsAffected 并区分驱动错误码
//     （ADR-0003 第 2、5 节）。再包一层自定义 Tx（Exec/Query/RowsAffected）会把
//     gorm 的能力抄一遍，适配层与用例层各写一半，反而更容易出错。
//   - 代价：app 拿到的是完整的 ORM 对象，可以用 tx.Model(...).Where(...) 绕过仓储；
//     archtest 只检查直接 import，**拦不住这种用法**。
//
// 因此约束靠评审与代码习惯维持，边界是这样划的：**app 只允许把 tx 原样透传给
// 端口方法，不得对 tx 调用任何方法**。触发重新评估的条件（任一出现即应重新设计，
// 而不是继续加注释）：
//
//   - app 层出现 tx.Exec / tx.Raw / tx.Model / tx.Where 等任何调用；
//   - 出现第二个数据库（例如只读副本、分析库），需要按用途区分事务句柄；
//   - 引入非 gorm 的存储实现（换库或增加一个非 SQL 仓储），签名无法同时满足。
type TransactionManager interface {
	// Do 在事务内执行 fn；fn 返回错误或 panic 时整体回滚。
	Do(ctx context.Context, fn func(ctx context.Context, tx *gorm.DB) error) error
}

// transactionManager 是 TransactionManager 的 GORM 实现。
type transactionManager struct {
	db *gorm.DB
	// sleep 与 now 可替换，便于测试退避序列而不真的等待。
	sleep func(context.Context, time.Duration) error
	// jitter 返回 [1-j, 1+j] 区间的系数；测试中固定为 1 以获得确定的退避序列。
	jitter func(float64) float64
	policy RetryPolicy
}

// Option 是 NewTransactionManager 的可选配置。
type Option func(*transactionManager)

// WithRetryPolicy 覆盖默认重试策略。
func WithRetryPolicy(p RetryPolicy) Option {
	return func(m *transactionManager) { m.policy = retryPolicyOrDefault(p) }
}

// WithSleep 替换默认的退避等待（测试用）。
//
// 替换的是"等待"而不是"时钟"：判断剩余预算仍然使用真实的 ctx.Deadline，
// 因此超时行为在测试与生产中一致。
func WithSleep(fn func(context.Context, time.Duration) error) Option {
	return func(m *transactionManager) { m.sleep = fn }
}

// WithJitter 替换抖动函数（测试用）。传入恒返回 1 的函数即可得到确定的退避序列。
func WithJitter(fn func(float64) float64) Option {
	return func(m *transactionManager) { m.jitter = fn }
}

// NewTransactionManager 构造事务管理器。db 必须是通过 Open 取得的连接池。
func NewTransactionManager(db *gorm.DB, opts ...Option) TransactionManager {
	m := &transactionManager{
		db:     db,
		sleep:  sleepContext,
		jitter: uniformJitter,
		policy: DefaultRetryPolicy(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Do 在事务内执行 fn，对死锁与锁等待超时做有限次重试。
func (m *transactionManager) Do(ctx context.Context, fn func(ctx context.Context, tx *gorm.DB) error) error {
	var lastErr error
	total := m.policy.MaxRetries + 1

	for attempt := 1; attempt <= total; attempt++ {
		// 每次尝试都检查 context：上一次退避可能已经耗尽预算。
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				// 已有失败记录时，超时的责任归因于最后的真实错误，更有排查价值。
				return &RetryExhaustedError{Attempts: attempt - 1, Last: lastErr}
			}
			return err
		}

		lastErr = m.runOnce(ctx, fn)
		if lastErr == nil {
			return nil
		}
		if !retryAllowed(lastErr) {
			return lastErr
		}
		if attempt == total {
			break
		}

		delay := m.backoff(attempt)
		// 剩余预算连退避都不够时立刻放弃，不要睡完再失败：
		// 那会把调用方的超时预算白白用光，连一个明确的错误都返回不了。
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < delay {
			return &RetryExhaustedError{Attempts: attempt, Last: lastErr}
		}
		if err := m.sleep(ctx, delay); err != nil {
			return &RetryExhaustedError{Attempts: attempt, Last: lastErr}
		}
	}

	return &RetryExhaustedError{Attempts: total, Last: lastErr}
}

// runOnce 执行一次完整事务，并把 panic 转成错误向上传播（回滚由 gorm 负责）。
//
// 不在这里 recover 成 nil：panic 必须继续抛出，否则调用方会以为事务成功。
// gorm 的 Transaction 在 panic 时会先回滚再重新 panic，这里只负责记录
// 已有错误以便重试判断。
func (m *transactionManager) runOnce(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 把事务句柄与 context 一起交给 fn：tx 已绑定 ctx，但显式再传一次
		// 便于用例层继续向下传递（例如传给仓储方法）。
		return fn(ctx, tx)
	})
}

// backoff 返回第 attempt 次失败后的等待时长：base * 2^(attempt-1)，再叠加抖动。
func (m *transactionManager) backoff(attempt int) time.Duration {
	d := m.policy.BaseDelay
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if m.policy.Jitter > 0 {
		d = time.Duration(float64(d) * m.jitter(m.policy.Jitter))
	}
	return d
}

// uniformJitter 返回 [1-j, 1+j] 内的系数。
//
// 刻意使用 math/rand/v2 而不是 crypto/rand：这里只需要把重试时刻打散，
// 不涉及任何安全性质。用 crypto/rand 反而会引入系统调用，在高并发重试路径上
// 制造额外开销。（gosec 的 G404 会把两者混为一谈，故显式豁免并说明理由。）
func uniformJitter(j float64) float64 {
	if j <= 0 {
		return 1
	}
	return 1 - j + 2*j*rand.Float64() //nolint:gosec // 退避抖动无需密码学随机性
}

// sleepContext 在等待期间响应 ctx 取消，避免退避阻塞关闭流程。
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
