# 架构决策记录（ADR）

ADR 记录不可逆或影响面较大的工程决策。被替代的 ADR 不删除，状态改为
`Superseded` 并链接新 ADR。

## 模板

```markdown
# ADR-NNNN: 标题

| 项目 | 内容 |
| --- | --- |
| 状态 | Proposed / Accepted / Superseded |
| 日期 | YYYY-MM-DD |
| 关联需求 | FR-xxx / NFR-xxx |

## 背景

为什么需要决策，约束是什么。

## 决策

选择了什么，边界在哪里。

## 备选方案

考虑过但没有采用的方案，以及原因。

## 后果

正面、负面影响，以及需要接受的代价。

## 验证方式

如何证明决策被正确执行（测试、审查、指标）。

## 关联

链接相关 ADR、需求、计划。
```

## 清单

| ADR | 主题 | 状态 |
| --- | --- | --- |
| [0001](0001-freeze-technology-stack.md) | 技术栈全周期冻结 | Accepted |
| [0002](0002-modular-monolith-boundaries.md) | 模块化单体与模块边界 | Accepted |
| [0003](0003-transaction-and-locking.md) | 事务与并发控制 | Proposed |
| [0007](0007-local-jwt-auth.md) | 本地 JWT 认证与角色 | Accepted |
| [0010](0010-error-model.md) | 错误模型与错误码 | Accepted |
