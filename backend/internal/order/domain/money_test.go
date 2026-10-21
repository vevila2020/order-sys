package domain_test

import (
	"errors"
	"math"
	"testing"

	"github.com/vevila2020/order-sys/internal/order/domain"
)

// 金额运算集中在这里，因为它是唯一需要处理溢出与精度的位置。
// 需求明确禁止浮点金额，所有金额是 int64 的最小货币单位。

func TestSumAmounts(t *testing.T) {
	cases := []struct {
		name    string
		in      []int64
		want    int64
		wantErr error
	}{
		{"空切片为 0", nil, 0, nil},
		{"单值", []int64{100}, 100, nil},
		{"多值", []int64{100, 200, 300}, 600, nil},
		{"含零", []int64{0, 100, 0}, 100, nil},
		{"溢出", []int64{math.MaxInt64, 1}, 0, domain.ErrAmountOverflow},
		{"累加中途溢出", []int64{math.MaxInt64 / 2, math.MaxInt64/2 + 2}, 0, domain.ErrAmountOverflow},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.SumAmounts(c.in)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
			if c.wantErr == nil && got != c.want {
				t.Fatalf("sum = %d, want %d", got, c.want)
			}
		})
	}
}

func TestMulAmount(t *testing.T) {
	cases := []struct {
		name    string
		qty     int64
		price   int64
		want    int64
		wantErr error
	}{
		{"正常", 3, 100, 300, nil},
		{"零单价", 5, 0, 0, nil},
		{"溢出", math.MaxInt64, 2, 0, domain.ErrAmountOverflow},
		{"大数相乘溢出", math.MaxInt64 / 2, 3, 0, domain.ErrAmountOverflow},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.MulAmount(c.qty, c.price)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
			if c.wantErr == nil && got != c.want {
				t.Fatalf("mul = %d, want %d", got, c.want)
			}
		})
	}
}

// TestMulAmountRejectsNegative 验证负数乘法被拒绝而不是静默算出负数金额。
func TestMulAmountRejectsNegative(t *testing.T) {
	// 负数 * 负数 = 正数，会被误认为合法。金额运算不应接受负操作数。
	if _, err := domain.MulAmount(-2, -3); !errors.Is(err, domain.ErrInvalidQuantity) {
		t.Fatalf("got %v, want ErrInvalidQuantity", err)
	}
}
