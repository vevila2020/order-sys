package http

import "github.com/gin-gonic/gin"

// Register 在 buyer 分组上挂载订单端点。
//
// buyer 分组要求已认证但不限制角色：需求 §8.1 的授权矩阵里，buyer 与 operator
// 都可以创建/查询/取消自己的订单。"自己的"这一约束由用例层按 user_id 过滤，
// 不在路由层表达——路由层拿不到订单归属。
func Register(group *gin.RouterGroup, h *Handler) {
	group.POST("/orders", h.Place)
}
