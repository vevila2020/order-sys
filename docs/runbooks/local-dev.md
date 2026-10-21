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

## 连接远程 MySQL（SSH 隧道）

本地开发也可以直接连服务器上的 MySQL，而不启动 Compose 里的 MySQL。
SQL 客户端（Navicat）能连不代表 Go 进程能连：Navicat 自己维护 SSH 隧道，
Go 进程不会。二者都需要一条独立的隧道。

### 认证前提：私钥有 passphrase

服务器只开密钥登录时，`ssh` 在非交互模式下无法应答 passphrase，会直接报
`Permission denied (publickey)`——即使服务器已经接受了该公钥。解决方式是
让 ssh-agent 代为签名，口令只需要输入一次。

```powershell
# 管理员 PowerShell：启用并开机自启
Set-Service ssh-agent -StartupType Automatic
Start-Service ssh-agent

# 普通 PowerShell：加载私钥（会提示输入一次 passphrase）
ssh-add C:\LGF\oracleK\vps-rn
ssh-add -l          # 应能看到 ED25519 SHA256:UxGB16mDJoDG9WN8hSZ/D/yYbNqpNYRG1/UsVpxMUgw
```

私钥路径不要写进 `.env`、Compose 或 Makefile；它比数据库密码权限更高，
一旦进入容器或日志就不可控。

### 启动 / 查看 / 停止隧道

```powershell
.\scripts\ssh-tunnel.ps1 -Action Start  -RemoteHost 23.238.26.42 -RemotePort 2233
.\scripts\ssh-tunnel.ps1 -Action Status -RemoteHost 23.238.26.42 -RemotePort 2233
.\scripts\ssh-tunnel.ps1 -Action Stop   -RemoteHost 23.238.26.42 -RemotePort 2233
```

隧道只绑定 `127.0.0.1:13306`（可用 `-LocalPort` 改），不会把 MySQL 暴露到局域网。
脚本默认走 ssh-agent；也可以用 `-KeyFile` 指定私钥，但带 passphrase 时仍需 agent。

### 环境差异：本地开发 vs 服务器部署

| 场景 | DSN | 是否需要隧道 |
| --- | --- | --- |
| Windows 本地开发，连远程库 | `dev:<密码>@tcp(127.0.0.1:13306)/order_sys?...` | 需要 |
| Windows 本地开发，用 Compose MySQL | `order:order@tcp(localhost:3306)/order_sys?...` | 不需要 |
| Debian 服务器部署 | `dev:<密码>@tcp(127.0.0.1:3306)/order_sys?...` | 不需要（同机直连） |

主机名必须写 `127.0.0.1` 而不是 `localhost`：Go 的 MySQL 驱动走 TCP，
`localhost` 可能解析到 `::1`，而隧道默认只监听 IPv4。

### 数据库账号

隧道连进来的源地址是 `127.0.0.1`，因此 MySQL 侧要为该来源授权；
只授权 `'dev'@'localhost'` 是不够的（那是 unix socket 的来源）。

```sql
SHOW DATABASES LIKE 'order_sys';
SELECT user, host FROM mysql.user WHERE user = 'dev';

CREATE USER 'dev'@'localhost'  IDENTIFIED BY '<强密码>';
CREATE USER 'dev'@'127.0.0.1' IDENTIFIED BY '<强密码>';
GRANT ALL PRIVILEGES ON order_sys.* TO 'dev'@'localhost';
GRANT ALL PRIVILEGES ON order_sys.* TO 'dev'@'127.0.0.1';
FLUSH PRIVILEGES;

SHOW GRANTS FOR 'dev'@'127.0.0.1';
```

`SELECT ... FROM mysql.user WHERE user='dev'` 返回 `Empty set` 表示用户不存在，
不是权限不足；用户不存在时 `SHOW DATABASES` 会连库名都隐藏。

### 排序规则必须与服务器一致

项目统一使用 `utf8mb4_unicode_ci`，与部署服务器保持一致。
`utf8mb4_0900_ai_ci` 是 MySQL 8 的默认排序规则，但与 `utf8mb4_unicode_ci`
在重音、大小写等价集合上不同，**不能在开发库与生产库之间混用**，
否则同一查询在两边可能得到不同结果。

