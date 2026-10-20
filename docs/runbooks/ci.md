# CI 运行手册

## 流水线结构

`.github/workflows/ci.yml` 包含四个并行 job：

| Job | 内容 | 失败含义 |
| --- | --- | --- |
| `lint` | `gofmt -l`、`go vet`、`golangci-lint` | 格式或静态问题 |
| `test` | `go test -race -count=1 ./...`、`go build ./...` | 测试失败或竞态 |
| `security` | `govulncheck`、`gitleaks`、SBOM | 漏洞、明文密钥 |
| `docker` | 构建 api/worker 镜像、Trivy 扫描 | 镜像不可构建或存在高危漏洞 |

## 本地复现

```bash
make ci          # 格式 + vet + 单测 + 构建
make security    # govulncheck + gitleaks（需本地安装）
make sbom        # 生成 artifacts/sbom.spdx.json
```

`-race` 在 Windows 上需要 CGO 与 C 编译器。Windows 默认没有 gcc，可改用 WSL：

```bash
# Debian / Ubuntu（WSL 同样适用）
sudo apt-get install -y gcc
CGO_ENABLED=1 go test -race -count=1 ./...
```

## 工具安装

```bash
go install golang.org/x/vuln/cmd/govulncheck@latest
go install github.com/anchore/syft/cmd/syft@latest
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6
go install github.com/zricethezav/gitleaks/v8@latest
```

`govulncheck` 必须用与被测模块相同的工具链构建：它用 `go list` 解析源码，
工具链低于 `go.mod` 的 `go` 指令时会直接拒绝分析。升级 Go 后请重新执行上面的 `go install`。

密钥扫描的误报放行写在仓库根目录 `.gitleaks.toml`，每条都注明理由；
新增放行项必须有明确依据，不得整目录豁免源码。

## 分支保护

主分支必须通过全部必需检查：

- 禁止直接推送功能代码，必须走 PR。
- PR 模板要求填写关联 FR/NFR、问题、决策、测试结果。
- 任何失败检查必须修复后重新推送，不通过强制合并绕过。

## 已知限制（M0）

- 不运行集成测试；M1 增加 testcontainers job。
- 覆盖率门槛在 M1 起执行（核心领域 ≥ 80%，整体 ≥ 60%）。
- SBOM 目前仅归档，尚未做依赖许可证自动校验。
