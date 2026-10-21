// Package httpserver 负责装配 Gin 引擎、中间件链与路由。
//
// 单独成包是为了避免 httpx 与 authx 的导入环：httpx 提供响应与错误模型，
// authx/healthx/ratelimit 的中间件都依赖 httpx，而路由装配又需要它们。
// 把"装配"从"模型"中拆开后，依赖方向保持单向。
package httpserver

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/vevila2020/order-sys/internal/platform/authx"
	"github.com/vevila2020/order-sys/internal/platform/healthx"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/httpx/middleware"
	"github.com/vevila2020/order-sys/internal/platform/observability"
	"github.com/vevila2020/order-sys/internal/platform/ratelimit"
)

// ServerDeps 是装配 HTTP 服务所需的依赖，全部由 main 显式注入，
// 禁止包级全局变量（需求 §3.3）。
type ServerDeps struct {
	Config    ServerConfig
	Logger    *zap.Logger
	Metrics   *observability.Metrics
	Health    *healthx.Registry
	Tokens    *authx.TokenService
	Limiter   ratelimit.Limiter
	LocalAuth bool
	// RegisterRoutes 让业务模块把自己的路由挂到已装配好的分组上。
	//
	// 为什么用钩子而不是让 httpserver import 各业务模块：platform 不得依赖业务
	// 模块（ADR-0002，由 internal/archtest 强制）。钩子把"装配"留在 main，
	// 平台包只提供分组与中间件链。为 nil 时只注册 M0 的基础路由。
	RegisterRoutes func(RouteGroups)
}

// RouteGroups 是可供业务模块挂载路由的分组。
//
// 每个分组已经带好对应角色的中间件链（auth → role → ratelimit → body_limit），
// 业务模块直接往上挂，不得自行再叠加认证或改变顺序（需求 §9.3）。
type RouteGroups struct {
	// Buyer 要求已认证，不区分角色。
	Buyer *gin.RouterGroup
	// Operator 要求已认证且角色为 operator。
	Operator *gin.RouterGroup
}

// ServerConfig 是路由装配使用的配置子集。
type ServerConfig struct {
	MaxBodyBytes     int64
	RequestTimeout   time.Duration
	RateLimitEnabled bool
}

// NewEngine 按固定顺序装配中间件并注册 M0 路由。
//
// 中间件顺序（需求 §9.3）：
//
//	request_id → recover → access_log → metrics → timeout → auth → role → ratelimit → body_limit → handler
//
// 指标放在认证之前，保证 401/403 也计入请求量与延迟。
func NewEngine(deps ServerDeps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	// 使用 gin.New 而不是 gin.Default，避免默认 Logger/Recovery 与自定义实现重复。
	engine := gin.New()
	// 移除 X-Powered-By 之类的框架指纹，减少信息泄露。
	engine.HandleMethodNotAllowed = true
	engine.RedirectTrailingSlash = false

	engine.Use(
		middleware.RequestID(),
		middleware.Recover(deps.Logger),
		middleware.AccessLog(deps.Logger),
	)
	if deps.Metrics != nil {
		engine.Use(deps.Metrics.Middleware())
	}
	engine.Use(middleware.Timeout(deps.Config.RequestTimeout))

	// 健康检查与指标不经过认证与限流：它们是基础设施探针。
	engine.GET("/health/live", healthx.LiveHandler())
	engine.GET("/health/ready", healthx.ReadyHandler(deps.Health))
	if deps.Metrics != nil {
		engine.GET("/metrics", deps.Metrics.GinHandler())
	}

	v1 := engine.Group("/api/v1")

	if deps.Tokens != nil {
		// 签发端点无需认证，但仍限制请求体大小。
		v1.POST("/auth/token",
			middleware.BodyLimit(deps.Config.MaxBodyBytes),
			authx.TokenHandler(deps.Tokens, deps.LocalAuth),
		)
	}

	// 受保护分组：M0 的探测路由与 M1 起的业务路由都挂在这里。
	// 中间件顺序必须与需求 §9.3 一致：auth → role → ratelimit → body_limit，
	// 因此每个角色分组都从 v1 直接派生，而不是在已加限流的组上再叠加角色。
	tokens := deps.Tokens
	if tokens == nil {
		// 没有 token 服务时不注册受保护路由，避免 nil 解引用。
		engine.NoRoute(func(c *gin.Context) {
			httpx.WriteError(c, httpx.NewError(httpx.CodeNotFound, nil))
		})
		return engine
	}

	buyerChain := []gin.HandlerFunc{
		authx.RequireAuth(tokens),
		ratelimit.Middleware(deps.Limiter, deps.Config.RateLimitEnabled),
		middleware.BodyLimit(deps.Config.MaxBodyBytes),
	}
	protected := v1.Group("", buyerChain...)
	protected.GET("/me", func(c *gin.Context) {
		// 身份只来自 token，用于验收 buyer/operator 边界。
		p, ok := authx.CurrentUser(c.Request.Context())
		if !ok {
			httpx.WriteError(c, httpx.NewError(httpx.CodeUnauthorized, nil))
			return
		}
		httpx.WriteData(c, http.StatusOK, gin.H{"user_id": p.UserID, "role": p.Role})
	})

	operatorChain := []gin.HandlerFunc{
		authx.RequireAuth(tokens),
		authx.RequireRole(authx.RoleOperator),
		ratelimit.Middleware(deps.Limiter, deps.Config.RateLimitEnabled),
		middleware.BodyLimit(deps.Config.MaxBodyBytes),
	}
	operator := v1.Group("/admin", operatorChain...)
	operator.GET("/ping", func(c *gin.Context) {
		httpx.WriteData(c, http.StatusOK, gin.H{"status": "ok"})
	})

	// 业务模块的路由挂载。
	//
	// operator 分组有两个用途：/admin 前缀下的管理探测，以及需求 §8.2 中
	// 直接挂在 /api/v1 下的管理端点（如 /skus）。后者不能挂在 /admin 上，
	// 否则路径与契约不符，因此单独派生一个同样中间件链的分组。
	// 这里从 v1 + 同一个 chain 派生，而不是在已挂载的 operator 组上再派生，
	// 避免中间件被重复执行两次。
	operatorV1 := v1.Group("", operatorChain...)
	if deps.RegisterRoutes != nil {
		deps.RegisterRoutes(RouteGroups{
			Buyer:    protected,
			Operator: operatorV1,
		})
	}

	// 未匹配路由返回统一 404 错误体，而不是 gin 的纯文本响应。
	engine.NoRoute(func(c *gin.Context) {
		httpx.WriteError(c, httpx.NewError(httpx.CodeNotFound, nil))
	})
	engine.NoMethod(func(c *gin.Context) {
		httpx.WriteError(c, httpx.NewError(httpx.CodeNotFound, nil))
	})

	return engine
}

// NewHTTPServer 构造带超时参数的 http.Server，避免使用零值导致无限等待。
func NewHTTPServer(addr string, handler http.Handler, readTimeout, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		// IdleTimeout 控制 keep-alive 空闲连接，防止连接泄漏。
		IdleTimeout: 60 * time.Second,
	}
}
