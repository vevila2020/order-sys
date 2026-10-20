package authx

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Issuer 与 Audience 是本服务签发 token 的固定标识；校验时必须匹配，
// 防止其他系统的 token 被误用。
const (
	TokenIssuer = "order-sys"
	// TokenAudience 是 JWT 的 aud 声明值，不是凭据；gosec 的 G101 会按
	// "硬编码字符串看着像密钥"报警，这里显式豁免并保留标识符语义。
	TokenAudience = "order-sys-api" //nolint:gosec // aud 声明值，非密钥
)

// minSecretLen 是 HS256 密钥的最小长度（32 字节对应 256 bit）。
const minSecretLen = 32

// 哨兵错误供中间件映射为统一 401，不区分具体失败原因，
// 避免向客户端泄露"用户不存在"还是"签名错误"。
var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token expired")
	ErrWeakSecret   = errors.New("jwt secret must be at least 32 bytes")
)

// TokenService 负责签发与校验 token。
type TokenService struct {
	secret   []byte
	ttl      time.Duration
	issuer   string
	audience string
	// now 可在测试中替换，避免依赖真实时钟。
	now func() time.Time
}

// NewTokenService 校验密钥强度后构造服务；密钥过短直接失败，防止误配置。
func NewTokenService(secret string, ttl time.Duration) (*TokenService, error) {
	if len(secret) < minSecretLen {
		return nil, ErrWeakSecret
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("jwt ttl must be positive")
	}
	return &TokenService{
		secret:   []byte(secret),
		ttl:      ttl,
		issuer:   TokenIssuer,
		audience: TokenAudience,
		now:      time.Now,
	}, nil
}

// TTL 返回 token 有效期，供签发响应返回过期时间。
func (s *TokenService) TTL() time.Duration { return s.ttl }

// Issue 为指定用户与角色签发 HS256 token。
func (s *TokenService) Issue(userID string, role Role) (string, Claims, error) {
	if userID == "" {
		return "", Claims{}, fmt.Errorf("user_id is required")
	}
	if !role.Valid() {
		return "", Claims{}, fmt.Errorf("role must be buyer or operator")
	}
	claims := newClaims(userID, role, s.issuer, s.audience, s.now(), s.ttl)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", Claims{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, claims, nil
}

// Parse 校验签名、算法、issuer、audience 与过期时间，返回调用者身份。
func (s *TokenService) Parse(raw string) (Principal, error) {
	claims := &Claims{}
	parser := jwt.NewParser(
		// 显式锁定 HS256：拒绝 alg=none 以及非对称算法降级攻击。
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(s.issuer),
		jwt.WithAudience(s.audience),
		jwt.WithTimeFunc(s.now),
		jwt.WithExpirationRequired(),
	)
	_, err := parser.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) {
		return s.secret, nil
	})
	if err != nil {
		// 过期单独返回，便于调用方决定是否提示重新登录；对外仍统一 401。
		if errors.Is(err, jwt.ErrTokenExpired) {
			return Principal{}, ErrExpiredToken
		}
		return Principal{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if claims.UserID == "" || claims.Subject != claims.UserID || !claims.Role.Valid() {
		return Principal{}, ErrInvalidToken
	}
	return Principal{UserID: claims.UserID, Role: claims.Role}, nil
}

// newTokenID 生成 jti，保证每次签发的 token 可区分。
func newTokenID() string { return uuid.NewString() }
