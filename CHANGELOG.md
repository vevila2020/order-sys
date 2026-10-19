# Changelog

格式参考 Keep a Changelog；版本号遵循 SemVer。里程碑 tag 形如 `v0.1.0-m0`。

## [Unreleased]

## [0.1.0-m0] - 2026-10-19

### Added

- Go module 骨架、`cmd/api` 与 `cmd/worker` 入口、版本信息注入。
- 配置加载与校验（Viper：YAML 默认值 + 环境变量覆盖 + 启动校验；敏感项仅环境变量）。
- 结构化 JSON 日志（zap），统一 `service_name`/`env`/`version` 与请求关联字段。
- 统一 HTTP 响应与错误模型，覆盖需求 §8.3 错误码；新增 `PAYLOAD_TOO_LARGE`(413)。
- 中间件链：request_id、recover、访问日志、指标、超时、认证、角色、限流、请求体上限。
- 本地 JWT 签发与校验、buyer/operator 角色；签发端点仅在 local/ci 且显式开启时可用。
- `/health/live`、`/health/ready`，支持关键/非关键依赖与 `degraded`。
- Prometheus 指标骨架：HTTP 请求量/延迟/在途数与 Go runtime 指标。
- 优雅关闭（SIGTERM/SIGINT，宽限期可配置，确定性退出码）。
- 限流接口与 no-op 实现（Redis 令牌桶属于 M3）。
- Docker Compose 本地环境（MySQL、Redis、Kafka、Prometheus、Grafana、Jaeger）与 api/worker。
- 多阶段非 root 镜像、Makefile、GitHub Actions CI、ADR 0001/0002/0003/0007/0010。
- 运行手册、API 说明、PR 模板。

### Notes

- M0 不含业务逻辑、业务表、Kafka 消费者与任何临时替代实现。
- `go test -race` 需要 CGO；Windows 本地不满足时以 Linux CI 结果为准。
