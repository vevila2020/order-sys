# ADR-0010: 错误模型与错误码

| 项目 | 内容 |
| --- | --- |
| 状态 | Accepted |
| 日期 | 2026-10-19 |
| 关联需求 | §8.1、§8.3、FR-024、NFR-005 |

## 背景

客户端需要可靠地区分"参数错误""未认证""冲突""依赖故障"等情况，以便决定
重试、刷新凭证还是提示用户。若每个 handler 自己拼错误体，会出现状态码不一致、
泄露内部堆栈、客户端无法稳定判断的问题。

## 决策

- 统一响应体：
  - 成功：`{"request_id":"...","data":...}`
  - 错误：`{"request_id":"...","error":{"code":"...","message":"...","details":{...}}}`
- 错误码是**稳定契约**：只新增，不修改、不删除。
- `AppError` 同时携带对外信息（`Code`/`Message`/`Details`）与服务端原因
  （未导出 `err`，仅用于日志与 `errors.Is/As`）。
- 业务层返回 `*AppError` 或其哨兵错误；handler 调 `WriteErrorFrom` 统一映射。
- 未知错误一律折叠为 500 `INTERNAL`，不返回堆栈、SQL 或 DSN。
- `context.DeadlineExceeded` / `context.Canceled` 映射为 504 `TIMEOUT`。
- 限流与"处理中"冲突通过 `RetryAfter` 写入 `Retry-After` 头。
- `message` 只用于展示，客户端应以 `code` 判断。

实现位置：`backend/internal/platform/httpx`（错误模型与响应）、
`backend/internal/platform/httpx/middleware`（panic、超时、超限）。

M0 额外登记了 `PAYLOAD_TOO_LARGE`（413）。需求 §8.3 未列出该码，但要求
"超过请求体上限返回 413"；新增错误码属于兼容变更，记录于此。

## 备选方案

- **每个 handler 直接 `c.JSON(status, gin.H{"error": ...})`。** 拒绝：无法保证
  一致性与不泄露内部信息。
- **用 HTTP 状态码单独表达错误。** 拒绝：409 下有多种语义，客户端无法区分。
- **返回详细内部错误便于调试。** 拒绝：违反 NFR-005；调试信息应进日志。

## 后果

- 正面：客户端行为稳定；错误不泄露内部实现；新增错误码成本低。
- 负面：需要维护错误码表；开发时必须在 `AppError` 与内部错误之间显式转换。

## 验证方式

- 表格驱动测试覆盖 §8.3 全部错误码的状态映射。
- 测试断言内部错误响应不含 `SELECT`、`dsn=` 等敏感片段。
- 中间件测试断言 panic、超时、超限都返回统一错误体。

## 关联

- [ADR-0007](0007-local-jwt-auth.md)
- 需求规格 §8.1、§8.3、§8.4
