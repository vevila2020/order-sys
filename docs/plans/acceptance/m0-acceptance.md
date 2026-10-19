# M0 验收记录

| 项目 | 内容 |
| --- | --- |
| 日期 | 2026-10-19 |
| 版本 | `v0.1.0-m0`（代码完成，tag 待打） |
| 环境 | Windows 11 Pro / Go 1.26.0（module `go 1.25.0`）/ Docker CLI 29.6.1 |
| 结论 | 代码与文档完成；依赖 Docker 守护进程与 make 的验收项未执行 |

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

## 2. 未验证（受环境限制）

| 检查 | 原因 | 后续动作 |
| --- | --- | --- |
| `make setup/up/down/...` | 当前 Windows 环境未安装 make | 在安装 GNU Make 的环境或 WSL 执行 |
| `make up` 拉起全部依赖 | Docker CLI 存在但 Docker Desktop 守护进程未运行 | 启动 Docker Desktop 后执行 `make setup && make up` |
| 镜像构建与非 root 检查 | 同上，`docker build` 无法连接 daemon | 启动 daemon 后执行 `make docker-build` |
| `go test -race` | Windows 无 gcc，`-race` 需要 CGO | Linux CI 执行 |
| SIGTERM 优雅退出 | Windows 下 Git Bash 的 kill 走 TerminateProcess，进程无法捕获 SIGTERM（退出码 143） | 在 Linux 容器/CI 验证；单元测试已覆盖 Shutdown 等待与超时 |
| Prometheus 抓取 API | 依赖 Compose 运行 | `make up` 后访问 Prometheus targets 页面 |
| CI 工作流实际运行 | 仓库未配置 remote，无法触发 Actions | 推送后检查四个 job |

## 3. 单元测试覆盖（本地实测）

| 包 | 覆盖率 |
| --- | --- |
| `authx` | 90.0% |
| `configx` | 81.9% |
| `healthx` | 82.8% |
| `httpserver` | 87.9% |
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

1. 启动 Docker Desktop，执行 `make setup && make up`，确认全部服务 healthy。
2. 在 Linux/CI 执行 `go test -race -count=1 ./...`。
3. 在 Linux 容器验证 SIGTERM 优雅退出与退出码 0。
4. 推送仓库并确认 CI 四个 job 全绿。
5. 归档 SBOM 与镜像扫描结果。
6. 打 tag `v0.1.0-m0`。
