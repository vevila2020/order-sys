package domain_test

import (
	"errors"
	"testing"

	"github.com/vevila2020/order-sys/internal/inventory/domain"
)

func TestNewSKU(t *testing.T) {
	cases := []struct {
		name    string
		skuID   string
		price   int64
		wantErr error
	}{
		{"正常", "sku-1", 29900, nil},
		{"零价格合法（赠品）", "sku-1", 0, nil},
		{"空 sku_id", "", 100, domain.ErrInvalidSKUID},
		{"负价格", "sku-1", -1, domain.ErrInvalidPrice},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := domain.NewSKU(c.skuID, "名字", c.price)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}

// TestSKUUpdatePriceBumpsVersion 验证改价递增版本。
//
// 版本用于 SKU 管理的丢失更新检测：两个管理员同时改价，后提交的一方应收到 409，
// 而不是静默覆盖对方的修改。
func TestSKUUpdatePriceBumpsVersion(t *testing.T) {
	sku, err := domain.NewSKU("sku-1", "名字", 100)
	if err != nil {
		t.Fatal(err)
	}
	before := sku.Version()

	if err := sku.UpdatePrice(200); err != nil {
		t.Fatalf("改价失败: %v", err)
	}
	if sku.PriceCents() != 200 {
		t.Fatalf("price = %d, want 200", sku.PriceCents())
	}
	if sku.Version() != before+1 {
		t.Fatalf("version = %d, want %d", sku.Version(), before+1)
	}

	// 非法价格不应改变任何状态。
	priceBefore, versionBefore := sku.PriceCents(), sku.Version()
	if err := sku.UpdatePrice(-5); !errors.Is(err, domain.ErrInvalidPrice) {
		t.Fatalf("负价格应被拒绝，got %v", err)
	}
	if sku.PriceCents() != priceBefore || sku.Version() != versionBefore {
		t.Fatal("失败的改价改变了状态")
	}
}

func TestSKUStatusTransitions(t *testing.T) {
	sku, err := domain.NewSKU("sku-1", "名字", 100)
	if err != nil {
		t.Fatal(err)
	}
	if sku.Status() != domain.SKUActive {
		t.Fatalf("新建 SKU 应为 ACTIVE，got %s", sku.Status())
	}

	if err := sku.Deactivate(); err != nil {
		t.Fatalf("停用失败: %v", err)
	}
	if sku.Status() != domain.SKUInactive {
		t.Fatalf("status = %s, want INACTIVE", sku.Status())
	}

	// 重复停用是幂等的 no-op，不是错误：运营重复点击不该报 409。
	versionBefore := sku.Version()
	if err := sku.Deactivate(); err != nil {
		t.Fatalf("重复停用应为 no-op，got %v", err)
	}
	if sku.Version() != versionBefore {
		t.Fatal("no-op 不应递增版本")
	}

	if err := sku.Activate(); err != nil {
		t.Fatalf("启用失败: %v", err)
	}
	if sku.Status() != domain.SKUActive {
		t.Fatalf("status = %s, want ACTIVE", sku.Status())
	}
}

// TestSKUStatusValid 验证状态的合法值判定，供反序列化时拒绝脏数据。
func TestSKUStatusValid(t *testing.T) {
	valid := []domain.SKUStatus{domain.SKUActive, domain.SKUInactive}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("%q 应合法", s)
		}
	}
	for _, s := range []domain.SKUStatus{"", "active", "DELETED"} {
		if s.Valid() {
			t.Errorf("%q 不应合法", s)
		}
	}
}
