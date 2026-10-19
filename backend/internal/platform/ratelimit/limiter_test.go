package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/authx"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

func init() { gin.SetMode(gin.TestMode) }

// fakeLimiter 是可编程的测试替身，不引入任何生产限流实现。
type fakeLimiter struct {
	allowed    bool
	retryAfter time.Duration
	err        error
	gotKey     string
}

func (f *fakeLimiter) Allow(_ context.Context, key string, _ int) (bool, time.Duration, error) {
	f.gotKey = key
	return f.allowed, f.retryAfter, f.err
}

func newEngine(limiter Limiter, enabled bool) *gin.Engine {
	e := gin.New()
	e.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(httpx.WithRequestID(c.Request.Context(), "req"))
	})
	e.POST("/limited", Middleware(limiter, enabled), func(c *gin.Context) {
		httpx.WriteData(c, http.StatusOK, gin.H{"ok": true})
	})
	return e
}

func TestNoopLimiterAllows(t *testing.T) {
	allowed, _, err := Noop{}.Allow(context.Background(), "user:u1", 1)
	if !allowed || err != nil {
		t.Fatalf("noop should allow, got %v %v", allowed, err)
	}
}

func TestDisabledMiddlewareBypassesLimiter(t *testing.T) {
	f := &fakeLimiter{allowed: false}
	rec := httptest.NewRecorder()
	newEngine(f, false).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/limited", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("disabled limiter should pass: %d", rec.Code)
	}
	if f.gotKey != "" {
		t.Errorf("limiter must not be called when disabled")
	}
}

func TestRejectedRequestReturns429WithRetryAfter(t *testing.T) {
	f := &fakeLimiter{allowed: false, retryAfter: 2500 * time.Millisecond}
	rec := httptest.NewRecorder()
	newEngine(f, true).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/limited", nil))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status: got %d", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After: got %q", got)
	}
}

func TestLimiterErrorFailsClosed(t *testing.T) {
	f := &fakeLimiter{err: errors.New("redis down")}
	rec := httptest.NewRecorder()
	newEngine(f, true).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/limited", nil))

	// 限流存储故障时不能放行全部流量，必须返回依赖不可用。
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestKeyPrefersUserIDOverIP(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Request.RemoteAddr = "203.0.113.9:1234"

	if got := KeyFromContext(c); got != "ip:203.0.113.9" {
		t.Errorf("anonymous key: got %q", got)
	}

	ctx := authx.WithPrincipal(c.Request.Context(), authx.Principal{UserID: "u1", Role: authx.RoleBuyer})
	c.Request = c.Request.WithContext(ctx)
	if got := KeyFromContext(c); got != "user:u1" {
		t.Errorf("authenticated key: got %q", got)
	}
}
