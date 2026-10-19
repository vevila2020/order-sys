# ADR-0007: 本地 JWT 认证与角色

| 项目 | 内容 |
| --- | --- |
| 状态 | Accepted |
| 日期 | 2026-10-19 |
| 关联需求 | §8.1、FR-024、FR-025、NFR-005 |

## 背景

项目需要区分买家与运营者权限，但不接入真实身份系统。需要一个足够简单、
可在本地签发、又能验证鉴权边界的方案，同时不能在生产环境意外开放签发端点。

## 决策

- 使用 `golang-jwt/jwt v5`，HS256 签名，密钥至少 32 字节。
- Claims：`sub`、`role`、`iss`、`aud`、`exp`、`iat`、`jti`。
  `iss=order-sys`，`aud=order-sys-api`，校验时两者都必须匹配。
- 显式锁定 `HS256`，拒绝 `alg=none` 与非对称算法降级。
- 角色仅 `buyer`、`operator`；授权矩阵见需求 §8.1。
- `POST /api/v1/auth/token` 仅在 `APP_ENV` 为 `local`/`ci` 且
  `LOCAL_AUTH_ENABLED=true` 时挂载；双重校验，设备遗漏一层也不会误开放。
- 非本地环境访问签发端点返回 404，不暴露"端点存在但被禁用"。
- 用户身份只取自 token 的 `sub`，绝不取自请求体。
- 认证失败统一返回 401，不区分"用户不存在"与"密钥错误"。

实现位置：`backend/internal/platform/authx`。

## 备选方案

- **固定静态 API Key。** 拒绝：无法表达角色与过期，也验证不了鉴权中间件。
- **接入真实 OIDC 提供方。** 拒绝：超出项目范围，且引入外部依赖。
- **session + cookie。** 拒绝：API 优先，Bearer token 更适合压测与脚本。

## 后果

- 正面：零外部依赖即可验收权限矩阵；安全边界通过配置与测试双重保证。
- 负面：不是生产级认证（无用户库、刷新令牌、密钥轮换），仅适用于本地学习环境。
- 密钥泄露风险由"仅从环境变量读取 + local 环境才允许默认值"控制。

## 验证方式

- `authx` 单元测试覆盖：过期、签名错误、`alg=none`、错误 `iss`/`aud`、
  角色矩阵、端点关闭。
- `configx` 校验：非 local/ci 开启 `LOCAL_AUTH_ENABLED` 时启动失败。
- 端到端：buyer 访问 `/api/v1/admin/*` 返回 403。

## 关联

- [ADR-0010](0010-error-model.md)
- 需求规格 §8.1、§9.4
