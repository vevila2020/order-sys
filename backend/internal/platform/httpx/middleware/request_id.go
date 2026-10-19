// Package middleware 提供固定顺序的 HTTP 中间件链。
//
// 顺序约定（需求 §9.3）：request_id → recover → access_log → timeout →
// auth → role → ratelimit → body_limit → handler。顺序改变会影响可追踪性、
// 资源占用和错误体一致性，必须经过评审并用测试保护。
package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// HeaderRequestID 是客户端可传入的请求 ID 头。
const HeaderRequestID = httpx.RequestIDHeader

// RequestID 保证每个请求带有稳定的请求 ID：优先使用客户端传入值，
// 缺失或格式非法时生成 UUID。该值同时写入响应头、gin context 与 request context，
// 使 handler、日志和错误体共享同一个 ID。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader(HeaderRequestID))
		// 只接受长度受限的可打印字符，防止把任意请求头原样写回响应与日志。
		if !validRequestID(id) {
			id = uuid.NewString()
		}
		c.Set(string(httpx.RequestIDContextKey), id)
		c.Request = c.Request.WithContext(httpx.WithRequestID(c.Request.Context(), id))
		c.Header(HeaderRequestID, id)
		c.Next()
	}
}

// validRequestID 限制请求 ID 为 1-128 个可见 ASCII 字符。
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
