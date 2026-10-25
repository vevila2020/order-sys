//go:build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	inventoryapp "github.com/vevila2020/order-sys/internal/inventory/app"
	inventoryinfra "github.com/vevila2020/order-sys/internal/inventory/infra"
	orderapp "github.com/vevila2020/order-sys/internal/order/app"
	orderhttp "github.com/vevila2020/order-sys/internal/order/http"
	orderinfra "github.com/vevila2020/order-sys/internal/order/infra"
	paymentapp "github.com/vevila2020/order-sys/internal/payment/app"
	paymentinfra "github.com/vevila2020/order-sys/internal/payment/infra"
	"github.com/vevila2020/order-sys/internal/platform/authx"
	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/idempotency"
	"github.com/vevila2020/order-sys/internal/platform/outbox"
	"github.com/vevila2020/order-sys/internal/platform/testdb"
)

// 这些用例走完整 HTTP → app → infra 链路，用中间件注入身份而不是真发 JWT：
// 认证本身由 authx 的单测覆盖，这里关心的是状态码、错误码与响应形状。

// withPrincipal 注入调用者身份，替代 RequireAuth。
//
// 用中间件而不是伪造 header：handler 从 context 取身份，与生产的取值路径一致。
func withPrincipal(userID string, role authx.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		p := authx.Principal{UserID: userID, Role: role}
		c.Request = c.Request.WithContext(authx.WithPrincipal(c.Request.Context(), p))
		c.Next()
	}
}

// newRouter 组装 engine + 路由。
func newRouter(t *testing.T, tdb *testdb.DB) (*gin.Engine, *gorm.DB) {
	t.Helper()

	gdb, err := db.Open(context.Background(), configx.MySQLConfig{
		DSN:          tdb.DSN,
		MaxOpenConns: 5,
		MaxIdleConns: 2,
	})
	if err != nil {
		t.Fatalf("打开数据库: %v", err)
	}
	t.Cleanup(func() { _ = db.Close(gdb) })

	tm := db.NewTransactionManager(gdb)
	inventorySvc := inventoryapp.NewService(tm,
		inventoryinfra.NewSKURepository(),
		inventoryinfra.NewInventoryRepository(),
		inventoryinfra.NewReservationRepository(),
	)
	paymentSvc := paymentapp.NewService(paymentinfra.NewPaymentRepository())
	invAdapter := orderinfra.NewInventoryAdapter(inventorySvc)
	payAdapter := orderinfra.NewPaymentAdapter(paymentSvc)

	svc := orderapp.NewService(
		tm,
		orderinfra.NewOrderRepository(),
		idempotency.NewStore(time.Hour),
		outbox.NewWriter(),
		invAdapter, invAdapter, invAdapter,
		payAdapter, payAdapter,
		invAdapter,
	)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/api/v1", withPrincipal("u1", authx.RoleBuyer))
	orderhttp.Register(group, orderhttp.NewHandler(svc))
	return engine, gdb
}

// doJSON 发送请求并解析响应体。
func doJSON(t *testing.T, engine *gin.Engine, method, path string, body any, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	var parsed map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("响应不是合法 JSON: %v (%s)", err, rec.Body.String())
		}
	}
	return rec, parsed
}

func dataOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 data: %v", body)
	}
	return data
}

func errorCodeOf(t *testing.T, body map[string]any) string {
	t.Helper()
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 error: %v", body)
	}
	code, _ := e["code"].(string)
	return code
}

// seedSKU 建立可下单的 SKU 与库存。
func seedSKU(t *testing.T, gdb *gorm.DB, skuID string, price, stock int64) {
	t.Helper()
	if err := gdb.Exec(
		`INSERT INTO skus (sku_id, name, price_cents, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'ACTIVE', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`, skuID, skuID, price).Error; err != nil {
		t.Fatalf("插入 SKU: %v", err)
	}
	if err := gdb.Exec(
		`INSERT INTO inventories (sku_id, total, available, allocated, reserved, sold, version, updated_at)
		 VALUES (?, ?, ?, 0, 0, 0, 0, UTC_TIMESTAMP(6))`, skuID, stock, stock).Error; err != nil {
		t.Fatalf("插入库存: %v", err)
	}
}

