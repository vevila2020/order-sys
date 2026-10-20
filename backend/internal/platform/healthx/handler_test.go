package healthx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// TestReadyHandlerDurationIsMilliseconds 锁定 duration_ms 的单位。
//
// time.Duration 的 JSON 编码是纳秒整数，若直接序列化，8ms 会变成 8000000，
// 而字段名仍写着 duration_ms——探针与告警会按毫秒解读，导致延迟被高估 10^6 倍。
func TestReadyHandlerDurationIsMilliseconds(t *testing.T) {
	const sleep = 5 * time.Millisecond
	r := NewRegistry(time.Second)
	r.Register(Check{Name: "slow", Critical: true, Func: func(context.Context) error {
		time.Sleep(sleep)
		return nil
	}})

	rec := httptest.NewRecorder()
	newRouter(r).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))

	var body struct {
		Data struct {
			Checks []struct {
				DurationMS int64 `json:"duration_ms"`
			} `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(body.Data.Checks) != 1 {
		t.Fatalf("checks: %s", rec.Body.String())
	}
	// 允许调度抖动，但数量级必须落在毫秒区间：纳秒编码会是 5_000_000 以上。
	got := body.Data.Checks[0].DurationMS
	if got < 3 || got > 200 {
		t.Fatalf("duration_ms: got %d, want a millisecond value near %d", got, sleep.Milliseconds())
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
