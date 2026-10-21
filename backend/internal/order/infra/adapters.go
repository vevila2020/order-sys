package infra

import (
	"context"
	"errors"

	inventoryapp "github.com/vevila2020/order-sys/internal/inventory/app"
	inventorydomain "github.com/vevila2020/order-sys/internal/inventory/domain"
	orderapp "github.com/vevila2020/order-sys/internal/order/app"
	paymentapp "github.com/vevila2020/order-sys/internal/payment/app"
	"github.com/vevila2020/order-sys/internal/platform/db"
)

// 本文件是跨模块适配器：把 inventory / payment 的应用服务接到 order 的端口上。
//
// 为什么适配器放 order/infra 而不是 order/app：
//   - app 只允许依赖其他模块的 app 层（archtest 的 otherModuleInternals 规则）
//     以及本模块的 domain；把适配器放在 app 里会让 app 直接持有另一个模块的
//     具体实现，端口的意义就没了。
//   - infra 的职责本来就是"与外部世界打交道"，跨模块调用也属于这一类。
//
// 适配器同时负责错误翻译：order 的 app 层只认识自己的哨兵错误，不知道
// inventory 的 ErrInsufficientStock。翻译集中在这里，HTTP 映射才能稳定。

// InventoryAdapter 把 inventory 的应用服务适配为 order 的三个端口
// （SKUReader / InventoryReserver / ReservationWriter）。
type InventoryAdapter struct {
	svc *inventoryapp.Service
}

// NewInventoryAdapter 构造适配器。
func NewInventoryAdapter(svc *inventoryapp.Service) *InventoryAdapter {
	if svc == nil {
		panic("order/infra: inventory 服务不能为空")
	}
	return &InventoryAdapter{svc: svc}
}

// ReadForOrder 实现 orderapp.SKUReader。
func (a *InventoryAdapter) ReadForOrder(ctx context.Context, tx db.Tx, skuID string) (orderapp.SKUPrice, error) {
	price, err := a.svc.ReadForOrder(ctx, tx, skuID)
	if err != nil {
		return orderapp.SKUPrice{}, translateInventoryError(err)
	}
	return orderapp.SKUPrice{
		SKUID:      price.SKUID,
		PriceCents: price.PriceCents,
		Available:  price.Available,
	}, nil
}

// Reserve 实现 orderapp.InventoryReserver。
func (a *InventoryAdapter) Reserve(ctx context.Context, tx db.Tx, skuID string, qty int64) error {
	return translateInventoryError(a.svc.ReserveIn(ctx, tx, skuID, qty))
}

// Release 实现 orderapp.InventoryReserver。
//
// M1 的下单路径不调用它：事务回滚会自动撤销同一事务内的扣减。保留实现是为了
// M3 的异步流程（预占成功但订单创建走异步时，需要显式归还）。
func (a *InventoryAdapter) Release(context.Context, db.Tx, string, int64) error {
	return errors.New("order/infra: Release 在 M1 未启用")
}

// CreateReservation 实现 orderapp.ReservationWriter。
func (a *InventoryAdapter) CreateReservation(ctx context.Context, tx db.Tx, reservationID, orderID string,
	lines []orderapp.ReservationLine) error {
	converted := make([]inventoryapp.ReservationLine, 0, len(lines))
	for _, line := range lines {
		converted = append(converted, inventoryapp.ReservationLine{
			SKUID:    line.SKUID,
			Quantity: line.Quantity,
		})
	}
	return translateInventoryError(a.svc.CreateReservation(ctx, tx, reservationID, orderID, converted))
}

// translateInventoryError 把 inventory 的错误翻译为 order 的哨兵错误。
//
// 只翻译调用方需要区分的那一个：库存不足要映射成 409 INSUFFICIENT_STOCK，
// 而"SKU 不存在/已停用"是 400/404 类问题，语义不同。其余错误原样透传，
// 由 order 的 HTTP 层折叠成 INTERNAL——它们属于真正的故障，不该伪装成业务结论。
func translateInventoryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, inventorydomain.ErrInsufficientStock) {
		return orderapp.ErrInsufficientStock
	}
	return err
}

// PaymentAdapter 把 payment 的应用服务适配为 order 的 PaymentCreator。
type PaymentAdapter struct {
	svc *paymentapp.Service
}

// NewPaymentAdapter 构造适配器。
func NewPaymentAdapter(svc *paymentapp.Service) *PaymentAdapter {
	if svc == nil {
		panic("order/infra: payment 服务不能为空")
	}
	return &PaymentAdapter{svc: svc}
}

// CreatePayment 实现 orderapp.PaymentCreator。
func (a *PaymentAdapter) CreatePayment(ctx context.Context, tx db.Tx, paymentID, orderID string, amountCents int64) error {
	return a.svc.CreatePayment(ctx, tx, paymentID, orderID, amountCents)
}

// 编译期断言：适配器必须满足端口。
var (
	_ orderapp.SKUReader         = (*InventoryAdapter)(nil)
	_ orderapp.InventoryReserver = (*InventoryAdapter)(nil)
	_ orderapp.ReservationWriter = (*InventoryAdapter)(nil)
	_ orderapp.PaymentCreator    = (*PaymentAdapter)(nil)
)
