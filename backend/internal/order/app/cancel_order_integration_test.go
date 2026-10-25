//go:build integration

package app_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	orderapp "github.com/vevila2020/order-sys/internal/order/app"
	"github.com/vevila2020/order-sys/internal/order/domain"
	"github.com/vevila2020/order-sys/internal/platform/testdb"
)

// T14 的验收场景：查询归属、取消的幂等与冲突、取消与支付并发只有一个终态。

// placeOrder 是测试里的常用前置：建 SKU、下单、返回订单号。
func placeOrder(t *testing.T, h *harness, userID, key, skuID string, qty int64) *orderapp.OrderView {
	t.Helper()
	view, err := h.svc.PlaceOrder(context.Background(), inputFor(userID, key, "hash-"+key,
		orderapp.PlaceOrderItem{SKUID: skuID, Quantity: qty}))
	if err != nil {
		t.Fatalf("下单失败: %v", err)
	}
	return view
}

// 查询返回状态、金额、明细、支付信息与关闭原因。
func TestGetOrderDetail(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 1500, 10)

	placed := placeOrder(t, h, "u1", "key-get00001", "sku-a", 2)

	detail, err := h.svc.GetOrder(ctx, "u1", placed.OrderID, false)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if detail.Status != domain.StatusPendingPayment || detail.TotalCents != 3000 {
		t.Fatalf("detail = %+v", detail)
	}
	if len(detail.Items) != 1 || detail.Items[0].Quantity != 2 {
		t.Fatalf("明细 = %+v", detail.Items)
	}
	if detail.PaymentID != placed.PaymentID || detail.PaymentStatus != "PENDING" {
		t.Fatalf("支付信息 = %s/%s", detail.PaymentID, detail.PaymentStatus)
	}
	if detail.CloseReason != "" {
		t.Fatalf("非终态不应有 close_reason: %q", detail.CloseReason)
	}
}

// 他人订单返回 ErrOrderForbidden（HTTP 层映射为 404，不泄露存在性）。
func TestGetOrderRejectsOtherUser(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 100, 10)

	placed := placeOrder(t, h, "u1", "key-other001", "sku-a", 1)

	_, err := h.svc.GetOrder(ctx, "u2", placed.OrderID, false)
	if !errors.Is(err, orderapp.ErrOrderForbidden) {
		t.Fatalf("err = %v, want ErrOrderForbidden", err)
	}

	// 运营可以查他人订单。
	if _, err := h.svc.GetOrder(ctx, "op1", placed.OrderID, true); err != nil {
		t.Fatalf("运营查询失败: %v", err)
	}
}

