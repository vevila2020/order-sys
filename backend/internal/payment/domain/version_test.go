package domain_test

import "testing"

// TestVersionSemantics 锁定版本的递增规则。
//
// Version 是仓储条件更新的判据：`UPDATE ... WHERE status = 'PENDING'` 之外，
// 版本也用于审计与排查。它必须满足：
//   - 真实迁移递增；
//   - 重放不递增（否则每次渠道重发都像一次状态变更）；
//   - SetProviderRef 不递增（参考号不是状态变更）。
func TestVersionSemantics(t *testing.T) {
	p := newPayment(t)
	if p.Version() != 0 {
		t.Fatalf("新建时 version = %d, want 0", p.Version())
	}

	if _, err := p.Succeed(); err != nil {
		t.Fatal(err)
	}
	if p.Version() != 1 {
		t.Fatalf("迁移后 version = %d, want 1", p.Version())
	}

	// 重放：版本不变。
	if _, err := p.Succeed(); err != nil {
		t.Fatal(err)
	}
	if p.Version() != 1 {
		t.Fatalf("重放后 version = %d, want 1", p.Version())
	}

	// 设置参考号：版本不变。
	p.SetProviderRef("ref-1")
	if p.Version() != 1 {
		t.Fatalf("设置参考号后 version = %d, want 1", p.Version())
	}

	// 相反结果被拒绝时版本也不变。
	if _, err := p.Fail("x"); err == nil {
		t.Fatal("期望相反结果被拒绝")
	}
	if p.Version() != 1 {
		t.Fatalf("被拒绝的迁移改变了 version: %d", p.Version())
	}
}
