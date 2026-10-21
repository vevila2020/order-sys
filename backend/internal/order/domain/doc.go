// Package domain 定义订单聚合、订单明细与订单状态机。
//
// 依赖约束：本包只依赖标准库。不得 import gin、gorm、viper、zap 等任何框架包，
// 也不得 import 本仓库的 internal 包（含其他业务模块）。跨模块协作由 app 层
// 通过端口完成，见 ADR-0002。该约束由 internal/archtest 静态检查守住。
//
// 状态迁移只经聚合方法（Complete、Cancel、FailPayment、ExpirePayment）执行，
// 聚合字段不对外暴露可变 setter，保证非法迁移无法绕过校验。
//
// 金额一律用 int64 表示最小货币单位，禁止 float64；时间一律 UTC。
package domain
