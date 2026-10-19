# ADR-0002: 模块化单体与模块边界

| 项目 | 内容 |
| --- | --- |
| 状态 | Accepted |
| 日期 | 2026-10-19 |
| 关联需求 | §3.3、§4.1、§9.1–§9.2、NFR-007、NFR-013 |

## 背景

订单、库存、支付、秒杀之间存在强事务关联。若一开始就拆成独立服务，就必须
用 Saga 处理本可在单库单事务内原子完成的工作，引入分布式一致性风险，而收益
（独立伸缩、独立发布）在单开发者项目中并不成立。

## 决策

一期采用**模块化单体**：

- 部署单元：`api`（同步 HTTP）与 `worker`（异步任务），共享同一套 `internal` 代码。
- 业务模块：`order`、`inventory`、`payment`、`flashsale`、`notification`，
  每个模块内部分层为 `domain` / `app` / `infra` / `http`。
- 依赖方向：`http → app → domain`；`infra` 实现 `app`/`domain` 定义的端口；
  `domain` 不 import Gin、GORM、Kafka、Redis。
- 数据所有权：每个模块只写自己的表；跨模块协作通过应用服务接口（同步）或
  领域事件（异步），不直接读写对方表。
- `platform` 只放跨模块基础设施（配置、日志、HTTP、认证、健康、指标、限流、
  后续的 db/cache/event/jobs），不放业务规则。
- 依赖注入在 `main` 中组装，禁止业务层包级全局变量。

M0 的实际目录（`internal/platform/*`、`internal/version`）已按此边界建立；
业务模块目录在 M1 创建。

## 备选方案

- **微服务起步。** 拒绝：单开发者成本高，且会用分布式事务掩盖领域建模问题。
- **按技术分层（handlers/services/repositories 全局目录）。** 拒绝：模块边界
  会被隐式耦合破坏，无法表达数据所有权。
- **单体但只有 `internal/app` 一个包。** 拒绝：无法阻止模块间直接访问对方表。

## 后果

- 正面：核心生命周期可用本地事务保证强一致；重构成本低；拆分服务时有明确接缝。
- 负面：需要在 code review 中持续维护边界；同一进程内故障隔离弱于微服务。
- 拆分时机：事件契约稳定、模块间只通过事件/端口交互、有独立伸缩需求时再拆。

## 验证方式

- 静态检查：`go list -deps` 检查 `domain` 包不依赖框架；PR 评审检查跨模块 import。
- 测试：组件测试用假端口替换 infra，证明 app 层不依赖具体实现。
- 目录审查：M1 起每个模块必须有 `domain` 且不包含框架 import。

## 关联

- [ADR-0001](0001-freeze-technology-stack.md)
- [ADR-0003](0003-transaction-and-locking.md)
- 需求规格 §3.3、§4.1、§9.1
