package domain

import "math"

// 金额一律用 int64 表示最小货币单位（分），禁止 float64（需求 §4.2）。
//
// 浮点的问题不是精度"通常够用"，而是它把误差藏进相等比较与累加里：
// 0.1 + 0.2 != 0.3，而订单总额要与支付金额精确相等才能对账。
//
// 所有金额运算集中在本文件，是唯一需要处理溢出的地方。

// SumAmounts 求和并检测溢出。
func SumAmounts(amounts []int64) (int64, error) {
	var sum int64
	for _, a := range amounts {
		// 用"加完再减回去"判断溢出，比检查符号位更直观，也不依赖具体补码行为。
		next := sum + a
		if next < sum {
			return 0, ErrAmountOverflow
		}
		sum = next
	}
	return sum, nil
}

// MulAmount 计算 单价 × 数量 并检测溢出。
//
// 拒绝负数操作数：两个负数相乘得到正数，会被误认为合法金额。
// 金额运算的输入合法性应由此处统一把关，而不是依赖调用方自觉。
func MulAmount(quantity, unitPriceCents int64) (int64, error) {
	if quantity < 0 || unitPriceCents < 0 {
		return 0, ErrInvalidQuantity
	}
	if quantity == 0 || unitPriceCents == 0 {
		return 0, nil
	}
	// 先做上界检查再相乘：int64 溢出是回绕而不是 panic，
	// 直接相乘再判断结果永远发现不了问题。
	if quantity > math.MaxInt64/unitPriceCents {
		return 0, ErrAmountOverflow
	}
	return quantity * unitPriceCents, nil
}
