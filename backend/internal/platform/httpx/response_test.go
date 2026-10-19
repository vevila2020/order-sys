package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// newContext 构造带请求 ID 的 gin 上下文，复用真实中间件的写入方式。
func newContext() (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request = c.Request.WithContext(WithRequestID(c.Request.Context(), "req-test"))
	c.Set(RequestIDContextKey, "req-test")
	return c, rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestWriteData(t *testing.T) {
	c, rec := newContext()
	WriteData(c, http.StatusCreated, map[string]any{"order_id": "o1"})

	body := decode(t, rec)
	if body["request_id"] != "req-test" {
		t.Errorf("request_id: got %v", body["request_id"])
	}
	data, ok := body["data"].(map[string]any)
	if !ok || data["order_id"] != "o1" {
		t.Errorf("data: got %v", body["data"])
	}
}

func TestStatusMapping(t *testing.T) {
	// 表格覆盖需求 §8.3 中 M0 涉及的全部错误码。
	cases := []struct {
		code   Code
		status int
	}{
		{CodeValidationFailed, http.StatusBadRequest},
		{CodeUnauthorized, http.StatusUnauthorized},
		{CodeForbidden, http.StatusForbidden},
		{CodeNotFound, http.StatusNotFound},
		{CodeConflict, http.StatusConflict},
		{CodeIdempotencyConflict, http.StatusConflict},
		{CodeIdempotencyInProgress, http.StatusConflict},
		{CodeInvalidStateTransition, http.StatusConflict},
		{CodeInsufficientStock, http.StatusConflict},
		{CodeSaleNotStarted, http.StatusConflict},
		{CodeSaleEnded, http.StatusConflict},
		{CodeSoldOut, http.StatusConflict},
		{CodeLimitExceeded, http.StatusConflict},
		{CodeRateLimited, http.StatusTooManyRequests},
		{CodeDependencyUnavailable, http.StatusServiceUnavailable},
		{CodeTimeout, http.StatusGatewayTimeout},
		{CodeInternal, http.StatusInternalServerError},
		{CodePayloadTooLarge, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		if got := NewError(tc.code, nil).Status(); got != tc.status {
			t.Errorf("%s: got %d want %d", tc.code, got, tc.status)
		}
	}
}

func TestWriteErrorShape(t *testing.T) {
	c, rec := newContext()
	WriteError(c, NewErrorWithDetails(CodeInsufficientStock, "", map[string]any{
		"sku_id": "s1", "requested": 2, "available": 0,
	}, nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d", rec.Code)
	}
	body := decode(t, rec)
	if body["request_id"] != "req-test" {
		t.Errorf("request_id: got %v", body["request_id"])
	}
	errBody, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("error object missing: %v", body)
	}
	if errBody["code"] != string(CodeInsufficientStock) {
		t.Errorf("code: got %v", errBody["code"])
	}
	if _, ok := errBody["message"].(string); !ok {
		t.Errorf("message missing: %v", errBody)
	}
	if _, ok := errBody["details"].(map[string]any); !ok {
		t.Errorf("details missing: %v", errBody)
	}
}

func TestInternalErrorDoesNotLeakDetails(t *testing.T) {
	c, rec := newContext()
	internal := errors.New("sql: SELECT * FROM users; dsn=root:pw@tcp(db:3306)")
	WriteErrorFrom(c, internal)

	raw := rec.Body.String()
	for _, secret := range []string{"SELECT", "dsn=", "root:pw"} {
		if strings.Contains(raw, secret) {
			t.Errorf("response leaks %q: %s", secret, raw)
		}
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d", rec.Code)
	}
	body := decode(t, rec)
	if body["error"].(map[string]any)["code"] != string(CodeInternal) {
		t.Errorf("code: got %v", body["error"])
	}
}

func TestFromErrorMapsDeadlineToTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-ctx.Done()

	appErr := FromError(ctx, context.DeadlineExceeded)
	if appErr == nil || appErr.Code != CodeTimeout {
		t.Fatalf("expected TIMEOUT, got %v", appErr)
	}

	// 已映射的错误必须原样保留，不能被折叠成 INTERNAL。
	original := NewError(CodeNotFound, errors.New("missing"))
	if got := FromError(context.Background(), original); got != original {
		t.Errorf("app error should pass through unchanged")
	}
}

func TestWithRetryAfterSetsHeader(t *testing.T) {
	c, rec := newContext()
	WriteError(c, NewError(CodeRateLimited, nil).WithRetryAfter(1500_000_000)) // 1.5s

	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After: got %q want 2", got)
	}
}

func TestAsAppErrorThroughWrapping(t *testing.T) {
	wrapped := wrap(ErrNotFound)
	appErr, ok := AsAppError(wrapped)
	if !ok || appErr.Code != CodeNotFound {
		t.Fatalf("expected NOT_FOUND through wrap, got %v", appErr)
	}
}

// wrap 模拟业务层包装错误的场景，验证 errors.Is/As 能穿透错误链。
func wrap(err error) error {
	return errors.Join(errors.New("context"), err)
}
