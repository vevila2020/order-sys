package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/vevila2020/order-sys/internal/inventory/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// Service 编排 inventory 模块的用例。
//
// 依赖全部是端口（接口），由 main 注入 infra 实现。事务边界在方法内部：
// 一个写用例就是一次 tx.Do 调用（ADR-0003）。
type Service struct {
	tx           db.TransactionManager
	skus         SKURepository
	inventories  InventoryRepository
	reservations ReservationRepository
}

// NewService 构造用例服务。所有参数必须非 nil——缺失依赖应当在装配期暴露，
// 而不是等到第一个请求进来才 panic。
//
// reservations 只在"在调用方事务内创建预占"（供下单用例）时使用；SKU 管理
// 端点不需要它，但它在进程生命周期内是固定依赖，因此放在构造参数里而不是
// 做成可选。
func NewService(tx db.TransactionManager, skus SKURepository,
	inventories InventoryRepository, reservations ReservationRepository) *Service {
	if tx == nil {
		panic("inventory/app: TransactionManager 不能为空")
	}
	if skus == nil {
		panic("inventory/app: SKURepository 不能为空")
	}
	if inventories == nil {
		panic("inventory/app: InventoryRepository 不能为空")
	}
	if reservations == nil {
		panic("inventory/app: ReservationRepository 不能为空")
	}
	return &Service{tx: tx, skus: skus, inventories: inventories, reservations: reservations}
}

// SKUView 是 SKU 与库存的组合视图，对应 API 的 SKU 响应。
//
// 领域对象不导出字段，也没有 JSON 标签；把展示结构放在 app 层而不是让 http
// 直接拼装，是为了让"哪些字段对外可见"成为一次明确的决定。
type SKUView struct {
	SKUID      string
	Name       string
	PriceCents int64
	Status     domain.SKUStatus
	Version    int64
	// 库存字段；SKU 尚未建库存行时为零值（StateKnown=false）。
	Available  int64
	Reserved   int64
	Sold       int64
	StateKnown bool
}

// CreateSKUInput 是创建 SKU 的输入。
type CreateSKUInput struct {
	SKUID        string
	Name         string
	PriceCents   int64
	InitialStock int64
}

