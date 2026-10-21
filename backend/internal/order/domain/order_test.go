package domain_test

import (
	"errors"
	"testing"

	"github.com/vevila2020/order-sys/internal/order/domain"
)

// 订单状态机（需求 §4.3）：
//
//	PENDING_PAYMENT -> COMPLETED / CANCELLED / PAYMENT_FAILED / PAYMENT_EXPIRED
//
// 四个目标都是终态，不可再迁移。

func newOrder(t *testing.T) *domain.Order {
	t.Helper()

	o, err := domain.NewOrder("order-1", "user-1", []domain.OrderItem{
		mustItem(t, "sku-1", 2, 100),
	})
	if err != nil {
		t.Fatalf("构造订单失败: %v", err)
	}
	return o
}

func mustItem(t *testing.T, skuID string, qty, unitPrice int64) domain.OrderItem {
	t.Helper()

	item, err := domain.NewOrderItem(skuID, qty, unitPrice)
	if err != nil {
		t.Fatalf("构造明细失败: %v", err)
	}
	return item
}

func TestNewOrder(t *testing.T) {
	o := newOrder(t)

	if o.ID() != "order-1" || o.UserID() != "user-1" {
		t.Fatalf("字段不正确: %s / %s", o.ID(), o.UserID())
	}
	if o.Status() != domain.StatusPendingPayment {
		t.Fatalf("新订单状态 = %s, want PENDING_PAYMENT", o.Status())
	}
	// 2 * 100 = 200
	if o.TotalCents() != 200 {
		t.Fatalf("总额 = %d, want 200", o.TotalCents())
	}
	if o.Currency() != "CNY" {
		t.Fatalf("币种 = %s, want CNY", o.Currency())
	}
}

// TestValidTransitions 覆盖四条合法迁移。
func TestValidTransitions(t *testing.T) {
	cases := []struct {
		name string
		do   func(*domain.Order) error
		want domain.OrderStatus
	}{
		{"完成", (*domain.Order).Complete, domain.StatusCompleted},
		{"取消", func(o *domain.Order) error { return o.Cancel("用户取消") }, domain.StatusCancelled},
		{"支付失败", func(o *domain.Order) error { return o.FailPayment("余额不足") }, domain.StatusPaymentFailed},
		{"支付超时", func(o *domain.Order) error { return o.ExpirePayment() }, domain.StatusPaymentExpired},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := newOrder(t)
			if err := c.do(o); err != nil {
				t.Fatalf("迁移失败: %v", err)
			}
			if o.Status() != c.want {
				t.Fatalf("status = %s, want %s", o.Status(), c.want)
			}
		})
	}
}

// TestTerminalStatesRejectFurtherTransitions 验证终态不可再迁移。
//
// 这是"取消与支付并发只有一个合法终态"的领域层保证：两个流程同时到达时，
// 谁先完成条件更新谁胜出，后者在领域层就被拒绝，不会覆盖对方的终态。
func TestTerminalStatesRejectFurtherTransitions(t *testing.T) {
	terminals := []struct {
		name  string
		enter func(*domain.Order) error
	}{
		{"COMPLETED", (*domain.Order).Complete},
		{"CANCELLED", func(o *domain.Order) error { return o.Cancel("x") }},
		{"PAYMENT_FAILED", func(o *domain.Order) error { return o.FailPayment("x") }},
		{"PAYMENT_EXPIRED", func(o *domain.Order) error { return o.ExpirePayment() }},
	}

	attempts := []struct {
		name string
		do   func(*domain.Order) error
	}{
		{"完成", (*domain.Order).Complete},
		{"取消", func(o *domain.Order) error { return o.Cancel("y") }},
		{"支付失败", func(o *domain.Order) error { return o.FailPayment("y") }},
		{"支付超时", func(o *domain.Order) error { return o.ExpirePayment() }},
	}

	for _, term := range terminals {
		for _, att := range attempts {
			t.Run(term.name+"_then_"+att.name, func(t *testing.T) {
				o := newOrder(t)
				if err := term.enter(o); err != nil {
					t.Fatalf("进入终态失败: %v", err)
				}
				terminal := o.Status()

				err := att.do(o)
				// 同一种迁移重复调用是幂等 no-op（同一终态重放），
				// 不算错误——见 TestRepeatedCancelIsIdempotent。
				if err == nil {
					return
				}
				if !errors.Is(err, domain.ErrInvalidStateTransition) {
					t.Fatalf("got %v, want ErrInvalidStateTransition", err)
				}
				// 被拒绝的迁移必须不改变终态：否则就是"报错了但数据变了"。
				if o.Status() != terminal {
					t.Fatalf("失败的迁移改变了状态: %s -> %s", terminal, o.Status())
				}
			})
		}
	}
}

// TestRepeatedCancelIsIdempotent 验证重复取消返回稳定结果而不是错误。
//
// 需求 §8.4：重复取消返回 200（稳定结果）。若这里返回错误，客户端重试会看到
// 一个"取消失败"的假象，而实际订单早已取消。
func TestRepeatedCancelIsIdempotent(t *testing.T) {
	o := newOrder(t)
	if err := o.Cancel("用户取消"); err != nil {
		t.Fatal(err)
	}
	if err := o.Cancel("用户取消"); err != nil {
		t.Fatalf("重复取消应成功，got %v", err)
	}
	if o.Status() != domain.StatusCancelled {
		t.Fatalf("status = %s, want CANCELLED", o.Status())
	}
}

