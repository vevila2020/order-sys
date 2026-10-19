package healthx

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmptyRegistryIsReady(t *testing.T) {
	r := NewRegistry(time.Second)
	if got := r.Run(context.Background()).Status; got != StatusOK {
		t.Errorf("empty registry: got %s", got)
	}
}

func TestCriticalFailureMakesReadyFail(t *testing.T) {
	r := NewRegistry(time.Second)
	r.Register(Check{Name: "mysql", Critical: true, Func: func(context.Context) error {
		return errors.New("connection refused")
	}})
	report := r.Run(context.Background())
	if report.Status != StatusFail {
		t.Fatalf("status: got %s", report.Status)
	}
	if len(report.Checks) != 1 || report.Checks[0].Status != StatusFail {
		t.Errorf("checks: %+v", report.Checks)
	}
}

func TestNonCriticalFailureIsDegraded(t *testing.T) {
	r := NewRegistry(time.Second)
	r.Register(Check{Name: "redis", Critical: false, Func: func(context.Context) error {
		return errors.New("down")
	}})
	r.Register(Check{Name: "mysql", Critical: true, Func: func(context.Context) error { return nil }})

	report := r.Run(context.Background())
	// 非关键依赖失败不能阻断普通下单，只标记降级。
	if report.Status != StatusDegraded {
		t.Fatalf("status: got %s", report.Status)
	}
}

func TestCriticalFailureDominatesDegraded(t *testing.T) {
	r := NewRegistry(time.Second)
	r.Register(Check{Name: "redis", Critical: false, Func: func(context.Context) error {
		return errors.New("down")
	}})
	r.Register(Check{Name: "mysql", Critical: true, Func: func(context.Context) error {
		return errors.New("down")
	}})
	if got := r.Run(context.Background()).Status; got != StatusFail {
		t.Errorf("status: got %s want fail", got)
	}
}

func TestCheckTimeoutCountsAsFailure(t *testing.T) {
	r := NewRegistry(10 * time.Millisecond)
	release := make(chan struct{})
	defer close(release)
	r.Register(Check{Name: "slow", Critical: true, Func: func(context.Context) error {
		// 阻塞到测试结束，验证注册表不会无限等待。
		<-release
		return nil
	}})

	start := time.Now()
	report := r.Run(context.Background())
	elapsed := time.Since(start)

	if report.Status != StatusFail {
		t.Fatalf("status: got %s", report.Status)
	}
	if elapsed > time.Second {
		t.Fatalf("run blocked too long: %s", elapsed)
	}
}

func TestRegistryReplacesDuplicateCheck(t *testing.T) {
	r := NewRegistry(time.Second)
	calls := int32(0)
	r.Register(Check{Name: "db", Critical: true, Func: func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}})
	r.Register(Check{Name: "db", Critical: true, Func: func(context.Context) error {
		atomic.AddInt32(&calls, 10)
		return nil
	}})
	r.Run(context.Background())
	if got := atomic.LoadInt32(&calls); got != 10 {
		t.Errorf("expected replaced check to run once, calls=%d", got)
	}
}

func TestRunReturnsSortedChecks(t *testing.T) {
	r := NewRegistry(time.Second)
	for _, name := range []string{"zeta", "alpha", "mysql"} {
		r.Register(Check{Name: name, Func: func(context.Context) error { return nil }})
	}
	report := r.Run(context.Background())
	want := []string{"alpha", "mysql", "zeta"}
	for i, res := range report.Checks {
		if res.Name != want[i] {
			t.Fatalf("order: got %v want %v", report.Checks, want)
		}
	}
}
