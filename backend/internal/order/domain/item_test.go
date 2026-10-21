package domain_test

import (
	"errors"
	"math"
	"testing"

	"github.com/vevila2020/order-sys/internal/order/domain"
)

func TestNewOrderItem(t *testing.T) {
	cases := []struct {
		name    string
		skuID   string
		qty     int64
		price   int64
		wantErr error
	}{
		{"正常", "sku-1", 2, 100, nil},
		{"空 sku_id", "", 1, 100, domain.ErrInvalidSKUID},
		{"数量为 0", "sku-1", 0, 100, domain.ErrInvalidQuantity},
		{"数量为负", "sku-1", -1, 100, domain.ErrInvalidQuantity},
		{"单价为负", "sku-1", 1, -1, domain.ErrInvalidPrice},
		{"单价为 0 合法（赠品）", "sku-1", 1, 0, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			item, err := domain.NewOrderItem(c.skuID, c.qty, c.price)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
			if c.wantErr == nil && item.AmountCents() != c.qty*c.price {
				t.Fatalf("amount = %d, want %d", item.AmountCents(), c.qty*c.price)
			}
		})
	}
}

// TestOrderItemDetectsOverflow 验证金额溢出被拒绝。
//
// 金额用 int64 表示最小货币单位，理论上限约 9.2e18 分（≈ 9.2 京元），
// 单笔订单不可能触达。但如果 quantity 与 unit_price 都来自用户输入，
// 乘法溢出会静默回绕成一个很小的正数，导致总额校验通过而实际少收钱。
// 宁可拒绝这笔不可能成立的订单。
func TestOrderItemDetectsOverflow(t *testing.T) {
	_, err := domain.NewOrderItem("sku-1", math.MaxInt64, 2)
	if !errors.Is(err, domain.ErrAmountOverflow) {
		t.Fatalf("got %v, want ErrAmountOverflow", err)
	}
}

// TestOrderItemSnapshot 验证明细是价格快照：之后改 SKU 价格不影响已有明细。
func TestOrderItemSnapshot(t *testing.T) {
	item, err := domain.NewOrderItem("sku-1", 2, 100)
	if err != nil {
		t.Fatal(err)
	}

	// OrderItem 是值类型且字段不可导出，外部无法修改单价；
	// 这里验证它保存的是构造时传入的价格而不是对某个外部变量的引用。
	if item.UnitPriceCents() != 100 {
		t.Fatalf("unit_price = %d, want 100", item.UnitPriceCents())
	}
	if item.Quantity() != 2 || item.SKUID() != "sku-1" {
		t.Fatalf("字段不正确: %+v", item)
	}
}