// TestRepeatedCompleteIsIdempotent 同理：重复的支付成功回调不应报错。
//
// 这与"终态不可再迁移"不冲突：终态不允许迁到**另一个**状态，
// 但重复同一结果属于重放，必须幂等。
func TestRepeatedCompleteIsIdempotent(t *testing.T) {
	o := newOrder(t)
	if err := o.Complete(); err != nil {
		t.Fatal(err)
	}
	if err := o.Complete(); err != nil {
		t.Fatalf("重复完成应成功，got %v", err)
	}
}

// TestCancelAfterCompleteFails 验证完成后的取消被拒绝（反向不幂等）。
func TestCancelAfterCompleteFails(t *testing.T) {
	o := newOrder(t)
	if err := o.Complete(); err != nil {
		t.Fatal(err)
	}
	err := o.Cancel("想取消已完成的订单")
	if !errors.Is(err, domain.ErrInvalidStateTransition) {
		t.Fatalf("got %v, want ErrInvalidStateTransition", err)
	}
	if o.Status() != domain.StatusCompleted {
		t.Fatal("失败的迁移改变了状态")
	}
}

// TestCompleteAfterCancelFails 验证已关闭订单不能被迟到支付重新打开。
func TestCompleteAfterCancelFails(t *testing.T) {
	o := newOrder(t)
	if err := o.Cancel("超时"); err != nil {
		t.Fatal(err)
	}
	err := o.Complete()
	if !errors.Is(err, domain.ErrInvalidStateTransition) {
		t.Fatalf("got %v, want ErrInvalidStateTransition", err)
	}
}

func TestOrderStatusValid(t *testing.T) {
	for _, s := range []domain.OrderStatus{
		domain.StatusPendingPayment, domain.StatusCompleted, domain.StatusCancelled,
		domain.StatusPaymentFailed, domain.StatusPaymentExpired,
	} {
		if !s.Valid() {
			t.Errorf("%q 应合法", s)
		}
	}
	for _, s := range []domain.OrderStatus{"", "PAID", "pending_payment", "REJECTED"} {
		if s.Valid() {
			t.Errorf("%q 不应合法", s)
		}
	}
}

func TestOrderStatusIsTerminal(t *testing.T) {
	if domain.StatusPendingPayment.IsTerminal() {
		t.Fatal("PENDING_PAYMENT 不是终态")
	}
	for _, s := range []domain.OrderStatus{
		domain.StatusCompleted, domain.StatusCancelled,
		domain.StatusPaymentFailed, domain.StatusPaymentExpired,
	} {
		if !s.IsTerminal() {
			t.Errorf("%q 应为终态", s)
		}
	}
}

// TestCloseReasonRecorded 验证终态原因被记录，供排查与展示。
func TestCloseReasonRecorded(t *testing.T) {
	o := newOrder(t)
	if err := o.Cancel("库存异常"); err != nil {
		t.Fatal(err)
	}
	if o.CloseReason() != "库存异常" {
		t.Fatalf("close_reason = %q, want 库存异常", o.CloseReason())
	}
}

// TestVersionBumpsOnTransition 验证每次迁移递增版本，供仓储条件更新使用。
func TestVersionBumpsOnTransition(t *testing.T) {
	o := newOrder(t)
	before := o.Version()
	if err := o.Complete(); err != nil {
		t.Fatal(err)
	}
	if o.Version() != before+1 {
		t.Fatalf("version = %d, want %d", o.Version(), before+1)
	}

	// 幂等重放不再递增：否则每次重复回调都会让审计看到一次"变更"。
	afterFirst := o.Version()
	if err := o.Complete(); err != nil {
		t.Fatal(err)
	}
	if o.Version() != afterFirst {
		t.Fatalf("重复完成递增了版本: %d -> %d", afterFirst, o.Version())
	}
}

func TestNewOrderValidates(t *testing.T) {
	validItem := mustItem(t, "sku-1", 1, 100)

	cases := []struct {
		name    string
		orderID string
		userID  string
		items   []domain.OrderItem
		wantErr error
	}{
		{"空 order_id", "", "user-1", []domain.OrderItem{validItem}, domain.ErrInvalidOrderID},
		{"空 user_id", "order-1", "", []domain.OrderItem{validItem}, domain.ErrInvalidUserID},
		{"空明细", "order-1", "user-1", nil, domain.ErrEmptyOrder},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := domain.NewOrder(c.orderID, c.userID, c.items)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

// TestOrderItemsAreCopied 验证构造时复制明细，防止调用方后续修改影响订单。
func TestOrderItemsAreCopied(t *testing.T) {
	items := []domain.OrderItem{mustItem(t, "sku-1", 1, 100)}
	o, err := domain.NewOrder("order-1", "user-1", items)
	if err != nil {
		t.Fatal(err)
	}

	items[0] = mustItem(t, "sku-2", 9, 999)
	got := o.Items()
	if len(got) != 1 || got[0].SKUID() != "sku-1" {
		t.Fatalf("构造后修改源切片影响了订单: %+v", got)
	}
}
