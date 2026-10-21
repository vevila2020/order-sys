# order-sys 项目约定

## AI 代码写入规范

- **写代码必须同步写注释。** 新增或修改代码时，必须在同一个改动中补齐注释，不允许“先写实现、以后再补注释”。
- 注释解释**为什么**和**约束**（设计意图、并发/安全假设、状态机规则、外部契约），不要逐行翻译代码。
- 导出的包、类型、函数使用 Go doc 注释，以包名/标识符开头；复杂分支、魔法值、错误映射和非显然的边界条件必须有行内注释。
- 注释与代码不一致视为缺陷：修改行为时必须同步修改注释，禁止保留过期注释。

## 运行环境

- **开发环境：Windows。** 给出的命令行命令必须能在 Windows PowerShell 7（`pwsh`）中直接执行，不要只给 bash / GNU 工具链写法（如 `sed -i`、`export`、`grep -P`、`/dev/urandom` 等）。
- **部署环境：Linux Debian amd64（x86_64）。** 容器镜像、部署脚本、systemd 单元等以该平台为准，不要引入仅 Windows 或非 amd64 可用的假设。
- 需要同时给出两套命令时，分别标注「PowerShell 7」与「Debian」，并注意两者路径分隔符、环境变量语法、换行符差异。

## Git 提交与推送

- **提交信息使用中文**，言语精简：标题一行说清改了什么，正文只写必要的动机与取舍，不罗列实现细节。
- 格式沿用 `type(scope): 描述`，例如 `feat(order): 实现幂等下单用例`。
- 推送前确认工作区干净、本地测试通过；推送目标为 `origin/master`。

## 项目约束（摘自需求规格 v3.1）

- 技术栈全周期冻结：Gin、Viper、zap、GORM、golang-migrate、go-redis、kafka-go、asynq、google/uuid、golang-jwt/v5、Prometheus、OpenTelemetry。不中途换库。
- 模块化单体：依赖方向 `http → app → domain`，`platform` 只放跨模块基础设施，不放业务规则。
- 业务代码由开发者手写；AI 输出用于方案、接口约定、测试建议与评审。
- 所有 I/O 传递 `context.Context`；金额使用整数最小货币单位；时间统一 UTC。
- 文档、ADR、运行手册与代码在同一 PR 中更新。
