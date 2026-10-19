// Package authx 实现本地模拟 JWT 认证与角色授权。
//
// 范围（需求 FR-024/FR-025、ADR-0007）：
//   - HS256 签名，密钥至少 32 字节；
//   - Claims 固定包含 sub、role、iss、aud、exp、iat、jti；
//   - 签发端点只在 local/ci 环境启用；
//   - 用户身份只取自 token，绝不取自请求体。
//
// 这不是生产认证方案：没有用户库、密码或刷新令牌，仅用于本地验收与测试。
package authx

import (
	"context"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Role 是授权角色，取值受控。
type Role string

const (
	// RoleBuyer 可以创建、查询、取消自己的订单并提交秒杀。
	RoleBuyer Role = "buyer"
	// RoleOperator 可以管理 SKU、活动、死信、对账与审计。
	RoleOperator Role = "operator"
)

// Valid 报告角色是否属于受控集合。
func (r Role) Valid() bool {
	return r == RoleBuyer || r == RoleOperator
}

// Claims 是签发的 JWT 负载。嵌入 jwt.RegisteredClaims 以复用标准校验
// （过期、签发时间、issuer、audience）。
type Claims struct {
	Role Role `json:"role"`
	// UserID 与 RegisteredClaims.Subject 保持一致；单独暴露便于日志与审计读取。
	UserID string `json:"user_id"`
	jwt.RegisteredClaims
}

// Principal 是从 token 解析出的调用者身份，放入 context 供 handler 使用。
type Principal struct {
	UserID string
	Role   Role
}

type principalKey struct{}

// WithPrincipal 把身份写入 context。
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// CurrentUser 返回当前调用者；未认证时 ok=false。
func CurrentUser(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// newClaims 构造标准 claims；jti 由调用方保证唯一。
func newClaims(userID string, role Role, issuer, audience string, now time.Time, ttl time.Duration) Claims {
	return Claims{
		Role:   role,
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Issuer:    issuer,
			Audience:  jwt.ClaimStrings{audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			ID:        newTokenID(),
		},
	}
}
