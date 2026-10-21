//go:build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	inventoryapp "github.com/vevila2020/order-sys/internal/inventory/app"
	inventoryhttp "github.com/vevila2020/order-sys/internal/inventory/http"
	inventoryinfra "github.com/vevila2020/order-sys/internal/inventory/infra"
	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/testdb"
)

// 这些用例走完整的 HTTP → app → infra 链路，只把鉴权中间件省掉：
// 角色矩阵已在 authx 的单测与 httpserver 的装配测试里覆盖，这里关心的是
// 端点的绑定、错误码与状态码。

// newRouter 组装 engine + handler，返回可供 httptest 调用的路由。
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

	svc := inventoryapp.NewService(
		db.NewTransactionManager(gdb),
		inventoryinfra.NewSKURepository(),
		inventoryinfra.NewInventoryRepository(),
		inventoryinfra.NewReservationRepository(),
	)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/api/v1")
	inventoryhttp.Register(group, inventoryhttp.NewHandler(svc))
	return engine, gdb
}

// doJSON 发送请求并把响应解析为统一响应体。
func doJSON(t *testing.T, engine *gin.Engine, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
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

// dataOf 取出 success 响应里的 data 对象。
func dataOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 data 对象: %v", body)
	}
	return data
}

// errorCodeOf 取出错误响应里的 code。
func errorCodeOf(t *testing.T, body map[string]any) string {
	t.Helper()
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 error 对象: %v", body)
	}
	code, _ := e["code"].(string)
	return code
}

