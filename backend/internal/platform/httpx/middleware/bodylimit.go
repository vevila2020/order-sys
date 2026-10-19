package middleware

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// BodyLimit 限制请求体大小，超过上限返回 413 PAYLOAD_TOO_LARGE。
//
// 同时使用两道防线：
//  1. Content-Length 超限时立即拒绝，避免读取大量数据；
//  2. http.MaxBytesReader 兜住分块传输和伪造 Content-Length 的情况。
//
// 只检查头部无法防御分块传输，只在读取时报错又会让 handler 先做无谓工作，
// 因此两者都需要。
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if maxBytes <= 0 {
			c.Next()
			return
		}
		if c.Request.ContentLength > maxBytes {
			httpx.WriteError(c, PayloadTooLarge(maxBytes))
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// PayloadTooLarge 构造 413 错误；code 使用稳定错误表的 PAYLOAD_TOO_LARGE。
func PayloadTooLarge(maxBytes int64) *httpx.AppError {
	return httpx.NewErrorWithDetails(httpx.CodePayloadTooLarge, "", map[string]any{
		"limit_bytes": maxBytes,
	}, nil)
}

// IsBodyTooLarge 判断错误是否由请求体超限导致，供 handler 的绑定错误映射使用。
func IsBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

// BodyLimitError 把 MaxBytesReader 产生的错误映射为统一的 413 AppError。
func BodyLimitError(err error) *httpx.AppError {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return PayloadTooLarge(maxErr.Limit)
	}
	return httpx.NewError(httpx.CodePayloadTooLarge, err)
}