// CreateSKU 创建 SKU 及其初始库存。
//
// SKU 与 inventories 必须同事务：只建 SKU 会让商品"存在但不可售"，而只建库存
// 会违反外键。中途失败时两者一起回滚，不会留下半成品。
func (s *Service) CreateSKU(ctx context.Context, in CreateSKUInput) (*SKUView, error) {
	if in.InitialStock < 0 {
		// 领域层用 ErrInvalidQuantity 表达"数量非法"；这里提前给出更贴切的
		// 参数错误，避免把"初始库存为负"报成"数量必须为正"这种含糊文案。
		return nil, fmt.Errorf("%w: initial_stock 不能为负", domain.ErrInvalidQuantity)
	}

	sku, err := domain.NewSKU(in.SKUID, in.Name, in.PriceCents)
	if err != nil {
		return nil, err
	}
	inv, err := domain.NewInventory(in.SKUID, in.InitialStock)
	if err != nil {
		return nil, err
	}

	var view *SKUView
	err = s.tx.Do(ctx, func(ctx context.Context, tx db.Tx) error {
		if err := s.skus.Create(ctx, tx, sku); err != nil {
			return err
		}
		if err := s.inventories.Create(ctx, tx, &inv); err != nil {
			return err
		}
		view = &SKUView{
			SKUID:      sku.ID(),
			Name:       sku.Name(),
			PriceCents: sku.PriceCents(),
			Status:     sku.Status(),
			Version:    sku.Version(),
			Available:  inv.Available(),
			Reserved:   inv.Reserved(),
			Sold:       inv.Sold(),
			StateKnown: true,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// GetSKU 读取 SKU 与当前库存。
//
// 库存行可能不存在（历史数据或人工清理），此时仍然返回 SKU，
// 只是 StateKnown=false：这与"SKU 不存在"是两件事，后者返回 ErrSKUNotFound。
func (s *Service) GetSKU(ctx context.Context, skuID string) (*SKUView, error) {
	var view *SKUView
	// 只读查询也走事务：SKU 与库存要读同一快照，否则可能出现
	// "读到改价后的 SKU + 改价前扣的库存"这种组合。
	err := s.tx.Do(ctx, func(ctx context.Context, tx db.Tx) error {
		sku, err := s.skus.Get(ctx, tx, skuID)
		if err != nil {
			return err
		}
		view = &SKUView{
			SKUID:      sku.ID(),
			Name:       sku.Name(),
			PriceCents: sku.PriceCents(),
			Status:     sku.Status(),
			Version:    sku.Version(),
		}

		inv, err := s.inventories.Get(ctx, tx, skuID)
		if err != nil {
			if errors.Is(err, ErrInventoryNotFound) {
				return nil // SKU 存在、库存行缺失：如实返回，不当作 404
			}
			return err
		}
		view.Available = inv.Available()
		view.Reserved = inv.Reserved()
		view.Sold = inv.Sold()
		view.StateKnown = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// UpdateSKUInput 是更新 SKU 的输入。nil 表示"不修改该字段"。
//
// 用指针而不是"空值表示不修改"：价格 0 是合法值（赠品），名称空串可能在未来
// 成为合法输入，用哨兵值表达"未提供"会把这些合法值误判为修改。
type UpdateSKUInput struct {
	SKUID           string
	ExpectedVersion int64
	Name            *string
	PriceCents      *int64
	Status          *domain.SKUStatus
}

// UpdateSKU 更新 SKU 的名称、价格或状态。
//
// 读-改-写三步在同一事务内，并用 version 做乐观锁：运营两次并发改价时，
// 后到者得到 ErrSKUVersionConflict 而不是静默覆盖前者的意图。
//
// 改价不影响历史订单：订单明细保存的是下单时的单价快照（OrderItem），
// 这里只改 skus.price_cents，没有任何路径会回写已有 order_items。
func (s *Service) UpdateSKU(ctx context.Context, in UpdateSKUInput) (*SKUView, error) {
	if in.ExpectedVersion < 0 {
		return nil, fmt.Errorf("%w: version 不能为负", ErrInvalidInput)
	}

	var view *SKUView
	err := s.tx.Do(ctx, func(ctx context.Context, tx db.Tx) error {
		sku, err := s.skus.Get(ctx, tx, in.SKUID)
		if err != nil {
			return err
		}
		// 版本不匹配时不进入修改路径：先报告冲突，避免调用方以为改动生效。
		if sku.Version() != in.ExpectedVersion {
			return fmt.Errorf("%w: sku_id=%s current=%d expected=%d",
				ErrSKUVersionConflict, in.SKUID, sku.Version(), in.ExpectedVersion)
		}

		// 依次应用，任一失败则整个事务回滚——不会出现"名称改了但价格没改"。
		if in.Name != nil {
			if err := sku.Rename(*in.Name); err != nil {
				return err
			}
		}
		if in.PriceCents != nil {
			if err := sku.UpdatePrice(*in.PriceCents); err != nil {
				return err
			}
		}
		if in.Status != nil {
			switch *in.Status {
			case domain.SKUActive:
				if err := sku.Activate(); err != nil {
					return err
				}
			case domain.SKUInactive:
				if err := sku.Deactivate(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%w: 未知状态 %q", ErrInvalidInput, *in.Status)
			}
		}

		if err := s.skus.Update(ctx, tx, sku, in.ExpectedVersion); err != nil {
			return err
		}

		view = &SKUView{
			SKUID:      sku.ID(),
			Name:       sku.Name(),
			PriceCents: sku.PriceCents(),
			Status:     sku.Status(),
			Version:    sku.Version(),
		}
		// 库存没有变化，但响应里带上它可以让客户端少一次查询。
		if inv, err := s.inventories.Get(ctx, tx, in.SKUID); err == nil {
			view.Available = inv.Available()
			view.Reserved = inv.Reserved()
			view.Sold = inv.Sold()
			view.StateKnown = true
		} else if !errors.Is(err, ErrInventoryNotFound) {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}
