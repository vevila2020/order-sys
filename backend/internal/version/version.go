// Package version 保存构建期注入的版本信息。
//
// 版本值通过链接参数注入，例如：
//
//	go build -ldflags "-X github.com/vevila2020/order-sys/internal/version.Version=v0.1.0"
//
// 未注入时保持开发默认值，保证本地 go run 也能正常输出日志与指标标签。
package version

import "fmt"

var (
	// Version 是语义化版本号，M0 默认为 dev。
	Version = "dev"
	// Commit 是构建所用的 git 提交 SHA。
	Commit = "unknown"
	// BuildDate 是构建时间（UTC，RFC 3339）。
	BuildDate = "unknown"
)

// String 返回单行版本描述，供 --version 与启动日志使用。
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, BuildDate)
}
