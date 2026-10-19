package authx

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret-must-be-at-least-32-bytes" //nolint:gosec // 测试固定密钥

// newService 构造使用可控时钟的 TokenService，避免测试依赖真实时间。
func newService(t *testing.T, secret string, ttl time.Duration, now time.Time) *TokenService {
	t.Helper()
	svc, err := NewTokenService(secret, ttl)
	if err != nil {
		t.Fatalf("NewTokenService: %v", err)
	}
	svc.now = func() time.Time { return now }
	return svc
}

func TestNewTokenServiceRejectsWeakSecret(t *testing.T) {
	if _, err := NewTokenService("short", time.Hour); !errors.Is(err, ErrWeakSecret) {
		t.Fatalf("expected ErrWeakSecret, got %v", err)
	}
	if _, err := NewTokenService(testSecret, 0); err == nil {
		t.Fatal("zero ttl must be rejected")
	}
}

func TestIssueAndParseRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	svc := newService(t, testSecret, time.Hour, now)

	raw, claims, err := svc.Issue("u1", RoleBuyer)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if claims.Subject != "u1" || claims.UserID != "u1" || claims.Role != RoleBuyer {
		t.Errorf("unexpected claims: %+v", claims)
	}
	if claims.ID == "" {
		t.Error("jti must be set")
	}
	if got := claims.ExpiresAt.Time; !got.Equal(now.Add(time.Hour)) {
		t.Errorf("exp: got %s", got)
	}

	p, err := svc.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.UserID != "u1" || p.Role != RoleBuyer {
		t.Errorf("principal: %+v", p)
	}
}

func TestIssueRejectsInvalidInput(t *testing.T) {
	svc := newService(t, testSecret, time.Hour, time.Now())
	if _, _, err := svc.Issue("", RoleBuyer); err == nil {
		t.Error("empty user id must be rejected")
	}
	if _, _, err := svc.Issue("u1", Role("admin")); err == nil {
		t.Error("unknown role must be rejected")
	}
}

func TestParseRejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	svc := newService(t, testSecret, time.Hour, now)
	raw, _, err := svc.Issue("u1", RoleBuyer)
	if err != nil {
		t.Fatal(err)
	}

	// 时钟推进两小时后，token 必须过期。
	svc.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := svc.Parse(raw); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expected expiration, got %v", err)
	}
}

func TestParseRejectsWrongSecret(t *testing.T) {
	now := time.Now()
	issuer := newService(t, testSecret, time.Hour, now)
	verifier := newService(t, testSecret+"-different", time.Hour, now)

	raw, _, err := issuer.Issue("u1", RoleBuyer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Parse(raw); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected invalid token, got %v", err)
	}
}

func TestParseRejectsAlgNone(t *testing.T) {
	// 手工构造 alg=none 的 token，验证 valid methods 白名单生效。
	claims := newClaims("u1", RoleBuyer, TokenIssuer, TokenAudience, time.Now(), time.Hour)
	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	raw, err := token.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, testSecret, time.Hour, time.Now())
	if _, err := svc.Parse(raw); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("alg=none must be rejected, got %v", err)
	}
}

func TestParseRejectsWrongIssuerOrAudience(t *testing.T) {
	now := time.Now()
	claims := newClaims("u1", RoleBuyer, "other-issuer", TokenAudience, now, time.Hour)
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	svc := newService(t, testSecret, time.Hour, now)
	if _, err := svc.Parse(raw); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong issuer must be rejected, got %v", err)
	}

	claims = newClaims("u1", RoleBuyer, TokenIssuer, "other-audience", now, time.Hour)
	raw, _ = jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if _, err := svc.Parse(raw); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong audience must be rejected, got %v", err)
	}
}

func TestBearerToken(t *testing.T) {
	cases := map[string]string{
		"Bearer abc": "abc",
		"bearer abc": "abc",
		"Bearer ":    "",
		"Basic abc":  "",
		"abc":        "",
		"Bearer":     "",
	}
	for header, want := range cases {
		if got := bearerToken(header); got != want {
			t.Errorf("%q: got %q want %q", header, got, want)
		}
	}
}

func TestTokenIsThreeSegments(t *testing.T) {
	svc := newService(t, testSecret, time.Hour, time.Now())
	raw, _, err := svc.Issue("u1", RoleBuyer)
	if err != nil {
		t.Fatal(err)
	}
	// JWT 必须是 header.payload.signature；任何段为空说明签名被跳过。
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[2] == "" {
		t.Fatalf("unexpected token shape: %q", raw)
	}
	if _, err := base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
		t.Errorf("payload not base64url: %v", err)
	}
}
