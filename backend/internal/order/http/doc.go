// Package http 提供订单模块的 HTTP 入口：handler、请求/响应 DTO 与路由注册。
//
// handler 只做三件事：绑定与校验参数、调用 app 层用例、映射统一响应。
// 事务、锁与业务规则不得出现在本包（需求 §9.3）。
//
// 本包可以 import app，但不得 import infra：具体实现由 main 组装后注入。
//
// 命名约定：包名 http 与标准库 net/http 同名，而 handler 又必须引用标准库的
// 状态码与 ResponseWriter。为避免歧义，本包内统一写成
//
//	import stdhttp "net/http"
//
// 并只用 stdhttp.XXX 引用标准库。目录名由需求 §9.1 固定（app/infra/http 并列），
// 不改成 transport/delivery；各模块的 http 包遵守同一约定。
package http
