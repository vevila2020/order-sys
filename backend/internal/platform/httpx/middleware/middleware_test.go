package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/loggerx"
)

func init() { gin.SetMode(gin.TestMode) }

// newTestEngine 返回带 request_id/recover/accesslog 的最小引擎，测试只关注单个中间件。
func newTestEngine(logBuf *bytes.Buffer, mw ...gin.HandlerFunc) *gin.Engine {
	log := loggerx.New(loggerx.Options{
		ServiceName: "test", Env: "test", Level: "debug", Output: logBuf,
	})
	e := gin.New()
	e.Use(RequestID(), Recover(log), AccessLog(log))
	e.Use(mw...)
	return e
}

func TestRequestIDGenerated(t *testing.T) {
	e := newTestEngine(&bytes.Buffer{})
	e.GET("/ping", func(c *gin.Context) {
		httpx.WriteData(c, http.StatusOK, gin.H{"request_id": httpx.RequestID(c)})
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))

	headerID := rec.Header().Get(httpx.RequestIDHeader)
	if _, err := uuid.Parse(headerID); err != nil {
		t.Fatalf("generated id is not uuid: %q", headerID)
	}
	body := decodeBody(t, rec)
	if body["request_id"] != headerID {
		t.Errorf("body/header mismatch: %v vs %q", body["request_id"], headerID)
	}
}

func TestRequestIDPassthroughAndSanitization(t *testing.T) {
	e := newTestEngine(&bytes.Buffer{})
	e.GET("/ping", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	// 合法 ID 必须原样返回，便于客户端串联自己的追踪系统。
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(httpx.RequestIDHeader, "client-req-1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if got := rec.Header().Get(httpx.RequestIDHeader); got != "client-req-1" {
		t.Errorf("passthrough: got %q", got)
	}

	// 含换行等不可打印字符的值会被替换，防止日志注入。
	req = httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.Header.Set(httpx.RequestIDHeader, "bad\nid")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if got := rec.Header().Get(httpx.RequestIDHeader); got == "bad\nid" || got == "" {
		t.Errorf("invalid id should be regenerated, got %q", got)
	}
}

func TestRecoverReturnsUnifiedError(t *testing.T) {
	var logBuf bytes.Buffer
	e := newTestEngine(&logBuf)
	e.GET("/boom", func(*gin.Context) { panic("kaboom") })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d", rec.Code)
	}
	body := decodeBody(t, rec)
	errBody := body["error"].(map[string]any)
	if errBody["code"] != string(httpx.CodeInternal) {
		t.Errorf("code: got %v", errBody["code"])
	}
	// 堆栈只能出现在服务端日志，绝不能出现在响应体。
	if strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("response leaks stack: %s", rec.Body.String())
	}
	if !strings.Contains(logBuf.String(), "panic recovered") || !strings.Contains(logBuf.String(), "kaboom") {
		t.Errorf("log missing panic details: %s", logBuf.String())
	}
}

func TestAccessLogUsesRouteTemplate(t *testing.T) {
	var logBuf bytes.Buffer
	e := newTestEngine(&logBuf)
	e.GET("/orders/:order_id", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/orders/secret-order-1", nil))

	out := logBuf.String()
	if !strings.Contains(out, `"route":"/orders/:order_id"`) {
		t.Errorf("log should use route template: %s", out)
	}
	// 原始 URL 含用户输入，不能进入日志。
	if strings.Contains(out, "secret-order-1") {
		t.Errorf("log leaks raw path: %s", out)
	}
	if !strings.Contains(out, `"request_id"`) {
		t.Errorf("log missing request_id: %s", out)
	}
}

func TestAccessLogLevelByStatus(t *testing.T) {
	var logBuf bytes.Buffer
	e := newTestEngine(&logBuf)
	e.GET("/warn", func(c *gin.Context) { c.Status(http.StatusNotFound) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/warn", nil))
	if !strings.Contains(logBuf.String(), `"level":"warn"`) {
		t.Errorf("4xx should log at warn: %s", logBuf.String())
	}
}

func TestTimeoutReturnsGatewayTimeout(t *testing.T) {
	e := newTestEngine(&bytes.Buffer{}, Timeout(20*time.Millisecond))
	e.GET("/slow", func(c *gin.Context) {
		// 约定：handler 观察到 ctx 取消后必须直接返回，不再写响应；
		// 由中间件保证客户端收到统一的 504 错误体。
		<-c.Request.Context().Done()
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/slow", nil))

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["error"].(map[string]any)["code"] != string(httpx.CodeTimeout) {
		t.Errorf("code: %v", body["error"])
	}
}

func TestTimeoutAllowsFastHandler(t *testing.T) {
	e := newTestEngine(&bytes.Buffer{}, Timeout(time.Second))
	e.GET("/fast", func(c *gin.Context) { httpx.WriteData(c, http.StatusOK, gin.H{"ok": true}) })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/fast", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBodyLimitContentLength(t *testing.T) {
	e := newTestEngine(&bytes.Buffer{}, BodyLimit(16))
	e.POST("/data", func(c *gin.Context) { c.Status(http.StatusOK) })

	body := strings.NewReader(strings.Repeat("a", 64))
	req := httptest.NewRequest(http.MethodPost, "/data", body)
	req.ContentLength = 64

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	if decodeBody(t, rec)["error"].(map[string]any)["code"] != string(httpx.CodePayloadTooLarge) {
		t.Errorf("unexpected error body: %s", rec.Body.String())
	}
}

func TestBodyLimitChunked(t *testing.T) {
	// Content-Length 为 -1 模拟分块传输，必须由 MaxBytesReader 兜住。
	e := newTestEngine(&bytes.Buffer{}, BodyLimit(16))
	e.POST("/data", func(c *gin.Context) {
		_, err := c.GetRawData()
		if err != nil {
			httpx.WriteError(c, BodyLimitError(err))
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/data", strings.NewReader(strings.Repeat("a", 64)))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	if IsBodyTooLarge(errors.New("wrapped")) {
		t.Error("unrelated error must not be treated as body-too-large")
	}
}

func TestBodyLimitWithinLimit(t *testing.T) {
	e := newTestEngine(&bytes.Buffer{}, BodyLimit(64))
	e.POST("/data", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/data", strings.NewReader("small"))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
}

func TestTimeoutCancelsContext(t *testing.T) {
	// 独立验证 context 截止时间被正确传播，handler 能观察到取消。
	done := make(chan error, 1)
	e := newTestEngine(&bytes.Buffer{}, Timeout(10*time.Millisecond))
	e.GET("/ctx", func(c *gin.Context) {
		deadline, ok := c.Request.Context().Deadline()
		if !ok {
			done <- errors.New("missing deadline")
			return
		}
		<-c.Request.Context().Done()
		if deadline.IsZero() {
			done <- errors.New("missing deadline")
			return
		}
		done <- c.Request.Context().Err()
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ctx", nil))
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected deadline exceeded, got %v", err)
	}
}

// decodeBody 解析统一响应体。
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
	}
	return body
}
