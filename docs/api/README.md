# API 说明

完整契约以需求规格 §8 为准（`docs/plans/2026-10-18-order-fulfillment-requirements.md`）。
M0 只实现基础设施端点；业务端点在 M1 起按 §8.2 逐个补充 schema 与示例。

## 通用约定

- 前缀：`/api/v1`（探针与指标不带前缀）。
- 认证：`Authorization: Bearer <JWT>`；用户身份取自 token `sub`。
- 请求 ID：可传 `X-Request-ID`，服务端缺失时生成，响应头与响应体使用同一值。
- 时间：RFC 3339 UTC；金额：整数最小货币单位。
- 成功：`{"request_id":"...","data":...}`
- 错误：`{"request_id":"...","error":{"code":"...","message":"...","details":{...}}}`

## M0 端点

| 方法与路径 | 认证 | 说明 |
| --- | --- | --- |
| `POST /api/v1/auth/token` | 无 | 本地模拟签发；仅 `APP_ENV=local\|ci` 且 `LOCAL_AUTH_ENABLED=true` |
| `GET /api/v1/me` | Bearer | 返回当前用户与角色，用于验证身份上下文 |
| `GET /api/v1/admin/ping` | Bearer + operator | 验证角色矩阵；buyer 返回 403 |
| `GET /health/live` | 无 | 仅检查进程 |
| `GET /health/ready` | 无 | 关键依赖失败 503；非关键失败 200 + `degraded` |
| `GET /metrics` | 无 | Prometheus 文本格式 |

### 签发 token

```http
POST /api/v1/auth/token
Content-Type: application/json

{"user_id":"u1","role":"buyer"}
```

```json
{
  "request_id": "req-01",
  "data": {
    "token": "<jwt>",
    "role": "buyer",
    "user_id": "u1",
    "expires_at": "2026-10-19T13:00:00Z"
  }
}
```

### 健康检查

```json
{
  "request_id": "req-02",
  "data": {
    "status": "ok",
    "checks": [
      {"name": "config", "status": "ok", "critical": true, "duration_ms": 0}
    ]
  }
}
```

`status` 取值：`ok` / `degraded` / `fail`。

## 错误码

见需求 §8.3。M0 额外登记 `PAYLOAD_TOO_LARGE`（413），见
[ADR-0010](../adr/0010-error-model.md)。

## 后续

M1 补充：SKU、订单、取消、支付回调的请求/响应 schema 与幂等语义示例。
