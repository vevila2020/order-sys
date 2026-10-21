package db

import (
	"errors"
	"fmt"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// MySQL 错误码（协议字段，不随驱动版本或错误文案变化）。
const (
	// errLockDeadlock 是 ER_LOCK_DEADLOCK：InnoDB 检测到死锁后主动回滚事务。
	errLockDeadlock uint16 = 1213
	// errLockWaitTimeout 是 ER_LOCK_WAIT_TIMEOUT：等锁超过 innodb_lock_wait_timeout。
	errLockWaitTimeout uint16 = 1205
)

// IsRetryable 判断错误是否属于"整个事务可以安全重试"的瞬时争用。
//
// 只有死锁与锁等待超时可以重试。其他错误一律不重试：
//   - 1062 唯一约束冲突是业务结论（幂等重放、每单一个支付单），重试只会再撞一次；
//   - context 超时是预算结论，重试只会把超时再拖一遍；
//   - 语法错误、类型错误重试永远是同一个结果。
func IsRetryable(err error) bool {
	code, ok := mysqlErrorCode(err)
	if !ok {
		return false
	}
	return code == errLockDeadlock || code == errLockWaitTimeout
}

// mysqlErrorCode 提取错误链中的 MySQL 错误码。
//
// 用 errors.As 而不是匹配错误文本：文案会随服务端语言或驱动版本变化，
// 错误码是稳定的协议字段。
func mysqlErrorCode(err error) (uint16, bool) {
	var me *mysqldriver.MySQLError
	if errors.As(err, &me) {
		return me.Number, true
	}
	return 0, false
}

// retryVetoer 让被包装的错误否决本次重试。
//
// 动机来自 ADR-0006 第 4 节：幂等键 INSERT 上的锁等待超时表示"另一个请求正在
// 处理同一个键"，此时重试整个事务只会再次排到同一个队尾，应当直接映射为
// IDEMPOTENCY_IN_PROGRESS 交给客户端退避。db 包不能 import 业务包，因此用
// 接口由调用方（platform/idempotency）实现否决权。
type retryVetoer interface {
	// VetoRetry 返回 true 表示该错误不应触发事务级重试。
	VetoRetry() bool
}

// retryAllowed 返回该错误是否应当重试，以及否决原因（用于日志与注释）。
func retryAllowed(err error) bool {
	if err == nil {
		return false
	}
	var veto retryVetoer
	if errors.As(err, &veto) && veto.VetoRetry() {
		return false
	}
	return IsRetryable(err)
}

// RetryExhaustedError 表示事务因持续争用耗尽重试预算。
//
// 它保留最后一次的原始错误（Unwrap），使调用方仍能用 errors.As 取到
// *mysql.MySQLError 做排查；对外则由 Error() 映射为 DEPENDENCY_UNAVAILABLE，
// 不泄露 SQL 或 DSN。
type RetryExhaustedError struct {
	// Attempts 是实际执行的事务尝试次数（含首次）。
	Attempts int
	// Last 是最后一次失败的原始错误。
	Last error
}

// Error 输出可直接进日志的描述。原始错误只出现在这里，不进入 HTTP 响应体。
func (e *RetryExhaustedError) Error() string {
	return fmt.Sprintf("事务重试耗尽（%d 次尝试）: %v", e.Attempts, e.Last)
}

// Unwrap 保留错误链，供 errors.Is/As 使用。
func (e *RetryExhaustedError) Unwrap() error { return e.Last }

// AsAppError 把重试耗尽映射为对外错误。
//
// 选择 DEPENDENCY_UNAVAILABLE(503) 而不是 CONFLICT(409)：409 在本系统中专门
// 表示业务前提不成立（库存不足、状态机不允许、幂等键冲突），客户端据此会放弃
// 重试；而死锁耗尽纯粹是瞬时争用，同一请求稍后重试通常成功。附 Retry-After
// 给出退避建议（ADR-0003 第 5 节）。
func (e *RetryExhaustedError) AsAppError() *httpx.AppError {
	return httpx.NewError(httpx.CodeDependencyUnavailable, e).WithRetryAfter(retryAfterHint)
}

// retryAfterHint 是死锁耗尽时建议客户端的退避时间。
// 取 1 秒：足够让冲突事务提交或回滚，又不会让客户端等待过久。
const retryAfterHint = time.Second
