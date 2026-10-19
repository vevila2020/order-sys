package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

func TestMetricsExposeHTTPAndRuntimeSeries(t *testing.T) {
	m := NewMetrics()
	e := gin.New()
	e.Use(m.Middleware())
	e.GET("/orders/:order_id", func(c *gin.Context) { c.Status(http.StatusOK) })
	e.GET("/metrics", m.GinHandler())

	// 先产生一次业务请求，再抓取指标。
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/o1", nil))

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	for _, series := range []string{
		"http_requests_total",
		"http_request_duration_seconds",
		"http_requests_in_flight",
		"go_goroutines",
	} {
		if !strings.Contains(body, series) {
			t.Errorf("missing series %q", series)
		}
	}
	// 路由标签必须使用模板，不能包含具体订单 ID。
	if !strings.Contains(body, `route="/orders/:order_id"`) {
		t.Errorf("route label not templated:\n%s", firstLines(body, 40))
	}
	// 只检查标签值，避免把 go_info 里的版本号 "go1.x" 误判为路径泄露。
	if strings.Contains(body, `route="/orders/o1"`) {
		t.Errorf("raw path leaked into route label")
	}
}

func TestMetricsUnknownRouteHasNoHighCardinality(t *testing.T) {
	m := NewMetrics()
	e := gin.New()
	e.Use(m.Middleware())
	e.GET("/metrics", m.GinHandler())

	for _, path := range []string{"/random-1", "/random-2", "/random-3"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `route="unknown"`) {
		t.Errorf("unmatched routes must collapse to unknown:\n%s", firstLines(body, 30))
	}
	if strings.Contains(body, "random-") {
		t.Errorf("raw unmatched path leaked into labels")
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
