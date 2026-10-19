// Package healthx 实现 liveness 与 readiness 检查。
//
// 语义（需求 FR-025）：
//   - live 只反映进程是否存在，永远不探测依赖，避免依赖抖动触发重启风暴；
//   - ready 探测依赖：关键依赖失败返回 503，非关键依赖失败返回 200 + degraded。
//
// M0 只注册配置自检；MySQL/Redis/Kafka 的真实检查分别在 M1/M3 注册。
package healthx

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Status 是单个检查或整体就绪状态。
type Status string

const (
	StatusOK       Status = "ok"
	StatusDegraded Status = "degraded"
	StatusFail     Status = "fail"
)

// Check 描述一个依赖检查。Timeout<=0 时使用 Registry 的默认超时。
type Check struct {
	Name     string
	Critical bool
	Timeout  time.Duration
	// Func 执行实际探测；返回 nil 表示健康。必须尊重传入的 ctx。
	Func func(ctx context.Context) error
}

// Result 是单次检查结果，包含耗时便于定位慢依赖；不包含 DSN 或密码。
type Result struct {
	Name     string        `json:"name"`
	Status   Status        `json:"status"`
	Critical bool          `json:"critical"`
	Duration time.Duration `json:"duration_ms"`
	Error    string        `json:"error,omitempty"`
}

// Report 是 ready 端点的响应体。
type Report struct {
	Status Status   `json:"status"`
	Checks []Result `json:"checks"`
}

// Registry 保存检查项。注册在启动期完成，运行期只读，因此用 RWMutex 保护。
type Registry struct {
	mu             sync.RWMutex
	checks         []Check
	defaultTimeout time.Duration
}

// NewRegistry 创建注册表；defaultTimeout 为单个检查未指定超时时的兜底值。
func NewRegistry(defaultTimeout time.Duration) *Registry {
	if defaultTimeout <= 0 {
		defaultTimeout = time.Second
	}
	return &Registry{defaultTimeout: defaultTimeout}
}

// Register 注册检查。同名检查会被替换，避免重复注册导致结果重复。
func (r *Registry) Register(c Check) {
	if c.Name == "" || c.Func == nil {
		return
	}
	if c.Timeout <= 0 {
		c.Timeout = r.defaultTimeout
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.checks {
		if r.checks[i].Name == c.Name {
			r.checks[i] = c
			return
		}
	}
	r.checks = append(r.checks, c)
}

// Run 并发执行全部检查，总耗时受每个检查的超时约束。
// 并发执行避免一个慢依赖串行拖长整个 readiness 响应。
func (r *Registry) Run(ctx context.Context) Report {
	r.mu.RLock()
	checks := make([]Check, len(r.checks))
	copy(checks, r.checks)
	r.mu.RUnlock()

	results := make([]Result, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			results[i] = runCheck(ctx, c)
		}(i, c)
	}
	wg.Wait()

	// 固定按名称排序，保证响应体稳定，便于测试和人工比对。
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })

	report := Report{Status: StatusOK, Checks: results}
	for _, res := range results {
		switch {
		case res.Status == StatusFail && res.Critical:
			report.Status = StatusFail
		case res.Status == StatusFail:
			// 非关键失败只降级，不阻止服务接收流量。
			if report.Status == StatusOK {
				report.Status = StatusDegraded
			}
		}
	}
	return report
}

// runCheck 执行单个检查并强制超时，超时按失败处理。
func runCheck(parent context.Context, c Check) Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, c.Timeout)
	defer cancel()

	// 用带缓冲的 channel 防止超时后 goroutine 阻塞在发送上。
	done := make(chan error, 1)
	go func() { done <- c.Func(ctx) }()

	res := Result{Name: c.Name, Critical: c.Critical, Status: StatusOK}
	select {
	case err := <-done:
		if err != nil {
			res.Status = StatusFail
			res.Error = err.Error()
		}
	case <-ctx.Done():
		res.Status = StatusFail
		res.Error = "health check timed out"
	}
	res.Duration = time.Since(start)
	return res
}
