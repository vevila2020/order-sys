//go:build tools

// 本文件用 tools build tag 把"已决策、尚未被生产代码 import"的依赖钉在 go.mod 里。
//
// 为什么需要它：M1 计划要求 T02 锁定 GORM、golang-migrate、testcontainers 的版本，
// 但它们真正的第一个 import 出现在 T04（platform/db）与 T17（testdb harness）。
// 若不在这里引用，`go mod tidy` 会把它们当作未使用依赖删除，下次 `go get` 又会
// 拉回可能不同的版本——go.sum 与 CI 缓存都会随之漂移。
//
// 加上 build tag 后，本文件不参与任何构建（go build / go vet / golangci-lint 都
// 看不到它），但 `go mod tidy` 会把它算进依赖图（tidy 视所有 build tag 为已启用），
// 从而把版本固定下来。这是 Go 生态处理"工具类依赖"的标准做法。
//
// 维护约定：
//   - T04 落地后，gorm 与 driver/mysql 的真实 import 出现在 platform/db，
//     届时删掉这里的对应行；它们会自然保留为直接依赖。
//   - T03 落地后，golang-migrate 的两个驱动由 cmd/api 或迁移命令真实 import。
//   - T17 落地后，testcontainers 由 testdb harness（integration tag）真实 import。
//   - 本文件最终应清空并删除；保留一条用不上的依赖在这里，等于隐藏一个未完成的
//     任务。删除时同步检查 go.mod 是否仍有 // indirect 残留。
package main

import (
	// golang-migrate 的驱动以副作用注册方式使用，最终形态就是 main 里的空导入，
	// 这里提前固定它们，避免 T03 时 go.sum 变动。
	_ "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	// GORM 与 MySQL 驱动是 M1 持久化的底座，版本需与 ADR-0001 记录一致。
	_ "gorm.io/driver/mysql"
	_ "gorm.io/gorm"

	// 集成测试用真实 MySQL 容器，避免用共享的远程库跑测试。
	_ "github.com/testcontainers/testcontainers-go"
	_ "github.com/testcontainers/testcontainers-go/modules/mysql"
)