// 取消：订单变 CANCELLED、预占释放回 available、支付单 CANCELLED、写事件。
func TestCancelOrderReleasesEverything(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 1500, 10)

	placed := placeOrder(t, h, "u1", "key-cancel01", "sku-a", 3)

	// 下单后：available=7 reserved=3。
	if avail, resv, _ := h.stock(t, "sku-a"); avail != 7 || resv != 3 {
		t.Fatalf("取消前库存 = %d/%d, want 7/3", avail, resv)
	}

	if err := h.svc.CancelOrder(ctx, "u1", placed.OrderID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}

	// 取消后：available 回到 10，reserved 归零。
	if avail, resv, sold := h.stock(t, "sku-a"); avail != 10 || resv != 0 || sold != 0 {
		t.Fatalf("取消后库存 = %d/%d/%d, want 10/0/0", avail, resv, sold)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM reservations WHERE order_id = ? AND status = 'RELEASED'`, placed.OrderID); n != 1 {
		t.Fatalf("预占状态未变为 RELEASED: %d", n)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM payments WHERE order_id = ? AND status = 'CANCELLED'`, placed.OrderID); n != 1 {
		t.Fatalf("支付单未取消: %d", n)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = ? AND event_type = 'order.cancelled'`, placed.OrderID); n != 1 {
		t.Fatalf("order.cancelled 事件数 = %d", n)
	}

	detail, err := h.svc.GetOrder(ctx, "u1", placed.OrderID, false)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if detail.Status != domain.StatusCancelled || detail.CloseReason != orderapp.CloseReasonUserCancelled {
		t.Fatalf("detail = %s/%q", detail.Status, detail.CloseReason)
	}
}

// 重复取消返回成功且不产生副作用：库存不再变化，事件不重复写。
func TestCancelOrderIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 100, 10)

	placed := placeOrder(t, h, "u1", "key-cancel02", "sku-a", 2)

	if err := h.svc.CancelOrder(ctx, "u1", placed.OrderID); err != nil {
		t.Fatalf("首次取消: %v", err)
	}
	if err := h.svc.CancelOrder(ctx, "u1", placed.OrderID); err != nil {
		t.Fatalf("重复取消应成功: %v", err)
	}

	// 关键：库存不能被归还两次。
	if avail, resv, _ := h.stock(t, "sku-a"); avail != 10 || resv != 0 {
		t.Fatalf("重复取消后库存 = %d/%d, want 10/0", avail, resv)
	}
	if n := h.count(t, `SELECT COUNT(*) FROM outbox_events WHERE aggregate_id = ? AND event_type = 'order.cancelled'`, placed.OrderID); n != 1 {
		t.Fatalf("事件被重复写入: %d", n)
	}
}

// 已完成订单取消返回 409：状态机不允许，而不是并发冲突。
func TestCancelCompletedOrderConflicts(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 100, 10)

	placed := placeOrder(t, h, "u1", "key-cancel03", "sku-a", 1)
	// 直接改库模拟支付已完成（T15 会通过回调走到这个状态）。
	if err := h.gdb.Exec(`UPDATE orders SET status = 'COMPLETED' WHERE order_id = ?`, placed.OrderID).Error; err != nil {
		t.Fatalf("推进状态: %v", err)
	}

	err := h.svc.CancelOrder(ctx, "u1", placed.OrderID)
	if !errors.Is(err, domain.ErrInvalidStateTransition) {
		t.Fatalf("err = %v, want ErrInvalidStateTransition", err)
	}
}

// 他人订单取消返回 ErrOrderForbidden。
func TestCancelOrderRejectsOtherUser(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 100, 10)

	placed := placeOrder(t, h, "u1", "key-cancel04", "sku-a", 1)

	err := h.svc.CancelOrder(ctx, "u2", placed.OrderID)
	if !errors.Is(err, orderapp.ErrOrderForbidden) {
		t.Fatalf("err = %v, want ErrOrderForbidden", err)
	}
	// 订单状态必须不变。
	if n := h.count(t, `SELECT COUNT(*) FROM orders WHERE order_id = ? AND status = 'PENDING_PAYMENT'`, placed.OrderID); n != 1 {
		t.Fatalf("他人取消改变了订单状态")
	}
}

// 取消与"支付成功"并发：只有一个终态，库存最多释放一次。
//
// 用直接更新订单状态模拟支付成功（T15 才引入完整回调）。两个 goroutine 同时
// 争抢同一行的条件更新，命中者决定终态。
func TestCancelRacesWithPaymentSuccess(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	h := newHarness(t, tdb)
	h.seedSKU(t, "sku-a", 100, 10)

	placed := placeOrder(t, h, "u1", "key-race0001", "sku-a", 2)

	var cancelOK, cancelConflict int64
	var wg sync.WaitGroup
	start := make(chan struct{})

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		// 模拟支付回调把订单推进到 COMPLETED。
		err := h.gdb.Exec(`UPDATE orders SET status = 'COMPLETED' WHERE order_id = ? AND status = 'PENDING_PAYMENT'`,
			placed.OrderID).Error
		if err != nil {
			t.Errorf("模拟支付成功: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		err := h.svc.CancelOrder(ctx, "u1", placed.OrderID)
		switch {
		case err == nil:
			atomic.AddInt64(&cancelOK, 1)
		case errors.Is(err, domain.ErrInvalidStateTransition), errors.Is(err, orderapp.ErrOrderStateConflict):
			atomic.AddInt64(&cancelConflict, 1)
		default:
			t.Errorf("取消返回意外错误: %v", err)
		}
	}()
	close(start)
	wg.Wait()

	// 无论谁赢，订单只能有一个终态。
	var status string
	if err := h.gdb.Raw(`SELECT status FROM orders WHERE order_id = ?`, placed.OrderID).Scan(&status).Error; err != nil {
		t.Fatalf("读取状态: %v", err)
	}
	if status != "COMPLETED" && status != "CANCELLED" {
		t.Fatalf("订单处于非终态: %s", status)
	}

	// 库存一致性：终态为取消则库存已归还，终态为完成则预占仍在。
	avail, resv, _ := h.stock(t, "sku-a")
	switch status {
	case "CANCELLED":
		if avail != 10 || resv != 0 {
			t.Fatalf("取消后库存 = %d/%d, want 10/0", avail, resv)
		}
	case "COMPLETED":
		if avail != 8 || resv != 2 {
			t.Fatalf("完成后库存 = %d/%d, want 8/2", avail, resv)
		}
	}
	// 取消方只能在"订单确实已被支付推进"时才报冲突，否则就是成功。
	if cancelOK+cancelConflict != 1 {
		t.Fatalf("取消结果计数 = ok:%d conflict:%d", cancelOK, cancelConflict)
	}
	if status == "COMPLETED" && cancelOK != 0 {
		t.Fatal("订单最终为 COMPLETED，但取消报告了成功")
	}
}
