// Package archtest 用可执行的测试守护模块化单体的依赖方向（ADR-0002）。
//
// 为什么要有这个包：目录结构本身不会阻止 `domain` 里出现一句
// `import "gorm.io/gorm"`。一旦出现，领域模型就与持久化绑定，单元测试要连数据
// 库，事务边界也会从 app 层渗进领域。代码评审能发现一次，但防不住以后每一次，
// 所以把规则写成测试，让 CI 在合并前拦住。
//
// 规则只检查**直接 import**，不做传递闭包检查：传递依赖无法归咎于本包，
// 且会随任一上游库新增依赖而误报。
//
// 已知盲区：import 规则管不到"通过签名泄漏类型"。最典型的是
// platform/db.TransactionManager 把 *gorm.DB 交到 app 层——app 没有 direct import
// gorm，测试却是绿的，而它可以对 tx 调用任意 ORM 方法。该约束目前只能靠评审，
// 取舍见 db.TransactionManager 的文档注释。新增端口时若把框架类型写进签名，
// 等于绕过本包的全部规则，须在 PR 中显式说明。
package archtest
