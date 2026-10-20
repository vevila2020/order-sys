# order-sys

Go 电商订单与秒杀后端，按企业级交付标准组织：明确的模块边界、事务与并发控制、
可靠异步事件、结构化日志与指标、CI 门禁和运行手册。

> 学习型项目：不处理真实资金，不接真实支付渠道，不承诺生产 SLA。

## 快速开始（约 5 分钟）

前置：Go 1.26+、Docker Compose、make、curl。

```bash
make setup        # 生成 .env 并填充随机密钥（不覆盖已存在的文件）
make up           # 构建并启动 api/worker 与依赖
docker compose ps # 等待服务 healthy
curl -s localhost:8080/health/live
```

验证认证与权限边界：

```bash
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/token \
  -H 'Content-Type: application/json' \
  -d '{"user_id":"u1","role":"buyer"}' | jq -r .data.token)

curl -s localhost:8080/api/v1/me -H "Authorization: Bearer $TOKEN"
curl -s -o /dev/null -w '%{http_code}\n' \
  localhost:8080/api/v1/admin/ping -H "Authorization: Bearer $TOKEN"  # 403
```

本地地址：

| 服务 | 地址 |
| --- | --- |
| API | http://localhost:8080 |
| 健康检查 | /health/live、/health/ready |
| 指标 | http://localhost:8080/metrics |
| Grafana | http://localhost:3000 |
| Jaeger | http://localhost:16686 |

停止：`make down`（保留数据卷）。更多见 [本地开发手册](docs/runbooks/local-dev.md)。

## 当前进度（M0 工程基线）

已完成：

- Go module、目录骨架、版本信息（`internal/version`）。
- 配置：Viper + YAML 默认值 + 环境变量覆盖 + 启动校验，敏感项只从环境变量读取。
- 日志：zap JSON，统一 `service_name/env/version` 与请求关联字段。
- HTTP：Gin 服务、统一成功/错误响应、稳定错误码、`request_id`。
- 中间件链：request_id → recover → access log → metrics → timeout → auth → role → ratelimit → body limit。
- 认证：本地 JWT 签发与校验、buyer/operator 角色，签发端点在非 local/ci 关闭。
- 健康检查：live/ready，支持关键/非关键依赖与 `degraded`。
- 指标：Prometheus 骨架、HTTP 指标与 Go runtime 指标。
- 优雅关闭：SIGTERM/SIGINT，宽限期可配置。
- 限流：接口 + no-op 实现（Redis 实现属于 M3）。
- 本地环境：Compose（MySQL/Redis/Kafka/Prometheus/Grafana/Jaeger）+ api/worker。
- 工程配套：多阶段非 root 镜像、Makefile、CI、ADR、运行手册、PR 模板。

**明确未做**（按计划属于后续里程碑）：业务表与迁移、GORM、订单/库存/支付/秒杀
用例、Redis/Kafka/asynq 客户端、Outbox relay、OpenTelemetry 埋点、压测与故障演练。

## 仓库结构

```text
backend/
├── cmd/api/            # HTTP 进程
├── cmd/worker/         # 后台任务进程（M0 仅管理端口）
├── internal/
│   ├── platform/       # 跨模块基础设施，不放业务规则
│   │   ├── authx/      # 本地 JWT 与角色
│   │   ├── configx/    # 配置加载与校验
│   │   ├── ctxkeys/    # context 关联标识键
│   │   ├── healthx/    # live/ready
│   │   ├── httpserver/ # 引擎与路由装配
│   │   ├── httpx/      # 响应、错误模型、优雅关闭
│   │   ├── loggerx/    # 结构化日志
│   │   ├── observability/ # Prometheus
│   │   └── ratelimit/  # 限流接口与 no-op
│   └── version/
├── config/             # 示例配置（敏感项留空）
└── Dockerfile
deploy/                 # Prometheus / Grafana / Kafka 配置
docs/                   # 需求、计划、ADR、运行手册、API
```

业务模块（`order`、`inventory`、`payment`、`flashsale`、`notification`）在 M1 创建。

## 开发命令

| 命令 | 说明 |
| --- | --- |
| `make help` | 列出全部目标 |
| `make test` / `make test-race` | 单元测试 / 竞态检测 |
| `make fmt` / `make fmt-check` / `make vet` / `make lint` | 格式与静态检查 |
| `make build` | 构建 api 与 worker |
| `make ci` | 本地执行 CI 核心检查 |
| `make security` / `make sbom` | 安全扫描 / SBOM |
| `make up` / `make down` / `make logs` | 本地环境 |

## 文档

- [需求规格 v3.1](docs/plans/2026-10-18-order-fulfillment-requirements.md)
- [M0 实施计划](docs/plans/2026-10-19-order-fulfillment-m0-implementation-plan.md)
- [ADR 索引](docs/adr/README.md)
- [本地开发手册](docs/runbooks/local-dev.md)
- [CI 手册](docs/runbooks/ci.md)
- [API 说明](docs/api/README.md)

## 技术栈

Go 1.26 · Gin · Viper · zap · JWT v5 · Prometheus · google/uuid；
GORM + golang-migrate（M1）、Redis + go-redis（M3）、Kafka + kafka-go（M3）、
asynq（M3）、OpenTelemetry（M5）。选型全周期冻结，见 [ADR-0001](docs/adr/0001-freeze-technology-stack.md)。
