package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/vevila2020/order-sys/internal/order/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/idempotency"
	"github.com/vevila2020/order-sys/internal/platform/outbox"
)

// ScopePlaceOrder 是下单接口的幂等作用域。
//
// 作用域进唯一索引 (scope, user_id, key)：后续接口复用同一张表时靠它隔离，
// 不新建表（ADR-0006 第 1 节）。
const ScopePlaceOrder = "order.create"

// PlaceOrderInput 是下单用例的输入。
type PlaceOrderInput struct {
	// UserID 来自 JWT sub，绝不取自请求体（ADR-0007）。
	UserID string
	// IdempotencyKey 是已通过格式校验的请求头值。
	IdempotencyKey string
	// RequestHash 是原始请求体的指纹（idempotency.Fingerprint 的结果）。
	RequestHash string
	// Items 是请求中的商品行，顺序即客户端提交顺序。
	Items []PlaceOrderItem
}

// PlaceOrderItem 是请求中的一行。
type PlaceOrderItem struct {
	SKUID    string
	Quantity int64
}

// OrderView 是下单结果。Replayed 表示本次是幂等重放（HTTP 层据此返回 200 而非 201）。
//
// 带 json 标签是因为这个结构会**原样序列化进幂等快照并长期留在库里**：
// 字段名一旦发布就不能随意改，否则历史快照无法解析。它同时是重放的输入，
// 因此 Replayed 用 json:"-" 排除——重放与否由本次请求决定，不能从快照里读。
type OrderView struct {
	OrderID       string             `json:"order_id"`
	Status        domain.OrderStatus `json:"status"`
	TotalCents    int64              `json:"total_cents"`
	Currency      string             `json:"currency"`
	Items         []OrderItemView    `json:"items"`
	PaymentID     string             `json:"payment_id"`
	PaymentStatus string             `json:"payment_status"`
	CreatedAt     time.Time          `json:"created_at"`
	Replayed      bool               `json:"-"`
}

