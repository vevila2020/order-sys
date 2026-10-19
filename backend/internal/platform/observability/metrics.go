// Package observability 提供 Prometheus 指标骨架。
//
// 指标命名与标签遵循需求 FR-037：标签基数必须可控，路由标签只能使用
// gin 的路由模板（c.FullPath()），禁止使用原始 URL。
package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics 持有 M0 所需的指标集合和一个独立 registry。
//
// 使用独立 registry 而不是默认 registry，避免第三方库的 init 注册污染 /metrics；
// 测试也可以安全地重复构造。
type Metrics struct {
	Registry *prometheus.Registry

	requestsTotal    *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	requestsInFlight prometheus.Gauge
}

// NewMetrics 构造指标集合并注册 Go runtime 与 process collector。
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests by method, route and status.",
		}, []string{"method", "route", "status"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "HTTP request latency in seconds by method and route.",
			// 分桶覆盖本地压测的常见量级，避免分桶过密导致基数膨胀。
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15},
		}, []string{"method", "route"}),
		requestsInFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Number of HTTP requests currently being served.",
		}),
	}
	reg.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.requestsInFlight,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler 返回 /metrics 的处理器。
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{
		// 指标编码失败不应让整个 /metrics 挂掉，但要暴露错误计数。
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// GinHandler 把 /metrics 挂到 gin 路由。
func (m *Metrics) GinHandler() gin.HandlerFunc {
	h := m.Handler()
	return func(c *gin.Context) {
		// promhttp 直接写 ResponseWriter，因此这里绕过统一响应体；/metrics 是
		// Prometheus 文本格式，不属于业务 API。
		h.ServeHTTP(c.Writer, c.Request)
	}
}

// Middleware 记录 HTTP 请求量、延迟与在途请求数。
func (m *Metrics) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		m.requestsInFlight.Inc()
		defer m.requestsInFlight.Dec()

		c.Next()

		route := c.FullPath()
		if route == "" {
			// 未匹配路由统一标记 unknown，防止攻击者用随机路径制造高基数标签。
			route = "unknown"
		}
		method := c.Request.Method
		status := strconv.Itoa(c.Writer.Status())
		m.requestsTotal.WithLabelValues(method, route, status).Inc()
		m.requestDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
	}
}
