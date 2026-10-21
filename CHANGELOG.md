# Changelog

格式参考 Keep a Changelog；版本号遵循 SemVer。里程碑 tag 形如 `v0.1.0-m0`。

## [Unreleased]

### Changed

- Go 版本基线由 1.26.9 升至 **1.26.10**（`go.mod`、Dockerfile 同步）。原因：
  `crypto/x509` 的 `GO-2026-6809`（不信任受损的中间证书）在 1.26.10 修复，
  govulncheck 判定为**可达**漏洞（路径经 `httpx.RunServer` → `x509.Certificate.Verify`）。
  M0 时该补丁版本尚不存在于镜像仓库，因此当时停在 1.26.9；现已可用。
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

- M1 业务模块骨架：`internal/{order,inventory,payment}/{domain,app,infra,http}` 与
  `internal/platform/db`，每个包带 package doc 说明职责与允许的导入方向。
- `internal/archtest`：把 ADR-0002 的分层规则写成测试，检查所有业务模块的直接
  import。`domain` 只许标准库；`app` 不得 import 本模块 `infra`/`http`、其他模块的
  `infra`/`http`，也不得直接依赖 gin/gorm/viper/kafka-go/redis/asynq；`platform`
  不得依赖任何业务模块。三条规则都用临时违规文件验证过确实会失败。
- M1 依赖锁定：`gorm.io/gorm v1.31.2`、`gorm.io/driver/mysql v1.6.0`、
  `golang-migrate/migrate/v4 v4.20.1`、`testcontainers-go v0.44.0`（含 mysql 模块）。
  通过带 `//go:build tools` 标签的 `backend/tools.go` 固定，避免 `go mod tidy`
  把它们当未使用依赖删除。
- `make test-integration`：以 `-tags=integration` 运行集成测试，与默认单元测试隔离。
- M1 初始 schema（`backend/migrations/000001_init.{up,down}.sql`）：`skus`、
  `inventories`（含守恒 CHECK）、`orders`、`order_items`、`reservations`、
  `reservation_items`、`payments`、`idempotency_records`、`outbox_events`。
  统一 `utf8mb4_unicode_ci`、`DATETIME(6)`、金额 `BIGINT UNSIGNED`；跨模块只存 ID 不建外键。
- `internal/platform/testdb`（integration tag）：testcontainers 启动 `mysql:8.4.3`，
  每个测试独立 schema，提供迁移 up/down/version 与表清单查询。6 个迁移测试覆盖
  up→down→up、字符集、守恒 CHECK、唯一键、Outbox 索引列序、列类型。
- [数据库迁移手册](docs/runbooks/migrate.md)：执行方式、`dirty` 处置、禁止事项、
  新增迁移检查清单。
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
