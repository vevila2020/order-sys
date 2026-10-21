// Package http 提供模拟支付回调的 HTTP 入口。
//
// 回调不使用 buyer JWT，而是校验 X-Simulated-Payment-Token（来自环境变量
// PAYMENT_CALLBACK_TOKEN）。令牌比较必须用 subtle.ConstantTimeCompare，且错误
// 响应不得回显期望值。该路由仍须经过 request_id、recover、访问日志、超时与
// 请求体上限，只是跳过 JWT 与角色中间件。
package http
