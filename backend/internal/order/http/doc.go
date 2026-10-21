// Package http 提供订单模块的 HTTP 入口：handler、请求/响应 DTO 与路由注册。
//
// handler 只做三件事：绑定与校验参数、调用 app 层用例、映射统一响应。
// 事务、锁与业务规则不得出现在本包（需求 §9.3）。
//
// 本包可以 import app，但不得 import infra：具体实现由 main 组装后注入。
package http