## 常用命令

| 命令 | 说明 |
| --- | --- |
| `make help` | 列出目标 |
| `make fmt` / `make fmt-check` | 格式化 / 检查格式 |
| `make vet` / `make lint` | 静态检查 |
| `make test` / `make test-race` | 单元测试（race 需要 CGO 与 C 编译器） |
| `make test-integration` | 集成测试，`-tags=integration`，需要 Docker |
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
| 隧道启动即退出，报 `Permission denied (publickey)` | 私钥有 passphrase 但未加载到 ssh-agent；执行 `ssh-add <私钥>`。也确认 `ssh-add -l` 能列出密钥 |
| `ssh-add -l` 报 `Could not open a connection to your authentication agent` | `ssh-agent` 服务未运行：`Set-Service ssh-agent -StartupType Automatic; Start-Service ssh-agent` |
| `ssh-add -l` 能列出密钥，但隧道仍报 publickey 失败 | PATH 里命中 Git 的 `ssh.exe`，它不认 Windows 的 ssh-agent。脚本已优先使用 `System32\OpenSSH\ssh.exe`；手动验证时也要用该路径，或 `Set-Alias ssh` 指向它 |
| 隧道正常但 Go 报 `connection refused` | DSN 写了 `localhost` 而非 `127.0.0.1`，或隧道端口与 DSN 不一致 |
| `dev` 用户 `SHOW DATABASES` 看不到库 | 该用户不存在或未对 `127.0.0.1` 来源授权，见上文授权 SQL |
| `go: updates to go.mod needed` | 新增了未被 import 的依赖；见下节"依赖与 tools.go" |

## 依赖与 tools.go

`backend/tools.go` 带 `//go:build tools` 标签，只为把已决策、尚未被生产代码 import
的依赖（GORM、golang-migrate、testcontainers）钉在 `go.mod` 里。

- 它不参与构建：`go build`、`go vet`、`golangci-lint` 都看不到它。
- 但 `go mod tidy` 会把它算进依赖图，因此不会把这些库当"未使用"删掉。
- 直接 `go build ./...` 后再 `go mod tidy` 不会产生 diff。

**维护：** T04 / T03 / T17 分别落地 GORM、migrate、testcontainers 的真实 import 后，
删掉 `tools.go` 里对应的行；文件清空即可删除。若发现某条依赖始终没有被真实 import，
说明对应任务没做完，不要在 `tools.go` 里长期留着它。

## 集成测试

集成测试用 build tag 隔离：

```powershell
# 只跑单元测试（不需要 Docker）
cd backend; go test ./... -count=1

# 跑集成测试（需要 Docker）
make test-integration
```

**不要用远程开发库跑集成测试。** 集成测试会执行迁移、建表、写数据；用
`127.0.0.1:13306` 那条隧道去跑会污染服务器的 `order_sys` 库。集成测试使用
testcontainers 启动的临时 MySQL 容器。

### 唯一的例外：真实库连通性检查

`TestOpenAgainstConfiguredDSN` 读取 `MYSQL_DSN` 验证隧道链路，未设置时自动跳过。
它**只读**（查版本与库排序规则），不建表、不写数据、不跑迁移，可以安全指向开发库：

```powershell
# 先启动隧道，然后从 .env 取 DSN 注入环境变量
.\scripts\ssh-tunnel.ps1 -Action Start -RemoteHost 23.238.26.42 -RemotePort 2233
cd backend
$env:MYSQL_DSN = (Get-Content ..\.env | Where-Object { $_ -match '^MYSQL_DSN=' }) -replace '^MYSQL_DSN=', '' -replace '"', ''
go test -tags=integration ./internal/platform/db -run TestOpenAgainstConfiguredDSN -v
```

通过时应打印实际连接的 MySQL 版本与 `utf8mb4_unicode_ci`。若报 `1045 Access denied`，
说明密码与服务器上的 `dev` 账号不一致，而不是隧道或驱动的问题。

## 安全注意

- `.env`、`config/local.yaml`、`config/ci.yaml` 已被 gitignore。
- 本地默认 JWT 密钥仅用于开发；任何非 local 环境必须显式注入密钥。
- 不要将 `.env` 提交到仓库；`gitleaks` 是 CI 门禁。
