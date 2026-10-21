# Changelog

格式参考 Keep a Changelog；版本号遵循 SemVer。里程碑 tag 形如 `v0.1.0-m0`。

## [Unreleased]

### Changed

- Go 版本基线由 1.25.x 升至 1.26.9（`go.mod`、Dockerfile、README、ADR-0001、需求 §9.1 同步）。
  原因：`net/http`、`crypto/tls`、`crypto/x509` 等标准库漏洞的修复版本为 Go 1.26.9+，
  而 `golang.org/x/net@v0.60.0`（修复 4 个漏洞）要求 go ≥ 1.26。
- 升级间接依赖：`golang.org/x/net` v0.48.0 → v0.60.0、`golang.org/x/crypto` → v0.57.0、
  `golang.org/x/sys` → v0.48.0、`golang.org/x/text` → v0.42.0、
  `github.com/quic-go/quic-go` v0.59.0 → v0.59.1。
  `govulncheck ./...` 现为 0 个可达漏洞。

### Fixed

- **`/health/ready` 的 `duration_ms` 单位错误**：`healthx.Result.Duration` 原为
  `time.Duration`，JSON 编码输出纳秒（5ms 变成 `5000000`），而字段名声明是毫秒。
  现改为 `int64` 毫秒，并新增 `TestReadyHandlerDurationIsMilliseconds` 锁定单位。
- 运行镜像的 openssl 存在 HIGH 漏洞（CVE-2026-84782）：运行阶段先 `apk upgrade` 再装依赖，
  Trivy 对 api/worker 镜像现为 `Total: 0`。
- `golangci-lint run ./...` 清零：修正 `httpx/shutdown_test.go` 的 errcheck 与 gosec 告警，
  并为 `authx.TokenAudience`（JWT aud 声明值，非凭据）添加带理由的 `nolint`。
  此前 lint job 会因 6 个问题失败。
- 新增 `.gitleaks.toml`：只放行 `.env`、`artifacts/`（SBOM）与需求文档中的示例
  Idempotency-Key 字面量；真实凭据仍会被拦截。
- 修正 `gofmt` 对 `authx/jwt.go` 的 import 分组。

### Added

- `ADR-0003 事务与并发控制` 由 Proposed 转为 **Accepted**：固定 `TransactionManager.Do`
  签名、条件更新与 `RowsAffected` 语义、按 `sku_id` 字典序加锁、1213/1205 重试
  （3 次，10/20/40ms + ±20% 抖动）、耗尽映射为 `DEPENDENCY_UNAVAILABLE`。
- 新增 `ADR-0006 幂等键与响应快照`（Accepted）：固定 `(scope, user_id, key)` 键格式、
  规范化 JSON 的 SHA-256 指纹、单事务内的 `IN_PROGRESS → COMPLETED` 生命周期，以及
  并发同键由唯一索引排队产生的三条映射。
- Dockerfile 新增 `GOPROXY` 构建参数（默认官方代理），Compose 与 Makefile 透传同名变量，
  便于在官方代理不可达的环境中构建镜像。

### CI

- `golangci-lint` 由 `v2.1.6` 升至 `v2.15.0`：旧版无法分析 Go 1.26 语法，
  以退出码 3 整体失败。同时修复它新报出的 `errcheck`（`shutdown_test.go` 未处理
  `Shutdown` 返回值）。
- `security` job 改为直接下载 gitleaks `v8.30.1` 二进制：`gitleaks/gitleaks-action@v2`
  要求组织账号提供商用 license。
- `docker` job 的 Trivy action 修正为 `aquasecurity/trivy-action@v0.36.0`
  （原 `0.28.0` 缺少 `v` 前缀，GitHub 无法解析）。

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
