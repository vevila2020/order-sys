# 本地开发运行手册

## 前置条件

| 工具 | 版本 | 用途 |
| --- | --- | --- |
| Go | 1.26+ | 构建与测试（`backend/go.mod` 为准） |
| Docker + Compose | 最新稳定版 | 本地依赖环境 |
| make | GNU Make | 统一命令入口（Windows 可用 Git Bash + make 或 WSL） |
| curl | 任意 | 接口验证 |
| openssl | 任意 | `make setup` 生成随机密钥 |

选装：`golangci-lint`、`govulncheck`、`gitleaks`、`syft`（`make lint/security/sbom` 使用）。

## 首次启动

```bash
cp backend/.env.example .env   # 或 make setup（会填充随机密钥）
make setup
make up
docker compose ps
curl -s localhost:8080/health/live
curl -s localhost:8080/health/ready
```

`make up` 会构建 api/worker 镜像并启动 MySQL、Redis、Kafka、Prometheus、
Grafana、Jaeger。首次启动 MySQL 初始化需要约 20–40 秒。

## 本地签发 token 与验证权限

```bash
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/token \
  -H 'Content-Type: application/json' \
  -d '{"user_id":"u1","role":"buyer"}' | jq -r .data.token)

curl -s localhost:8080/api/v1/me -H "Authorization: Bearer $TOKEN"
curl -s -o /dev/null -w '%{http_code}\n' \
  localhost:8080/api/v1/admin/ping -H "Authorization: Bearer $TOKEN"   # 预期 403
```

## 常用命令

| 命令 | 说明 |
| --- | --- |
| `make help` | 列出目标 |
| `make fmt` / `make fmt-check` | 格式化 / 检查格式 |
| `make vet` / `make lint` | 静态检查 |
| `make test` / `make test-race` | 单元测试（race 需要 CGO 与 C 编译器） |
| `make build` | 构建到 `backend/bin/` |
| `make run-api` / `make run-worker` | 本地直接运行（需先 `make up` 提供依赖） |
| `make logs` | 跟踪 api/worker 日志 |
| `make down` | 停止服务，保留数据卷 |
| `make security` / `make sbom` | 安全扫描 / 生成 SBOM |

## 端口

| 端口 | 服务 | 暴露范围 |
| --- | --- | --- |
| 8080 | api | 宿主机 |
| 8081 | worker 管理端口 | 仅容器网络（本地直连时用 `make run-worker`） |
| 3000 | Grafana | 宿主机 |
| 16686 | Jaeger UI | 宿主机 |
| 4317 | OTLP gRPC | 宿主机 |
| 3306 / 6379 / 9092 / 9090 | MySQL / Redis / Kafka / Prometheus | 仅容器网络 |

端口冲突时修改 `docker-compose.yml` 的 `ports` 映射或 `HTTP_ADDR`。

## 日志

结构化 JSON，按关联 ID 检索：

```bash
docker compose logs api | jq 'select(.request_id=="<id>")'
docker compose logs api | jq 'select(.level=="error")'
```

## 数据与清理

- 只有 `mysql-data` 卷需要备份；Redis/Kafka 数据可重建。
- `make down` 不删除卷；需要重建时执行 `docker compose down -v`（会丢数据）。
- 不使用 `docker compose down -v` 清理日常问题。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| `JWT_SECRET must be set via make setup` | 未生成 `.env`，执行 `make setup` |
| 启动报 `jwt.secret is required` | `APP_ENV` 非 local 且未注入密钥 |
| `/health/ready` 返回 503 | 关键检查失败；看响应体 `checks[].error` |
| 端口被占用 | 修改 Compose 端口映射或 `HTTP_ADDR` |
| Windows 下 `make` 不存在 | 使用 Git Bash 安装 make，或改用 WSL |
| `go test -race` 报 cgo 错误 | Windows 默认无 gcc；CI/Linux 执行 race 测试 |

## 安全注意

- `.env`、`config/local.yaml`、`config/ci.yaml` 已被 gitignore。
- 本地默认 JWT 密钥仅用于开发；任何非 local 环境必须显式注入密钥。
- 不要将 `.env` 提交到仓库；`gitleaks` 是 CI 门禁。
