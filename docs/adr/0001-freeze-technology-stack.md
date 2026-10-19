# ADR-0001: 技术栈全周期冻结

| 项目 | 内容 |
| --- | --- |
| 状态 | Accepted |
| 日期 | 2026-10-19 |
| 关联需求 | §3.1–§3.5、NFR-007、NFR-013 |

## 背景

学习型项目最容易出现的失败模式是不断更换技术：先手写 SQL 再换 GORM、
先用内存队列再换 Kafka。每次替换都会让已完成的测试与文档失效，且掩盖真实
的工程问题。因此需要在 M0 一次性固定选型，后续只"启用能力"，不"更换技术品类"。

## 决策

冻结清单与首次启用里程碑：

| 领域 | 技术 | 首次启用 |
| --- | --- | --- |
| 语言 | Go 1.25.x | M0 |
| HTTP | Gin | M0 |
| 配置 | Viper | M0 |
| 日志 | zap | M0 |
| 持久化 | GORM + MySQL 8 | M1 |
| 迁移 | golang-migrate | M1 |
| 缓存/准入 | Redis 7 + go-redis | M3 |
| 消息 | Kafka（KRaft）+ kafka-go | M3 |
| 后台任务 | asynq | M3 |
| 标识 | google/uuid | M0 |
| 认证 | golang-jwt/jwt v5 | M0 |
| 指标 | Prometheus client_golang | M0 |
| 追踪 | OpenTelemetry | M5 |
| 测试 | testing、-race、testcontainers-go | M1 |
| 扫描/构建 | golangci-lint、govulncheck、gitleaks、Trivy、Compose、Make | M0 |

M0 实际写入 `go.mod` 的依赖仅限本阶段使用的库；其余在对应里程碑 `go get`。
不允许 M0 提前引入 GORM/Redis/Kafka/asynq 的空壳封装。

## 备选方案

- **先用 `database/sql` 手写全部 SQL，后续再引入 GORM。** 拒绝：这正是需求
  §3.5 明令禁止的路线。
- **M0 一次性引入全部依赖。** 拒绝：会引入未使用的依赖，无法验证真实集成，
  且 `go.sum` 膨胀后难以判断某个库是否真的在用。
- **允许按里程碑重新选型。** 拒绝：会让 ADR、测试和压测结果失去可比性。

## 后果

- 正面：选型稳定，测试与文档可复用；里程碑边界清晰。
- 负面：即使某个库在早期不理想，也必须走停线变更流程，短期灵活性下降。
- 新增不与现有库职责重叠的辅助依赖（测试工具、代码生成）可以，但需要 ADR 记录。

## 验证方式

- 每个里程碑的 PR 检查 `go.mod` 新增依赖是否在清单内。
- `make ci` 中的 govulncheck、license 检查作为门禁。
- 例外流程：提交 ADR → 影响分析 → 迁移与回滚方案 → 更新需求文档。

## 关联

- [ADR-0002](0002-modular-monolith-boundaries.md)
- 需求规格 §3.1–§3.5、§16.2