// OrderItemView 是订单明细的对外表示。
type OrderItemView struct {
	SKUID          string `json:"sku_id"`
	Quantity       int64  `json:"quantity"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	AmountCents    int64  `json:"amount_cents"`
}

// Service 编排订单用例。
//
// 依赖按用途分组：下单用 skus/inventory/reservations/payments 的写端口；
// 查询与取消额外用 paymentRead 与 releaser。不拆成两个构造函数——它们都是
// 进程级固定依赖，拆开只会让 main 的装配变复杂。
type Service struct {
	tx           db.TransactionManager
	orders       OrderRepository
	idempotency  IdempotencyStore
	outbox       OutboxWriter
	skus         SKUReader
	inventory    InventoryReserver
	reservations ReservationWriter
	payments     PaymentCreator
	paymentRead  PaymentReader
	releaser     ReservationReleaser
	now          func() time.Time
}

// NewService 构造订单用例服务。
//
// 依赖全部为端口，由 main 注入。now 可替换，用于让集成测试断言时间字段
// 而不依赖真实时钟。
func NewService(
	tx db.TransactionManager,
	orders OrderRepository,
	idempotencyStore IdempotencyStore,
	outboxWriter OutboxWriter,
	skus SKUReader,
	inventory InventoryReserver,
	reservations ReservationWriter,
	payments PaymentCreator,
	paymentReader PaymentReader,
	releaser ReservationReleaser,
) *Service {
	if tx == nil || orders == nil || idempotencyStore == nil || outboxWriter == nil ||
		skus == nil || inventory == nil || reservations == nil || payments == nil ||
		paymentReader == nil || releaser == nil {
		// 装配期失败优于运行期 nil 解引用：后者会在第一个请求进来时才暴露，
		// 且错误信息指向用例内部而不是装配代码。
		panic("order/app: 依赖不能为空")
	}
	return &Service{
		tx:           tx,
		orders:       orders,
		idempotency:  idempotencyStore,
		outbox:       outboxWriter,
		skus:         skus,
		inventory:    inventory,
		reservations: reservations,
		payments:     payments,
		paymentRead:  paymentReader,
		releaser:     releaser,
		now:          time.Now,
	}
}

// PlaceOrder 在一个事务内完成幂等占位、价格快照、库存预占、订单、支付单与 Outbox。
//
// 事务内顺序是固定的，不能调换：
//
//  1. 占用幂等键 —— 并发同键的请求在这里排队（唯一索引），失败立即结束；
//  2. 按 sku_id 升序读取价格 —— 固定顺序，避免与其他事务交叉加锁；
//  3. 按同一顺序预占库存 —— 任一行不足则整单回滚，所有已扣库存一并撤销；
//  4. 写预占记录、订单与明细；
//  5. 写支付单；
//  6. 写 Outbox 事件（order.created / inventory.reserved）；
//  7. 写幂等快照并标为 COMPLETED。
//
// 为什么先占幂等键再读价格：如果先读价格，一个注定冲突的重放请求会先消耗
// 若干数据库往返，且并发同键的两个请求会同时读价格——它们最终只有一个能
// 写入，另一个的读取完全浪费。先占键把"谁是第一个"提前到事务最开始。
//
// 事务内禁止任何网络调用（ADR-0003 第 6 节）：这里只有本地 SQL。
func (s *Service) PlaceOrder(ctx context.Context, in PlaceOrderInput) (*OrderView, error) {
	if in.UserID == "" {
		return nil, domain.ErrInvalidUserID
	}
	if len(in.Items) == 0 {
		return nil, domain.ErrEmptyOrder
	}
	if in.IdempotencyKey == "" || in.RequestHash == "" {
		return nil, fmt.Errorf("%w: 幂等键与指纹由 HTTP 层提供", ErrInvalidInput)
	}

	// 下单时间在事务外取一次：TransactionManager 可能重试整个事务，若在闭包内
	// 取 now，重试后的 created_at 会与前一次不同，客户端看到的时间会跳变。
	// 标识符同样在事务外生成：重试时若重新生成，同一笔下单在多次尝试中会拿到
	// 不同的 order_id / payment_id，幂等快照与实际落库的行就对不上了。
	createdAt := s.now().UTC()
	orderID := uuid.NewString()
	reservationID := uuid.NewString()
	paymentID := uuid.NewString()

	var view *OrderView
	err := s.tx.Do(ctx, func(ctx context.Context, tx db.Tx) error {
		rec := idempotency.Record{
			Scope:       ScopePlaceOrder,
			UserID:      in.UserID,
			Key:         in.IdempotencyKey,
			RequestHash: in.RequestHash,
		}
		existing, err := s.idempotency.Begin(ctx, tx, rec)
		if err != nil {
			return err
		}
		if existing != nil {
			// 重放分支：只读，不执行业务写入，也不重写快照（ADR-0006 第 6 节）。
			replayed, err := replayView(existing.Snapshot)
			if err != nil {
				return err
			}
			view = replayed
			return nil
		}

		// 读取价格并按 sku_id 升序处理。排序在这里完成而不是信任请求顺序：
		// 客户端提交顺序任意，固定全序才能降低交叉死锁概率（ADR-0003 第 3 节）。
		items, err := s.buildItems(ctx, tx, in.Items)
		if err != nil {
			return err
		}

		order, err := domain.NewOrder(orderID, in.UserID, items, createdAt)
		if err != nil {
			return err
		}

		// 预占库存：逐行条件更新。任一行返回库存不足，事务回滚，之前扣掉的
		// 数量随之撤销——不会出现"部分扣减"。
		lines := make([]ReservationLine, 0, len(items))
		for _, it := range items {
			if err := s.inventory.Reserve(ctx, tx, it.SKUID(), it.Quantity()); err != nil {
				return err
			}
			lines = append(lines, ReservationLine{SKUID: it.SKUID(), Quantity: it.Quantity()})
		}

		if err := s.reservations.CreateReservation(ctx, tx, reservationID, orderID, lines); err != nil {
			return err
		}
		if err := s.orders.Create(ctx, tx, order); err != nil {
			return err
		}
		if err := s.payments.CreatePayment(ctx, tx, paymentID, orderID, order.TotalCents()); err != nil {
			return err
		}

		// Outbox 与业务同事务：事务回滚时事件一并消失，不会产生幽灵事件。
		if err := s.writeEvents(ctx, tx, order, lines); err != nil {
			return err
		}

		// 快照只存 data 部分：request_id 属于单次请求，重放时必须重新生成。
		snapshot, err := json.Marshal(OrderView{
			OrderID:       order.ID(),
			Status:        order.Status(),
			TotalCents:    order.TotalCents(),
			Currency:      order.Currency(),
			Items:         toItemViews(order.Items()),
			PaymentID:     paymentID,
			PaymentStatus: "PENDING",
			CreatedAt:     order.CreatedAt(),
		})
		if err != nil {
			return fmt.Errorf("编码幂等快照: %w", err)
		}
		if err := s.idempotency.Complete(ctx, tx, rec, snapshot); err != nil {
			return err
		}

		view = &OrderView{
			OrderID:       order.ID(),
			Status:        order.Status(),
			TotalCents:    order.TotalCents(),
			Currency:      order.Currency(),
			Items:         toItemViews(order.Items()),
			PaymentID:     paymentID,
			PaymentStatus: "PENDING",
			CreatedAt:     order.CreatedAt(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// buildItems 读取价格并构造订单明细，按 sku_id 升序返回。
//
// 价格快照取自事务内读取的 SKU：并发改价时，订单要么看到旧价要么看到新价，
// 不会出现"读到旧价但按新价扣款"的中间态。
func (s *Service) buildItems(ctx context.Context, tx db.Tx, requested []PlaceOrderItem) ([]domain.OrderItem, error) {
	if len(requested) == 0 {
		return nil, domain.ErrEmptyOrder
	}

	// 排序前先复制：调用方的切片可能被复用，原地排序会改变请求本身的顺序。
	sorted := make([]PlaceOrderItem, len(requested))
	copy(sorted, requested)
	sortPlaceOrderItems(sorted)

	items := make([]domain.OrderItem, 0, len(sorted))
	for _, req := range sorted {
		price, err := s.skus.ReadForOrder(ctx, tx, req.SKUID)
		if err != nil {
			return nil, err
		}
		// 快速失败：库存不足在 Reserve 里也会被发现，但提前判断能避免
		// 前面若干行已经扣减后才失败——虽然事务会回滚，但会多占一会儿行锁。
		if price.Available < req.Quantity {
			return nil, fmt.Errorf("%w: sku_id=%s requested=%d available=%d",
				ErrInsufficientStock, req.SKUID, req.Quantity, price.Available)
		}
		item, err := domain.NewOrderItem(req.SKUID, req.Quantity, price.PriceCents)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// writeEvents 写入下单产生的两条 Outbox 事件。
//
// 事件负载只含对消费者有用的字段，不含用户隐私或价格明细之外的内部结构
// （需求 §9.6：事件负载不含敏感个人信息）。
func (s *Service) writeEvents(ctx context.Context, tx db.Tx, order *domain.Order, lines []ReservationLine) error {
	now := s.now().UTC()

	items := make([]map[string]any, 0, len(order.Items()))
	for _, it := range order.Items() {
		items = append(items, map[string]any{
			"sku_id":   it.SKUID(),
			"quantity": it.Quantity(),
		})
	}
	createdPayload, err := json.Marshal(map[string]any{
		"order_id":    order.ID(),
		"user_id":     order.UserID(),
		"status":      string(order.Status()),
		"total_cents": order.TotalCents(),
		"currency":    order.Currency(),
		"items":       items,
	})
	if err != nil {
		return fmt.Errorf("编码 order.created 负载: %w", err)
	}

	reservedItems := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		reservedItems = append(reservedItems, map[string]any{
			"sku_id":   line.SKUID,
			"quantity": line.Quantity,
		})
	}
	reservedPayload, err := json.Marshal(map[string]any{
		"order_id": order.ID(),
		"items":    reservedItems,
	})
	if err != nil {
		return fmt.Errorf("编码 inventory.reserved 负载: %w", err)
	}

	events := []outbox.Event{
		{
			EventID:       uuid.NewString(),
			EventType:     "order.created",
			AggregateType: "order",
			AggregateID:   order.ID(),
			OccurredAt:    now,
			Data:          createdPayload,
		},
		{
			EventID:       uuid.NewString(),
			EventType:     "inventory.reserved",
			AggregateType: "order",
			// 聚合仍是订单：分区键保证同一订单的事件有序（需求 §9.6）。
			AggregateID: order.ID(),
			OccurredAt:  now,
			Data:        reservedPayload,
		},
	}
	for _, ev := range events {
		if err := s.outbox.Write(ctx, tx, ev); err != nil {
			return err
		}
	}
	return nil
}

// replayView 从幂等快照还原下单结果。
func replayView(snapshot []byte) (*OrderView, error) {
	var view OrderView
	if err := json.Unmarshal(snapshot, &view); err != nil {
		return nil, fmt.Errorf("解析幂等快照: %w", err)
	}
	view.Replayed = true
	return &view, nil
}

// toItemViews 把领域明细转为视图。
func toItemViews(items []domain.OrderItem) []OrderItemView {
	out := make([]OrderItemView, 0, len(items))
	for _, it := range items {
		out = append(out, OrderItemView{
			SKUID:          it.SKUID(),
			Quantity:       it.Quantity(),
			UnitPriceCents: it.UnitPriceCents(),
			AmountCents:    it.AmountCents(),
		})
	}
	return out
}

// sortPlaceOrderItems 按 sku_id 字节序升序排序。
//
// 用插入排序而不是 sort.Slice：订单行数通常是个位数，插入排序无分配、
// 无反射，且对已有序输入是 O(n)。更重要的是它稳定——重复 SKU 的检测
// 不依赖顺序，但稳定排序让行为在测试中完全可复现。
func sortPlaceOrderItems(items []PlaceOrderItem) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].SKUID < items[j-1].SKUID; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// ErrInvalidInput 表示用例层参数不合法（不满足领域规则的前提条件）。
var ErrInvalidInput = errors.New("order: 请求参数无效")

// ErrInsufficientStock 表示某个 SKU 的可售量不足。
//
// 为什么在 order/app 再定义一次而不是复用 inventory/domain 的哨兵：
// 跨模块只允许 app→app 的依赖（ADR-0002），order 不得 import inventory/domain。
// main 在装配时注入的适配器负责把 inventory 的错误翻译成本包的哨兵，
// 这样 order 的 HTTP 层只认识自己的错误集，也就能稳定映射错误码。
var ErrInsufficientStock = errors.New("order: 库存不足")