// place 走 HTTP 下单，返回订单号与支付单号。
func place(t *testing.T, engine *gin.Engine, key string) (string, string) {
	t.Helper()
	rec, body := doJSON(t, engine, http.MethodPost, "/api/v1/orders",
		map[string]any{"items": []map[string]any{{"sku_id": "sku-a", "quantity": 2}}},
		map[string]string{orderhttp.IdempotencyKeyHeader: key})
	if rec.Code != http.StatusCreated {
		t.Fatalf("下单 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, body)
	return data["order_id"].(string), data["payment_id"].(string)
}

// 下单 201，带 Idempotency-Key；重放 200。
func TestPlaceOrderHTTP(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, gdb := newRouter(t, tdb)
	seedSKU(t, gdb, "sku-a", 1500, 10)

	orderID, paymentID := place(t, engine, "key-http00001")
	if orderID == "" || paymentID == "" {
		t.Fatal("响应缺少 order_id 或 payment_id")
	}

	// 同键同体重放：200，同一订单。
	rec, body := doJSON(t, engine, http.MethodPost, "/api/v1/orders",
		map[string]any{"items": []map[string]any{{"sku_id": "sku-a", "quantity": 2}}},
		map[string]string{orderhttp.IdempotencyKeyHeader: "key-http00001"})
	if rec.Code != http.StatusOK {
		t.Fatalf("重放 status = %d, want 200", rec.Code)
	}
	if dataOf(t, body)["order_id"] != orderID {
		t.Fatalf("重放返回了不同订单")
	}
}

// 缺少 Idempotency-Key 返回 400 VALIDATION_FAILED。
func TestPlaceOrderRequiresIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, gdb := newRouter(t, tdb)
	seedSKU(t, gdb, "sku-a", 100, 10)

	rec, body := doJSON(t, engine, http.MethodPost, "/api/v1/orders",
		map[string]any{"items": []map[string]any{{"sku_id": "sku-a", "quantity": 1}}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, body); code != string(httpx.CodeValidationFailed) {
		t.Fatalf("code = %s", code)
	}
}

// 库存不足返回 409 INSUFFICIENT_STOCK。
func TestPlaceOrderInsufficientStockHTTP(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, gdb := newRouter(t, tdb)
	seedSKU(t, gdb, "sku-a", 100, 1)

	rec, body := doJSON(t, engine, http.MethodPost, "/api/v1/orders",
		map[string]any{"items": []map[string]any{{"sku_id": "sku-a", "quantity": 5}}},
		map[string]string{orderhttp.IdempotencyKeyHeader: "key-http00002"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, body); code != string(httpx.CodeInsufficientStock) {
		t.Fatalf("code = %s, want INSUFFICIENT_STOCK", code)
	}
}

// 查询返回完整详情；他人订单 404。
func TestGetOrderHTTP(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, gdb := newRouter(t, tdb)
	seedSKU(t, gdb, "sku-a", 1500, 10)

	orderID, paymentID := place(t, engine, "key-http00003")

	rec, body := doJSON(t, engine, http.MethodGet, "/api/v1/orders/"+orderID, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, body)
	if data["status"] != "PENDING_PAYMENT" || data["total_cents"].(float64) != 3000 {
		t.Fatalf("data = %v", data)
	}
	if data["payment_id"] != paymentID || data["payment_status"] != "PENDING" {
		t.Fatalf("支付信息 = %v/%v", data["payment_id"], data["payment_status"])
	}
	if _, ok := data["close_reason"]; ok {
		t.Fatalf("非终态不应出现 close_reason: %v", data["close_reason"])
	}

	// 不存在的订单与"他人的订单"必须返回同一个 404。
	rec, body = doJSON(t, engine, http.MethodGet, "/api/v1/orders/no-such-order", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在订单 status = %d", rec.Code)
	}
	if code := errorCodeOf(t, body); code != string(httpx.CodeNotFound) {
		t.Fatalf("code = %s", code)
	}
}

// 取消返回 200 与 CANCELLED；重复取消同样 200；已完成订单 409。
func TestCancelOrderHTTP(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, gdb := newRouter(t, tdb)
	seedSKU(t, gdb, "sku-a", 100, 10)

	orderID, _ := place(t, engine, "key-http00004")

	rec, body := doJSON(t, engine, http.MethodPost, "/api/v1/orders/"+orderID+"/cancel", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("取消 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	data := dataOf(t, body)
	if data["status"] != "CANCELLED" {
		t.Fatalf("status = %v", data["status"])
	}
	if data["close_reason"] != orderapp.CloseReasonUserCancelled {
		t.Fatalf("close_reason = %v", data["close_reason"])
	}
	if data["payment_status"] != "CANCELLED" {
		t.Fatalf("payment_status = %v", data["payment_status"])
	}

	// 重复取消：200，结果相同。
	rec, _ = doJSON(t, engine, http.MethodPost, "/api/v1/orders/"+orderID+"/cancel", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("重复取消 status = %d, want 200", rec.Code)
	}

	// 已完成的订单：409 INVALID_STATE_TRANSITION。
	orderID2, _ := place(t, engine, "key-http00005")
	if err := gdb.Exec(`UPDATE orders SET status = 'COMPLETED' WHERE order_id = ?`, orderID2).Error; err != nil {
		t.Fatalf("推进状态: %v", err)
	}
	rec, body = doJSON(t, engine, http.MethodPost, "/api/v1/orders/"+orderID2+"/cancel", nil, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("已完成取消 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, body); code != string(httpx.CodeInvalidStateTransition) {
		t.Fatalf("code = %s", code)
	}
}
