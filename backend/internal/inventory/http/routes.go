package http

import "github.com/gin-gonic/gin"

// Register 在 operator 分组上挂载 SKU 管理端点。
//
// 路由由调用方（httpserver 的注册表）决定挂在哪个分组，本包不关心鉴权：
// 把"谁能访问"与"路径是什么"分开，避免在业务包里复制角色判断，也避免
// 有人把管理端点误挂到 buyer 分组上——那需要显式改调用方代码，评审时看得见。
//
// 需求 §8.1：创建/修改 SKU 要求 operator；查询 SKU 同样按管理接口处理
// （buyer 不参与商品管理）。
func Register(group *gin.RouterGroup, h *Handler) {
	group.POST("/skus", h.Create)
	group.GET("/skus/:sku_id", h.Get)
	group.PATCH("/skus/:sku_id", h.Update)
}
