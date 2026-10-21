//go:build tools

// 本文件用 tools build tag 把"已决策、尚未被生产代码 import"的依赖钉在 go.mod 里。
//
// 为什么需要它：M1 计划要求 T02 锁定 GORM、golang-migrate、testcontainers 的版本，
// 但它们真正的第一个 import 出现在 T04（platform/db）与后续的迁移命令。
// 若不在这里引用，`go mod tidy` 会把它们当作未使用依赖删除，下次 `go get` 又会
// 拉回可能不同的版本——go.sum 与 CI 缓存都会随之漂移。
//
// 加上 build tag 后，本文件不参与任何构建（go build / go vet / golangci-lint 都
// 看不到它），但 `go mod tidy` 会把它算进依赖图（tidy 视所有 build tag 为已启用），
// 从而把版本固定下来。这是 Go 生态处理"工具类依赖"的标准做法。
//
// 维护约定：每个里程碑落地真实 import 后删掉对应行；文件清空即删除。
// 保留一条用不上的依赖在这里，等于隐藏一个未完成的任务。
//
// 已落地（2026-10-21，M1-T03）：golang-migrate 的两个驱动与 testcontainers
// （含 mysql 模块）现在由 internal/platform/testdb 真实 import（integration tag），
// 故已从本文件移除。剩下的 GORM 在 M1-T04 落地后同样移除。
package main

import (
	// GORM 与 MySQL 驱动是 M1 持久化的底座，版本需与 ADR-0001 记录一致。
	_ "gorm.io/driver/mysql"
	_ "gorm.io/gorm"
)
