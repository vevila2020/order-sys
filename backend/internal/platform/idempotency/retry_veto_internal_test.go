package idempotency

import (
	"context"
	"errors"
	"testing"
)

// 这个用例放在内部测试包：inProgressError 不导出，而"否决事务重试"是一个
// 必须锁定的实现契约——它决定客户端收到 409 还是 503（ADR-0006 第 4 节）。
// 用 _test 包只能通过公开行为间接验证，容易在重构时悄悄失效。
func TestInProgressErrorVetoesRetry(t *testing.T) {
	err := &inProgressError{cause: context.DeadlineExceeded}

	if !errors.Is(err, ErrInProgress) {
		t.Fatal("inProgressError 必须能被 errors.Is(err, ErrInProgress) 命中")
	}
	// db.retryVetoer 是结构化接口，这里按同样签名断言，不 import db（避免循环）。
	var veto interface{ VetoRetry() bool }
	if !errors.As(err, &veto) {
		t.Fatal("inProgressError 必须实现 VetoRetry，否则事务会被无谓地重试")
	}
	if !veto.VetoRetry() {
		t.Fatal("VetoRetry 必须返回 true")
	}
	// Unwrap 只返回哨兵：原始原因（这里是 context.DeadlineExceeded）不能进入
	// Is 判定链，否则上层会把"等同一个键"误判成"自己的请求超时"。
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cause 不应进入 Is 判定链：会被误判为请求超时")
	}
}

// ErrKeyReused 不否决重试：它不是并发等待，而是确定性的业务结论。
// 若它被重试，只会得到同一个结果并浪费预算。
func TestKeyReusedDoesNotVetoRetry(t *testing.T) {
	err := ErrKeyReused
	var veto interface{ VetoRetry() bool }
	if errors.As(err, &veto) {
		t.Fatal("ErrKeyReused 不应实现 VetoRetry：重试不会改变结论")
	}
}
