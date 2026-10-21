// Package domain 定义 SKU、库存与预占的领域模型与不变量。
//
// 依赖约束：只依赖标准库，不 import 任何框架包或本仓库的其他 internal 包。
//
// 库存恒等式 available + allocated + reserved + sold = total 是贯穿全模块的
// 不变量：任何变更都要么保持它成立，要么在变更前拒绝。四次预占/确认/释放的
// 状态机为 RESERVED → CONFIRMED 或 RESERVED → RELEASED，两种迁移都只能发生一次。
package domain
