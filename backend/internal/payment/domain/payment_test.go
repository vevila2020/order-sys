package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vevila2020/order-sys/internal/payment/domain"
)

func newPayment(t *testing.T) *domain.Payment {
	t.Helper()

	p, err := domain.NewPayment("pay-1", "order-1", 29900)
	if err != nil {
		t.Fatalf("构造支付单失败: %v", err)
	}
	return p
}

func TestNewPayment(t *testing.T) {
	p := newPayment(t)

	if p.ID() != "pay-1" || p.OrderID() != "order-1" {
		t.Fatalf("字段不正确: %s / %s", p.ID(), p.OrderID())
	}
	if p.AmountCents() != 29900 {
		t.Fatalf("amount = %d, want 29900", p.AmountCents())
	}
	if p.Status() != domain.StatusPending {
		t.Fatalf("status = %s, want PENDING", p.Status())
	}
	if p.IsTerminal() {
		t.Fatal("PENDING 不是终态")
	}
}

func TestNewPaymentValidates(t *testing.T) {
	cases := []struct {
		name      string
		paymentID string
		orderID   string
		amount    int64
		wantErr   error
	}{
		{"空 payment_id", "", "order-1", 100, domain.ErrInvalidPaymentID},
		{"空 order_id", "pay-1", "", 100, domain.ErrInvalidOrderID},
		{"负金额", "pay-1", "order-1", -1, domain.ErrInvalidAmount},
		{"零金额合法（全额优惠订单）", "pay-1", "order-1", 0, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := domain.NewPayment(c.paymentID, c.orderID, c.amount)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

// TestValidTransitions 覆盖三条合法迁移。
func TestValidTransitions(t *testing.T) {
	cases := []struct {
		name string
		do   func(*domain.Payment) (bool, error)
		want domain.PaymentStatus
	}{
		{"成功", (*domain.Payment).Succeed, domain.StatusSucceeded},
		{"失败", func(p *domain.Payment) (bool, error) { return p.Fail("余额不足") }, domain.StatusFailed},
		{"取消", (*domain.Payment).Cancel, domain.StatusCancelled},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPayment(t)
			// 首次迁移必然发生：changed 为 true。
			changed, err := c.do(p)
			if err != nil {
				t.Fatalf("迁移失败: %v", err)
			}
			if !changed {
				t.Fatal("首次迁移应返回 changed=true")
			}
			if p.Status() != c.want {
				t.Fatalf("status = %s, want %s", p.Status(), c.want)
			}
			if !p.IsTerminal() {
				t.Fatal("迁移后应为终态")
			}
		})
	}
}

// TestSameResultReplayIsIdempotent 验证相同结果的重复回调返回 changed=false 且无错误。
//
// 需求 §8.4：相同结果重复调用返回 200。支付渠道会重发回调，这是常态而不是异常；
// 若第二次报错，渠道会认为支付失败并持续重试。
//
// 返回 "changed" 而不是 nil error 才能让上层区分"这次真的迁移了"与"重放"：
// 前者需要写 Outbox 事件与审计，后者什么都不该做。
func TestSameResultReplayIsIdempotent(t *testing.T) {
	cases := []struct {
		name string
		do   func(*domain.Payment) (bool, error)
	}{
		{"重复成功", (*domain.Payment).Succeed},
		{"重复失败", func(p *domain.Payment) (bool, error) { return p.Fail("x") }},
		{"重复取消", (*domain.Payment).Cancel},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPayment(t)
			if _, err := c.do(p); err != nil {
				t.Fatalf("首次迁移失败: %v", err)
			}
			changed, err := c.do(p)
			if err != nil {
				t.Fatalf("重复同一结果不应报错，got %v", err)
			}
			if changed {
				t.Fatal("重复同一结果不应报告发生了迁移")
			}
		})
	}
}

// TestOppositeResultRejected 验证相反结果不能覆盖已有终态。
//
// 这是最关键的约束：若允许覆盖，一个推迟到达的"失败"回调会把已完成订单改判为
// 失败，或一个迟到的"成功"回调把已取消订单重新打开——两条路径都会让库存与资金
// 状态失去一致性。
func TestOppositeResultRejected(t *testing.T) {
	cases := []struct {
		name  string
		first func(*domain.Payment) (bool, error)
		then  func(*domain.Payment) (bool, error)
	}{
		{"成功后失败", (*domain.Payment).Succeed, func(p *domain.Payment) (bool, error) { return p.Fail("x") }},
		{"成功后取消", (*domain.Payment).Succeed, (*domain.Payment).Cancel},
		{"失败后成功", func(p *domain.Payment) (bool, error) { return p.Fail("x") }, (*domain.Payment).Succeed},
		{"失败后取消", func(p *domain.Payment) (bool, error) { return p.Fail("x") }, (*domain.Payment).Cancel},
		{"取消后成功", (*domain.Payment).Cancel, (*domain.Payment).Succeed},
		{"取消后失败", (*domain.Payment).Cancel, func(p *domain.Payment) (bool, error) { return p.Fail("x") }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newPayment(t)
			if _, err := c.first(p); err != nil {
				t.Fatalf("首次迁移失败: %v", err)
			}
			terminal := p.Status()

			_, err := c.then(p)
			if !errors.Is(err, domain.ErrInvalidStateTransition) {
				t.Fatalf("got %v, want ErrInvalidStateTransition", err)
			}
			// 被拒绝的迁移不得改变已有终态。
			if p.Status() != terminal {
				t.Fatalf("失败的迁移改变了终态: %s -> %s", terminal, p.Status())
			}
		})
	}
}

