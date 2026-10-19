package authx

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

func init() { gin.SetMode(gin.TestMode) }

// newAuthEngine 构造只含认证/授权的最小路由，隔离测试目标。
func newAuthEngine(t *testing.T) (*gin.Engine, *TokenService) {
	t.Helper()
	svc, err := NewTokenService(testSecret, time.Hour)
	if err != nil {
		t.Fatalf("token service: %v", err)
	}
	e := gin.New()
	e.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(httpx.WithRequestID(c.Request.Context(), "req-1"))
	})
	e.POST("/auth/token", TokenHandler(svc, true))
	e.GET("/me", RequireAuth(svc), func(c *gin.Context) {
		p, _ := CurrentUser(c.Request.Context())
		httpx.WriteData(c, http.StatusOK, gin.H{"user_id": p.UserID, "role": p.Role})
	})
	e.GET("/admin", RequireAuth(svc), RequireRole(RoleOperator), func(c *gin.Context) {
		httpx.WriteData(c, http.StatusOK, gin.H{"ok": true})
	})
	return e, svc
}

// issueToken 直接调用服务签发，避免测试依赖 HTTP 端点。
func issueToken(t *testing.T, svc *TokenService, role Role) string {
	t.Helper()
	raw, _, err := svc.Issue("u1", role)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return raw
}

func doRequest(e *gin.Engine, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestRequireAuth(t *testing.T) {
	e, svc := newAuthEngine(t)

	if rec := doRequest(e, http.MethodGet, "/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("missing token: got %d", rec.Code)
	}
	if rec := doRequest(e, http.MethodGet, "/me", "not-a-token"); rec.Code != http.StatusUnauthorized {
		t.Errorf("garbage token: got %d", rec.Code)
	}
	if rec := doRequest(e, http.MethodGet, "/me", issueToken(t, svc, RoleBuyer)); rec.Code != http.StatusOK {
		t.Errorf("valid token: got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRequireRoleMatrix(t *testing.T) {
	e, svc := newAuthEngine(t)

	buyer := issueToken(t, svc, RoleBuyer)
	operator := issueToken(t, svc, RoleOperator)

	if rec := doRequest(e, http.MethodGet, "/admin", buyer); rec.Code != http.StatusForbidden {
		t.Errorf("buyer on operator route: got %d", rec.Code)
	}
	if rec := doRequest(e, http.MethodGet, "/admin", operator); rec.Code != http.StatusOK {
		t.Errorf("operator on operator route: got %d", rec.Code)
	}
	// 未认证访问受保护路由必须返回 401 而不是 403，便于客户端区分重试策略。
	if rec := doRequest(e, http.MethodGet, "/admin", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous on operator route: got %d", rec.Code)
	}
}

func TestTokenHandlerIssuesToken(t *testing.T) {
	e, _ := newAuthEngine(t)
	body := bytes.NewBufferString(`{"user_id":"u9","role":"operator"}`)
	req := httptest.NewRequest(http.MethodPost, "/auth/token", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data TokenResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Token == "" || resp.Data.Role != RoleOperator || resp.Data.UserID != "u9" {
		t.Errorf("unexpected response: %+v", resp.Data)
	}
	if resp.Data.ExpiresAt == "" {
		t.Error("expires_at must be set")
	}
}

func TestTokenHandlerRejectsInvalidRole(t *testing.T) {
	e, _ := newAuthEngine(t)
	req := httptest.NewRequest(http.MethodPost, "/auth/token",
		bytes.NewBufferString(`{"user_id":"u9","role":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenHandlerDisabledReturns404(t *testing.T) {
	svc, _ := NewTokenService(testSecret, time.Hour)
	e := gin.New()
	e.POST("/auth/token", TokenHandler(svc, false))

	req := httptest.NewRequest(http.MethodPost, "/auth/token",
		bytes.NewBufferString(`{"user_id":"u9","role":"buyer"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// 非本地环境按资源不存在处理，避免暴露端点存在但被禁用。
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
}
