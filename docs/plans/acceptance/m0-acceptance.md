# M0 验收记录

| 项目 | 内容 |
| --- | --- |
| 日期 | 2026-10-20 |
| 版本 | `v0.1.0-m0`（已打 tag，commit `a8af215`） |
| 环境 | Windows 11 Pro / Go 1.26.9（module `go 1.26.9`）/ WSL Ubuntu 24.04（Linux amd64）/ Docker 29.9.1 + Compose v5.6.0 |
| 结论 | §3.3 退出标准在本机全部验证通过；待推送后确认 CI 与打 tag |

## 1. 已验证

| 检查 | 命令 | 结果 |
| --- | --- | --- |
| 格式 | `cd backend && gofmt -l .` | 无输出 |
| 静态检查 | `cd backend && go vet ./...` | 通过 |
| 单元测试 | `cd backend && go test ./... -count=1` | 全部通过 |
| 构建 | `cd backend && go build ./...` | 通过 |
| 版本注入 | `go build -ldflags ...` → `./bin/api --version` | `v0.1.0-m0 (commit ea4aa08, built ...)` |
| 配置示例 | `go test ./internal/platform/configx -run TestExampleConfigsLoad` | 通过 |
| Compose 语法 | `JWT_SECRET=... PAYMENT_CALLBACK_TOKEN=... docker compose config --quiet` | 通过 |
| live | `curl localhost:8080/health/live` | `200 {"status":"ok"}` |
| ready | `curl localhost:8080/health/ready` | `200 {"status":"ok","checks":[...]}` |
| worker live | `curl localhost:8081/health/live` | `200 {"status":"ok"}` |
| 统一错误体 | `curl localhost:8080/nope` | `404 {"error":{"code":"NOT_FOUND",...}}` |
| request_id | 同上 | 响应头 `X-Request-Id` 与响应体 `request_id` 一致 |
| 签发 token | `POST /api/v1/auth/token` | 返回 token/role/user_id/expires_at |
| 身份上下文 | `GET /api/v1/me` + buyer token | 返回 token 中的 `sub` |
| 角色矩阵 | buyer 访问 `/api/v1/admin/ping` | `403` |
| 指标 | `curl localhost:8080/metrics` | 含 `http_requests_total`、`http_request_duration_seconds`、`http_requests_in_flight`、Go runtime 指标 |
| worker 管理端口 | `curl localhost:8081/health/{live,ready}`、`/metrics` | 全部 200，ready 返回 config 检查结果 |
| 限流/体限配置 | `make ci` 中中间件测试 | 通过 |
| `make ci` | `fmt-check + vet + test + build` | 退出码 0 |
| `golangci-lint` | `golangci-lint run ./...`（v2.1.6，项目 `.golangci.yml`） | `0 issues.` |
| `govulncheck` | `cd backend && govulncheck ./...`（Go 1.26.9 构建） | `No vulnerabilities found.`（0 个可达） |
| 密钥扫描 | `gitleaks detect --source . --no-git --redact` 与 git 历史模式 | 均为 `no leaks found` |
| SBOM | `syft dir:backend -o spdx-json=artifacts/sbom.spdx.json` | 已生成（约 226 KB） |
| Compose 语法 | `docker compose config --quiet` | 退出码 0 |
| `go test -race` | WSL Ubuntu 24.04 + Go 1.26.9 + gcc 13.3，`CGO_ENABLED=1 go test -race -count=1 ./...` | 全部通过 |
| SIGTERM 优雅退出 | WSL 与本机容器内 `docker compose stop -t 20 api` | 日志出现 `shutdown signal received`、`http server stopped`，容器退出码 0 |
| 版本注入 | `make build` 后 `./bin/api --version` | `8a6ab34 (commit 8a6ab34, built ...)` |
| Compose 全栈 | `GOPROXY=... docker compose up -d` | 8 个服务全部启动；mysql/redis/kafka/api/worker healthy |
| 非 root 运行 | `docker compose exec api id` | `uid=100(app) gid=101(app)` |
| Prometheus 抓取 | `/api/v1/targets` 查询 | api、worker、prometheus 三个 target 均 `health=up` |
| 指标入库 | `/api/v1/query?query=http_requests_total` | 返回带 method/route/status 标签的序列 |
| Grafana / Jaeger | `curl localhost:3000/login`、`localhost:16686` | 均 200 |
| 容器认证矩阵 | buyer → `/api/v1/me` 200、`/api/v1/admin/ping` 403；无 token 401；operator → 200 | 通过 |
| 镜像构建 | `docker compose build` | api、worker 构建成功 |
| 镜像漏洞扫描 | Trivy 0.58.1，`--severity CRITICAL,HIGH --exit-code 1` | api 与 worker 均 `Total: 0` |

