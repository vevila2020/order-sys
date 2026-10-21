// Package http 提供 SKU 管理的 HTTP 入口。
//
// 管理端点要求 operator 角色；buyer 访问返回 403（需求 §8.1 授权矩阵）。
// 更新 SKU 必须携带 version，版本不匹配返回 409，用于检测丢失更新。
package http
