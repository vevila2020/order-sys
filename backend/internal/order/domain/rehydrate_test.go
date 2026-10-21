package domain_test

import (
	"errors"
	"testing"

	"github.com/vevila2020/order-sys/internal/order/domain"
)

// Rehydrate 系列是 infra 层把数据库行还原为领域对象的入口，
// 因此它们的正确性直接决定查询接口返回什么。

func TestRehydrateOrderItem(t *testing.T) {
	// 金额采用存储值，不重新计算：若历史数据与当前算法不一致，
	// 重新计算会掩盖差异，查询结果就会与数据库不符。
	item := domain.RehydrateOrderItem("sku-1", 2, 100, 200)
	if item.SKUID() != "sku-1" || item.Quantity() != 2 {
		t.Fatalf("字段不正确: %+v", item)
	}
	if item.UnitPriceCents() != 100 || item.AmountCents() != 200 {
		t.Fatalf("金额不正确: unit=%d amount=%d", item.UnitPriceCents(), item.AmountCents())
	}

	// 存储值与"数量×单价"不一致时保留存储值——这正是需要暴露的数据问题。
	mismatched := domain.RehydrateOrderItem("sku-1", 2, 100, 999)
	if mismatched.AmountCents() != 999 {
		t.Fatalf("应保留存储金额 999，got %d", mismatched.AmountCents())
	}
}

func TestRehydrateOrder(t *testing.T) {
	t.Run("恢复终态订单", func(t *testing.T) {
		o, err := domain.RehydrateOrder("order-1", "user-1", domain.StatusCancelled,
			[]domain.OrderItem{domain.RehydrateOrderItem("sku-1", 1, 100, 100)},
			100, "CNY", "用户取消", 3, testCreatedAt)
		if err != nil {
			t.Fatalf("恢复失败: %v", err)
		}
		if o.Status() != domain.StatusCancelled || !o.Status().IsTerminal() {
			t.Fatalf("status = %s", o.Status())
		}
		if o.CloseReason() != "用户取消" || o.Version() != 3 {
			t.Fatalf("reason=%q version=%d", o.CloseReason(), o.Version())
		}
		if o.TotalCents() != 100 || o.Currency() != "CNY" {
			t.Fatalf("total=%d currency=%s", o.TotalCents(), o.Currency())
		}
	})

	t.Run("未知状态拒绝", func(t *testing.T) {
		_, err := domain.RehydrateOrder("order-1", "user-1", "PAID", nil, 0, "CNY", "", 0, testCreatedAt)
		if !errors.Is(err, domain.ErrInvalidStateTransition) {
			t.Fatalf("got %v, want ErrInvalidStateTransition", err)
		}
	})

	t.Run("明细被复制", func(t *testing.T) {
		items := []domain.OrderItem{domain.RehydrateOrderItem("sku-1", 1, 100, 100)}
		o, err := domain.RehydrateOrder("order-1", "user-1", domain.StatusPendingPayment, items, 100, "CNY", "", 0, testCreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		items[0] = domain.RehydrateOrderItem("sku-2", 9, 9, 81)
		if got := o.Items(); len(got) != 1 || got[0].SKUID() != "sku-1" {
			t.Fatalf("恢复时未复制明细: %+v", got)
		}
	})

	t.Run("恢复后仍可迁移", func(t *testing.T) {
		// 从库中读出的 PENDING_PAYMENT 订单必须能继续走支付流程。
		o, err := domain.RehydrateOrder("order-1", "user-1", domain.StatusPendingPayment, nil, 100, "CNY", "", 0, testCreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := o.Complete(); err != nil {
			t.Fatalf("恢复后的订单无法迁移: %v", err)
		}
	})
}

// TestNewOrderRejectsDuplicateSKU 补足重复明细的校验分支。
func TestNewOrderRejectsDuplicateSKU(t *testing.T) {
	_, err := domain.NewOrder("order-1", "user-1", []domain.OrderItem{
		mustItem(t, "sku-1", 1, 100),
		mustItem(t, "sku-1", 2, 100),
	}, testCreatedAt)
	if !errors.Is(err, domain.ErrDuplicateSKU) {
		t.Fatalf("got %v, want ErrDuplicateSKU", err)
	}
}

// TestNewOrderPropagatesAmountOverflow 验证构造订单时溢出会被传播。
func TestNewOrderPropagatesAmountOverflow(t *testing.T) {
	// 单行金额已经溢出，构造订单应把错误透传而不是存一个回绕后的总额。
	item := domain.RehydrateOrderItem("sku-1", 1<<62, 4, -1)
	_, err := domain.NewOrder("order-1", "user-1", []domain.OrderItem{item}, testCreatedAt)
	if !errors.Is(err, domain.ErrAmountOverflow) {
		t.Fatalf("got %v, want ErrAmountOverflow", err)
	}
}
