package httpx

import (
	"github.com/gin-gonic/gin"
)

// RequestIDHeader 是请求 ID 的传输头名称；响应头与响应体使用同一个值。
const RequestIDHeader = "X-Request-ID"

// 响应体结构固定，客户端依赖这些字段名，不得随业务改动。
type successBody struct {
	RequestID string `json:"request_id"`
	Data      any    `json:"data"`
}

type errorBody struct {
	RequestID string       `json:"request_id"`
	Error     errorPayload `json:"error"`
}

type errorPayload struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// WriteData 写入成功响应。状态码由调用方决定（201/200/202 等语义不同）。
func WriteData(c *gin.Context, status int, data any) {
	c.JSON(status, successBody{RequestID: RequestID(c), Data: data})
}

// WriteError 写入错误响应，使用 AppError 自带的状态码与 code。
func WriteError(c *gin.Context, appErr *AppError) {
	if appErr == nil {
		appErr = NewError(CodeInternal, nil)
	}
	// Retry-After 只从服务端计算，客户端不能通过请求头影响它。
	if appErr.RetryAfter > 0 {
		c.Header("Retry-After", formatSeconds(appErr.RetryAfter))
	}
	c.AbortWithStatusJSON(appErr.Status(), errorBody{
		RequestID: RequestID(c),
		Error: errorPayload{
			Code:    appErr.Code,
			Message: appErr.Message,
			Details: appErr.Details,
		},
	})
}

// WriteErrorFrom 把任意错误映射为统一错误响应；未知错误折叠为 INTERNAL，
// 避免内部堆栈、SQL 或 DSN 泄露给客户端。
func WriteErrorFrom(c *gin.Context, err error) {
	WriteError(c, FromError(c.Request.Context(), err))
}

// RequestID 读取中间件写入的请求 ID；缺失时返回空串，
// 测试或未接入中间件的 handler 仍能生成合法响应体。
func RequestID(c *gin.Context) string {
	return c.GetString(string(RequestIDContextKey))
}
