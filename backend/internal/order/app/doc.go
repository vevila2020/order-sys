// Package app 编排订单用例：下单、查询、取消，以及支付回调引起的订单终态迁移。
//
// 依赖方向：app 依赖本模块 domain，以及 inventory/payment 在 app 层暴露的端口；
// 不依赖任何 infra 包，也不 import 其他模块的 http 或 infra。
//
// 事务边界在本层：一个写用例就是一次 TransactionManager.Do 调用（ADR-0003）。
// app 持有事务句柄只是为了把它原样传给端口，不得用它拼装 SQL：tx 是 *gorm.DB，
// 拿得到全部 ORM 能力，这条纪律无法由 archtest 的 import 规则保证。取舍与
// 重新评估的触发条件写在 platform/db.TransactionManager 的文档注释里。
//
// 事务内禁止调用 Kafka/HTTP/Redis/asynq：持锁期间的外部 I/O 会把行锁时间放大到
// 秒级并引发级联等待。Outbox 写入属于同库同事务的本地写，不受此限。
package app
