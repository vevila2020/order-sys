// Package domain 定义模拟支付单及其状态机。
//
// 依赖约束：只依赖标准库，不 import 任何框架包或本仓库的其他 internal 包。
//
// 状态机：
//
//	PENDING -> SUCCEEDED / FAILED / CANCELLED
//
// SUCCEEDED、FAILED、CANCELLED 均为终态，不可再次迁移。相同结果的重复回调是
// 幂等 no-op（返回"未发生迁移"），相反结果必须返回领域错误而非静默覆盖——
// 覆盖会掩盖真实的时序问题（例如订单已取消后支付成功）。
//
// 每个订单最多一个有效支付单，由 payments.order_id 的唯一约束兜底，不靠应用层先查。
package domain
