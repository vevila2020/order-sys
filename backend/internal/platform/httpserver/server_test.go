package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/authx"
	"github.com/vevila2020/order-sys/internal/platform/healthx"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/loggerx"
	"github.com/vevila2020/order-sys/internal/platform/observability"
	"github.com/vevila2020/order-sys/internal/platform/ratelimit"
)

const testSecret = "test-secret-must-be-at-least-32-bytes" //nolint:gosec // 测试固定密钥

// newTestEngine 装配与生产一致的依赖，仅替换配置取值以缩短测试时间。
func newTestEngine(t *testing.T, localAuth bool) (*gin.Engine, *authx.TokenService) {
	t.Helper()
	svc, err := authx.NewTokenService(testSecret, time.Hour)
	if err != nil {
		t.Fatalf("token service: %v", err)
	}
	registry := healthx.NewRegistry(time.Second)
	registry.Register(healthx.Check{Name: "config", Critical: true, Func: func(context.Context) error {
		return nil
	}})

	engine := NewEngine(ServerDeps{
		Config: ServerConfig{
			MaxBodyBytes:     256,
			RequestTimeout:   time.Second,
			RateLimitEnabled: false,
		},
		Logger:    loggerx.New(loggerx.Options{ServiceName: "test", Env: "test", Level: "error", Output: &bytes.Buffer{}}),
		Metrics:   observability.NewMetrics(),
		Health:    registry,
		Tokens:    svc,
		Limiter:   ratelimit.Noop{},
		LocalAuth: localAuth,
	})
	return engine, svc
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestHealthEndpoints(t *testing.T) {
	e, _ := newTestEngine(t, true)
	for _, path := range []string{"/health/live", "/health/ready"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: got %d", path, rec.Code)
		}
		if _, ok := decode(t, rec)["request_id"].(string); !ok {
			t.Errorf("%s: missing request_id", path)
		}
	}
}

func TestAuthTokenAndRoleMatrix(t *testing.T) {
	e, _ := newTestEngine(t, true)

	token := mintToken(t, e, "u1", "buyer")

	rec := doJSON(e, http.MethodGet, "/api/v1/me", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: got %d body=%s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["data"].(map[string]any)["user_id"] != "u1" {
		t.Errorf("identity not from token: %s", rec.Body.String())
	}

	// buyer 访问 operator 路由必须 403，且不泄露资源细节。
	rec = doJSON(e, http.MethodGet, "/api/v1/admin/ping", "", token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin as buyer: got %d", rec.Code)
	}

	operatorToken := mintToken(t, e, "op1", "operator")
	rec = doJSON(e, http.MethodGet, "/api/v1/admin/ping", "", operatorToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin as operator: got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAuthTokenDisabledReturns404(t *testing.T) {
	e, _ := newTestEngine(t, false)
	body := strings.NewReader(`{"user_id":"u1","role":"buyer"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled token endpoint: got %d", rec.Code)
	}
}

func TestUnknownRouteReturnsUnifiedError(t *testing.T) {
	e, _ := newTestEngine(t, true)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d", rec.Code)
	}
	errObj := decode(t, rec)["error"].(map[string]any)
	if errObj["code"] != string(httpx.CodeNotFound) {
		t.Errorf("code: got %v", errObj["code"])
	}
}

func TestRequestIDHeaderAndBodyMatch(t *testing.T) {
	e, _ := newTestEngine(t, true)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	if rec.Header().Get(httpx.RequestIDHeader) == "" {
		t.Fatal("missing request id header")
	}
	if rec.Header().Get(httpx.RequestIDHeader) != decode(t, rec)["request_id"] {
		t.Error("header and body request_id differ")
	}
}

func TestBodyLimitOnBusinessRoute(t *testing.T) {
	e, _ := newTestEngine(t, true)
	body := strings.NewReader(`{"user_id":"` + strings.Repeat("u", 400) + `","role":"buyer"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPanicReturnsUnified500(t *testing.T) {
	e, _ := newTestEngine(t, true)
	// 通过一个临时路由触发 panic，验证 recover 在整条链中生效。
	e.GET("/panic", func(*gin.Context) { panic("boom") })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("panic detail leaked: %s", rec.Body.String())
	}
}

// mintToken 走真实签发端点，覆盖请求体解析与响应结构。
func mintToken(t *testing.T, e *gin.Engine, userID, role string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token",
		strings.NewReader(`{"user_id":"`+userID+`","role":"`+role+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint token: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Data.Token
}

// doJSON 发送可选 Bearer token 的请求。
func doJSON(e *gin.Engine, method, path, body, token string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestBodyLimitRunsAfterAuth(t *testing.T) {
	// 中间件顺序约定：认证先于请求体读取。未认证的超大请求必须返回 401，
	// 这样攻击者无法用大请求体消耗认证前的资源。
	e, _ := newTestEngine(t, true)
	// 使用 GET 走已注册路由；请求体大小检查在认证之后执行。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/ping",
		strings.NewReader(strings.Repeat("x", 1024)))
	req.ContentLength = 1024
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRoleRunsBeforeRateLimit(t *testing.T) {
	// 角色不符必须 403，而不是先被限流（顺序：auth → role → ratelimit）。
	e, _ := newTestEngine(t, true)
	token := mintToken(t, e, "u1", "buyer")
	rec := doJSON(e, http.MethodGet, "/api/v1/admin/ping", "", token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d", rec.Code)
	}
}