// TestFinishedAtRecorded 验证首次迁移记录完成时间，重放不覆盖。
//
// 重放不能刷新 finished_at：那会让审计看到"这笔支付在上个月成功过一次，
// 又在今天成功了一次"，而实际只成功过一次。
func TestFinishedAtRecorded(t *testing.T) {
	p := newPayment(t)
	if !p.FinishedAt().IsZero() {
		t.Fatal("PENDING 不应有完成时间")
	}

	if _, err := p.Succeed(); err != nil {
		t.Fatal(err)
	}
	first := p.FinishedAt()
	if first.IsZero() {
		t.Fatal("迁移后应记录完成时间")
	}

	// 重复成功不应刷新时间。
	if _, err := p.Succeed(); err != nil {
		t.Fatal(err)
	}
	if !p.FinishedAt().Equal(first) {
		t.Fatalf("重放刷新了 finished_at: %v -> %v", first, p.FinishedAt())
	}
}

// TestFinishedAtIsUTC 验证时间统一 UTC（需求 §4.2）。
func TestFinishedAtIsUTC(t *testing.T) {
	p := newPayment(t)
	if _, err := p.Succeed(); err != nil {
		t.Fatal(err)
	}
	if loc := p.FinishedAt().Location().String(); loc != "UTC" {
		t.Fatalf("finished_at 时区 = %s, want UTC", loc)
	}
}

func TestProviderRef(t *testing.T) {
	p := newPayment(t)
	if p.ProviderRef() != "" {
		t.Fatal("新建支付单不应有渠道参考号")
	}
	p.SetProviderRef("ref-12345")
	if p.ProviderRef() != "ref-12345" {
		t.Fatalf("provider_ref = %q", p.ProviderRef())
	}
}

func TestPaymentStatusValid(t *testing.T) {
	for _, s := range []domain.PaymentStatus{
		domain.StatusPending, domain.StatusSucceeded, domain.StatusFailed, domain.StatusCancelled,
	} {
		if !s.Valid() {
			t.Errorf("%q 应合法", s)
		}
	}
	for _, s := range []domain.PaymentStatus{"", "PAID", "pending", "REFUNDED"} {
		if s.Valid() {
			t.Errorf("%q 不应合法", s)
		}
	}
}

func TestPaymentStatusIsTerminal(t *testing.T) {
	if domain.StatusPending.IsTerminal() {
		t.Fatal("PENDING 不是终态")
	}
	for _, s := range []domain.PaymentStatus{
		domain.StatusSucceeded, domain.StatusFailed, domain.StatusCancelled,
	} {
		if !s.IsTerminal() {
			t.Errorf("%q 应为终态", s)
		}
	}
}

// TestFailureReasonRecorded 验证失败原因被记录，供排查与展示。
func TestFailureReasonRecorded(t *testing.T) {
	p := newPayment(t)
	if _, err := p.Fail("余额不足"); err != nil {
		t.Fatal(err)
	}
	if p.FailureReason() != "余额不足" {
		t.Fatalf("failure_reason = %q", p.FailureReason())
	}
}

// TestRehydratePayment 覆盖 infra 层还原路径。
func TestRehydratePayment(t *testing.T) {
	t.Run("恢复终态", func(t *testing.T) {
		finished := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
		p, err := domain.RehydratePayment("pay-1", "order-1", 100, domain.StatusSucceeded, "ref-1", "", finished, 2)
		if err != nil {
			t.Fatalf("恢复失败: %v", err)
		}
		if !p.IsTerminal() || p.Status() != domain.StatusSucceeded {
			t.Fatalf("status = %s", p.Status())
		}
		if p.ProviderRef() != "ref-1" {
			t.Fatalf("provider_ref = %q", p.ProviderRef())
		}
		if !p.FinishedAt().Equal(finished) {
			t.Fatalf("finished_at = %v, want %v", p.FinishedAt(), finished)
		}
	})

	t.Run("非 UTC 输入被转换", func(t *testing.T) {
		// DATETIME(6) 不保存时区，驱动读回的时间可能带本地时区；
		// 还原时统一转 UTC，避免同一时刻在 API 上显示成不同字符串。
		loc := time.FixedZone("CST", 8*3600)
		local := time.Date(2026, 10, 21, 20, 0, 0, 0, loc)
		p, err := domain.RehydratePayment("pay-1", "order-1", 100, domain.StatusSucceeded, "", "", local, 1)
		if err != nil {
			t.Fatalf("恢复失败: %v", err)
		}
		if p.FinishedAt().Location() != time.UTC {
			t.Fatalf("location = %v, want UTC", p.FinishedAt().Location())
		}
		if !p.FinishedAt().Equal(local) {
			t.Fatalf("转换改变了时刻: %v vs %v", p.FinishedAt(), local)
		}
	})

	t.Run("未知状态拒绝", func(t *testing.T) {
		_, err := domain.RehydratePayment("pay-1", "order-1", 100, "REFUNDED", "", "", time.Time{}, 0)
		if !errors.Is(err, domain.ErrInvalidStateTransition) {
			t.Fatalf("got %v, want ErrInvalidStateTransition", err)
		}
	})
}