## 2. 构建环境说明

| 现象 | 处理 |
| --- | --- |
| `golang:1.26.10-*` 镜像尚不存在（官方最新为 1.26.9） | `go.mod` 固定 `go 1.26.9`，Dockerfile 使用 `golang:1.26.9-alpine`；两者必须同步，否则镜像内 `go mod download` 直接失败 |
| 容器内访问 `proxy.golang.org` 报 EOF | Dockerfile 增加 `GOPROXY` 构建参数，Compose 与 Makefile 透传同名变量；默认仍为官方代理 |
| Trivy 无法下载漏洞库（`mirror.gcr.io` 不可达） | 预先用可达镜像源拉取 `aquasec/trivy-db:2` 并导入本地缓存卷，再用 `--skip-db-update --offline-scan` 扫描 |

## 3. 单元测试覆盖（本地实测）

| 包 | 覆盖率 |
| --- | --- |
| `authx` | 90.0% |
| `configx` | 81.9% |
| `healthx` | 96.6% |
| `httpserver` | 81.6% |
| `httpx` | 64.2% |
| `httpx/middleware` | 89.2% |
| `loggerx` | 67.6% |
| `observability` | 100.0% |
| `ratelimit` | 89.5% |

整体覆盖率门槛（≥60%）由 CI 统计；M0 未配置 coverage 门禁。

## 4. 范围确认

- [x] 无业务表、无迁移、无 GORM。
- [x] 无 Redis/Kafka/asynq 客户端。
- [x] 无 Kafka 消费者、无 Outbox relay。
- [x] 无限流临时实现（仅接口 + no-op）。
- [x] 无 OTel 全量埋点（仅保留配置）。
- [x] 无明文密钥（`.env`、本地配置已 gitignore；示例值为空）。

## 5. 进入 M1 前必须完成

- [x] 1. 在 Linux（WSL Ubuntu 24.04 / amd64）执行 `go test -race -count=1 ./...`，全部通过。
- [x] 2. 在 Linux 验证 SIGTERM 优雅退出：退出码 0，日志依次出现 `shutdown signal received` 与 `http server stopped`。
- [x] 3. `golangci-lint`、`govulncheck`、`gitleaks` 本地执行通过（见 §1）。
- [x] 4. 生成并归档 `artifacts/sbom.spdx.json`。
- [x] 5. 启动 Docker Desktop，执行 `docker compose up -d`，全部服务 healthy。
- [x] 6. 构建 api/worker 镜像，验证非 root（uid 100），Trivy 扫描 0 HIGH/CRITICAL。
- [x] 7. 推送仓库并确认 CI 四个 job 全绿（run `39380574773`，commit `a8af215`）。
- [x] 8. 打 tag `v0.1.0-m0` 并推送。

M0 退出标准全部完成。

## 6. M0 验收中修复的缺陷

| 缺陷 | 影响 | 处理 |
| --- | --- | --- |
| `healthx.Result.Duration` 用 `time.Duration` 序列化 | 字段名为 `duration_ms`，实际输出纳秒（5ms → `5000000`），探针会把健康检查耗时高估 10^6 倍 | 改为 `int64` 毫秒；新增 `TestReadyHandlerDurationIsMilliseconds` 锁定单位 |
| `golangci-lint` 6 个告警 | CI lint job 必红 | 修正 errcheck；对测试 server 与非凭据常量加带理由的 `nolint` |
| `govulncheck` 25 个可达漏洞 | CI security job 必红 | Go 基线升至 1.26.9，升级 x/net、x/crypto、x/sys、x/text、quic-go |
| `gitleaks` 3 处误报 | CI security job 必红 | 新增 `.gitleaks.toml`，仅放行 `.env`、SBOM 与文档示例字面量 |
| 运行镜像 openssl 有 HIGH 漏洞 | CI docker job 必红 | 运行阶段先 `apk upgrade` 再安装依赖 |
