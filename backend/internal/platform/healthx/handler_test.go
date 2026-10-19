package healthx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// newRouter 挂载 live/ready，模拟真实路由装配。
func newRouter(r *Registry) *gin.Engine {
	e := gin.New()
	e.GET("/health/live", LiveHandler())
	e.GET("/health/ready", ReadyHandler(r))
	return e
}

func TestLiveHandlerIgnoresFailingChecks(t *testing.T) {
	r := NewRegistry(10e6) // 10ms
	r.Register(Check{Name: "mysql", Critical: true, Func: func(context.Context) error {
		return errors.New("down")
	}})

	rec := httptest.NewRecorder()
	newRouter(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	// liveness 只反映进程存活，依赖故障不得触发重启风暴。
	if rec.Code != http.StatusOK {
		t.Fatalf("live status: got %d body=%s", rec.Code, rec.Body.String())
	}
	if decodeStatus(t, rec) != string(StatusOK) {
		t.Errorf("live payload: %s", rec.Body.String())
	}
}

func TestReadyHandlerCriticalFailureIs503(t *testing.T) {
	r := NewRegistry(10e6)
	r.Register(Check{Name: "mysql", Critical: true, Func: func(context.Context) error {
		return errors.New("down")
	}})

	rec := httptest.NewRecorder()
	newRouter(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status: got %d", rec.Code)
	}
}

func TestReadyHandlerNonCriticalIsDegraded200(t *testing.T) {
	r := NewRegistry(10e6)
	r.Register(Check{Name: "redis", Critical: false, Func: func(context.Context) error {
		return errors.New("down")
	}})

	rec := httptest.NewRecorder()
	newRouter(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("ready status: got %d", rec.Code)
	}
	if got := decodeStatus(t, rec); got != string(StatusDegraded) {
		t.Errorf("ready status: got %q want degraded", got)
	}
}

// decodeStatus 读取统一响应体中的 data.status 字段。
func decodeStatus(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return body.Data.Status
}