// 创建返回 201，响应含初始库存与 version=0。
func TestCreateSKUEndpoint(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, _ := newRouter(t, tdb)

	rec, body := doJSON(t, engine, http.MethodPost, "/api/v1/skus", map[string]any{
		"sku_id":        "sku-api-1",
		"name":          "限量键盘",
		"price_cents":   29900,
		"initial_stock": 100,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	data := dataOf(t, body)
	if data["sku_id"] != "sku-api-1" || data["status"] != "ACTIVE" {
		t.Fatalf("data = %v", data)
	}
	if data["available"].(float64) != 100 || data["reserved"].(float64) != 0 {
		t.Fatalf("库存字段 = %v", data)
	}
	if data["version"].(float64) != 0 {
		t.Fatalf("version = %v, want 0", data["version"])
	}
}

// 负初始库存必须在库外被拒绝（VALIDATION_FAILED），不能写进 CHECK 约束。
func TestCreateSKURejectsNegativeStock(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, _ := newRouter(t, tdb)

	rec, body := doJSON(t, engine, http.MethodPost, "/api/v1/skus", map[string]any{
		"sku_id":        "sku-api-neg",
		"name":          "负库存",
		"price_cents":   100,
		"initial_stock": -1,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, body); code != string(httpx.CodeValidationFailed) {
		t.Fatalf("code = %s", code)
	}
}

// 重复 sku_id 返回 409 而不是 500。
func TestCreateSKUDuplicate(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, _ := newRouter(t, tdb)

	payload := map[string]any{
		"sku_id": "sku-api-dup", "name": "重复", "price_cents": 100, "initial_stock": 1,
	}
	if rec, _ := doJSON(t, engine, http.MethodPost, "/api/v1/skus", payload); rec.Code != http.StatusCreated {
		t.Fatalf("首次创建 status = %d", rec.Code)
	}

	rec, body := doJSON(t, engine, http.MethodPost, "/api/v1/skus", payload)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, body); code != string(httpx.CodeConflict) {
		t.Fatalf("code = %s", code)
	}
}

// 查询不存在返回 404。
func TestGetSKUNotFound(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, _ := newRouter(t, tdb)

	rec, body := doJSON(t, engine, http.MethodGet, "/api/v1/skus/sku-missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, body); code != string(httpx.CodeNotFound) {
		t.Fatalf("code = %s", code)
	}
}

// 更新携带过期 version 返回 409；携带正确 version 返回 200 并递增版本。
func TestUpdateSKUVersionConflict(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, _ := newRouter(t, tdb)

	if rec, _ := doJSON(t, engine, http.MethodPost, "/api/v1/skus", map[string]any{
		"sku_id": "sku-api-upd", "name": "原名", "price_cents": 100, "initial_stock": 5,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("创建失败: %d", rec.Code)
	}

	// 正确版本：0 → 200，版本递增到 1。
	rec, body := doJSON(t, engine, http.MethodPatch, "/api/v1/skus/sku-api-upd", map[string]any{
		"version": 0, "price_cents": 200,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("更新 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if v := dataOf(t, body)["version"].(float64); v != 1 {
		t.Fatalf("更新后 version = %v, want 1", v)
	}

	// 仍用 version=0：必须 409，且价格不能被覆盖。
	rec, body = doJSON(t, engine, http.MethodPatch, "/api/v1/skus/sku-api-upd", map[string]any{
		"version": 0, "price_cents": 300,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("过期版本 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, body); code != string(httpx.CodeConflict) {
		t.Fatalf("code = %s", code)
	}

	rec, body = doJSON(t, engine, http.MethodGet, "/api/v1/skus/sku-api-upd", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("查询失败: %d", rec.Code)
	}
	if price := dataOf(t, body)["price_cents"].(float64); price != 200 {
		t.Fatalf("price_cents = %v, want 200（冲突的更新不应生效）", price)
	}
}

// 改价不影响历史订单：订单明细保存的是下单时的单价快照。
func TestUpdateSKUPriceDoesNotAffectOrders(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, gdb := newRouter(t, tdb)

	if rec, _ := doJSON(t, engine, http.MethodPost, "/api/v1/skus", map[string]any{
		"sku_id": "sku-api-hist", "name": "历史价", "price_cents": 100, "initial_stock": 10,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("创建失败: %d", rec.Code)
	}

	// 模拟一笔已存在的订单明细，单价 100。
	if err := gdb.Exec(
		`INSERT INTO orders (order_id, user_id, status, total_cents, currency, version, created_at, updated_at)
		 VALUES ('ord-hist', 'u1', 'PENDING_PAYMENT', 200, 'CNY', 0, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`).Error; err != nil {
		t.Fatalf("写入订单: %v", err)
	}
	if err := gdb.Exec(
		`INSERT INTO order_items (order_id, sku_id, quantity, unit_price_cents, amount_cents)
		 VALUES ('ord-hist', 'sku-api-hist', 2, 100, 200)`).Error; err != nil {
		t.Fatalf("写入明细: %v", err)
	}

	rec, _ := doJSON(t, engine, http.MethodPatch, "/api/v1/skus/sku-api-hist", map[string]any{
		"version": 0, "price_cents": 999,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("改价 status = %d", rec.Code)
	}

	var unitPrice int64
	if err := gdb.Raw(`SELECT unit_price_cents FROM order_items WHERE order_id = 'ord-hist'`).
		Scan(&unitPrice).Error; err != nil {
		t.Fatalf("读取明细: %v", err)
	}
	if unitPrice != 100 {
		t.Fatalf("历史订单单价被改成 %d：价格快照失效", unitPrice)
	}
}

// 停用后查询仍可用（管理端点需要看到停用商品），状态字段正确。
func TestUpdateSKUStatus(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, _ := newRouter(t, tdb)

	if rec, _ := doJSON(t, engine, http.MethodPost, "/api/v1/skus", map[string]any{
		"sku_id": "sku-api-off", "name": "停用", "price_cents": 100, "initial_stock": 1,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("创建失败: %d", rec.Code)
	}

	rec, body := doJSON(t, engine, http.MethodPatch, "/api/v1/skus/sku-api-off", map[string]any{
		"version": 0, "status": "INACTIVE",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("停用 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if status := dataOf(t, body)["status"]; status != "INACTIVE" {
		t.Fatalf("status = %v", status)
	}
}

// 非法状态值由 binding 拦下，返回 VALIDATION_FAILED。
func TestUpdateSKURejectsUnknownStatus(t *testing.T) {
	ctx := context.Background()
	tdb := testdb.NewMigrated(ctx, t)
	engine, _ := newRouter(t, tdb)

	if rec, _ := doJSON(t, engine, http.MethodPost, "/api/v1/skus", map[string]any{
		"sku_id": "sku-api-bad", "name": "非法状态", "price_cents": 100, "initial_stock": 1,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("创建失败: %d", rec.Code)
	}

	rec, body := doJSON(t, engine, http.MethodPatch, "/api/v1/skus/sku-api-bad", map[string]any{
		"version": 0, "status": "DELETED",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, body); code != string(httpx.CodeValidationFailed) {
		t.Fatalf("code = %s", code)
	}
}
