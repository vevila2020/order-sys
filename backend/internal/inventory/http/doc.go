// Package http 提供 SKU 管理的 HTTP 入口。
//
// 管理端点要求 operator 角色；buyer 访问返回 403（需求 §8.1 授权矩阵）。
// 更新 SKU 必须携带 version，版本不匹配返回 409，用于检测丢失更新。
//
// 命名约定：包名与标准库 net/http 同名，本包内统一写成 stdhttp "net/http"，
// 只用 stdhttp.XXX 引用标准库，避免同名歧义。详见 order/http 的包文档。
package http
