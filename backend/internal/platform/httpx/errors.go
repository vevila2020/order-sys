// Package httpx 定义统一 HTTP 响应、错误模型、服务启动与中间件链。
//
// 错误契约来自需求 §8.3：业务代码返回被 AppError 包装的错误，由 WriteErrorFrom
// 统一映射为稳定的 code 与 HTTP 状态。内部错误细节（堆栈、SQL、DSN）永远不写回客户端。
package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Code 是稳定的机器可读错误码。一旦发布就不能修改或删除，只能新增。
type Code string

// 错误码清单，与需求规格 §8.3 一一对应。
const (
	CodeValidationFailed       Code = "VALIDATION_FAILED"
	CodeUnauthorized           Code = "UNAUTHORIZED"
	CodeForbidden              Code = "FORBIDDEN"
	CodeNotFound               Code = "NOT_FOUND"
	CodeConflict               Code = "CONFLICT"
	CodeIdempotencyConflict    Code = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyInProgress  Code = "IDEMPOTENCY_IN_PROGRESS"
	CodeInvalidStateTransition Code = "INVALID_STATE_TRANSITION"
	CodeInsufficientStock      Code = "INSUFFICIENT_STOCK"
	CodeSaleNotStarted         Code = "SALE_NOT_STARTED"
	CodeSaleEnded              Code = "SALE_ENDED"
	CodeSoldOut                Code = "SOLD_OUT"
	CodeLimitExceeded          Code = "LIMIT_EXCEEDED"
	CodeRateLimited            Code = "RATE_LIMITED"
	CodePayloadTooLarge        Code = "PAYLOAD_TOO_LARGE"
	CodeDependencyUnavailable  Code = "DEPENDENCY_UNAVAILABLE"
	CodeTimeout                Code = "TIMEOUT"
	CodeInternal               Code = "INTERNAL"
)

// statusByCode 固定错误码到 HTTP 状态的映射；不要在 handler 里自行判断状态码。
var statusByCode = map[Code]int{
	CodeValidationFailed:       http.StatusBadRequest,
	CodeUnauthorized:           http.StatusUnauthorized,
	CodeForbidden:              http.StatusForbidden,
	CodeNotFound:               http.StatusNotFound,
	CodeConflict:               http.StatusConflict,
	CodeIdempotencyConflict:    http.StatusConflict,
	CodeIdempotencyInProgress:  http.StatusConflict,
	CodeInvalidStateTransition: http.StatusConflict,
	CodeInsufficientStock:      http.StatusConflict,
	CodeSaleNotStarted:         http.StatusConflict,
	CodeSaleEnded:              http.StatusConflict,
	CodeSoldOut:                http.StatusConflict,
	CodeLimitExceeded:          http.StatusConflict,
	CodeRateLimited:            http.StatusTooManyRequests,
	CodePayloadTooLarge:        http.StatusRequestEntityTooLarge,
	CodeDependencyUnavailable:  http.StatusServiceUnavailable,
	CodeTimeout:                http.StatusGatewayTimeout,
	CodeInternal:               http.StatusInternalServerError,
}

// defaultMessage 是面向客户端的兜底文案；客户端应以 code 判断，不应解析 message。
var defaultMessage = map[Code]string{
	CodeValidationFailed:       "请求参数无效",
	CodeUnauthorized:           "未认证或凭证无效",
	CodeForbidden:              "无权限访问该资源",
	CodeNotFound:               "资源不存在",
	CodeConflict:               "请求与当前状态冲突",
	CodeIdempotencyConflict:    "相同幂等键的请求内容不一致",
	CodeIdempotencyInProgress:  "相同幂等键的请求正在处理中",
	CodeInvalidStateTransition: "当前状态不允许该操作",
	CodeInsufficientStock:      "库存不足",
	CodeSaleNotStarted:         "活动未开始",
	CodeSaleEnded:              "活动已结束",
	CodeSoldOut:                "活动名额已抢完",
	CodeLimitExceeded:          "超过单用户限购",
	CodeRateLimited:            "请求过于频繁",
	CodePayloadTooLarge:        "请求体超过大小限制",
	CodeDependencyUnavailable:  "依赖服务暂不可用",
	CodeTimeout:                "请求处理超时",
	CodeInternal:               "服务内部错误",
}

// AppError 是可映射到错误码的应用错误。
//
// 设计上有意区分对外信息与内部原因：Message/Details 会返回给客户端，
// err 只用于服务端日志与 errors.Is/As 判断，绝不序列化到响应体。
type AppError struct {
	Code    Code
	Message string
	Details any
	// RetryAfter 为限流与"处理中"冲突提供建议等待时间，>0 时写入 Retry-After 头。
	RetryAfter time.Duration
	err        error
}

// WithRetryAfter 返回附加建议等待时间的副本，保持链式构造的不可变语义。
func (e *AppError) WithRetryAfter(d time.Duration) *AppError {
	cp := *e
	cp.RetryAfter = d
	return &cp
}

// Error 实现 error 接口，输出便于日志排查的内部信息。
func (e *AppError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap 保留错误链，供 errors.Is/As 使用（领域错误映射的入口）。
func (e *AppError) Unwrap() error { return e.err }

// Status 返回该错误对应的 HTTP 状态码；未知错误码按 500 处理。
func (e *AppError) Status() int {
	if status, ok := statusByCode[e.Code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// NewError 构造带默认文案的应用错误。
func NewError(code Code, cause error) *AppError {
	return &AppError{Code: code, Message: messageFor(code), err: cause}
}

// NewErrorWithDetails 构造带结构化错误详情与自定义文案的应用错误。
// details 只允许放非敏感上下文，例如 sku_id、requested、available。
func NewErrorWithDetails(code Code, message string, details any, cause error) *AppError {
	if message == "" {
		message = messageFor(code)
	}
	return &AppError{Code: code, Message: message, Details: details, err: cause}
}

// messageFor 返回错误码的默认文案，未登记的 code 使用通用内部错误文案。
func messageFor(code Code) string {
	if msg, ok := defaultMessage[code]; ok {
		return msg
	}
	return defaultMessage[CodeInternal]
}

// 哨兵错误供 errors.Is 使用，避免调用方比较文案。
var (
	ErrValidationFailed       = NewError(CodeValidationFailed, nil)
	ErrUnauthorized           = NewError(CodeUnauthorized, nil)
	ErrForbidden              = NewError(CodeForbidden, nil)
	ErrNotFound               = NewError(CodeNotFound, nil)
	ErrConflict               = NewError(CodeConflict, nil)
	ErrInvalidStateTransition = NewError(CodeInvalidStateTransition, nil)
	ErrInsufficientStock      = NewError(CodeInsufficientStock, nil)
	ErrRateLimited            = NewError(CodeRateLimited, nil)
	ErrDependencyUnavailable  = NewError(CodeDependencyUnavailable, nil)
	ErrTimeout                = NewError(CodeTimeout, nil)
	ErrInternal               = NewError(CodeInternal, nil)
)

// AsAppError 判断错误链中是否包含 AppError。
func AsAppError(err error) (*AppError, bool) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}

// FromError 把任意错误映射为 AppError。未知错误统一折叠成 INTERNAL，
// 从而保证客户端永远看不到内部细节。
//
// context 超时与取消映射为 TIMEOUT；调用方应传入请求 context 以保留语义。
func FromError(ctx context.Context, err error) *AppError {
	if err == nil {
		return nil
	}
	if appErr, ok := AsAppError(err); ok {
		// 已映射的错误保持原样；若内部原因缺失则不追加。
		return appErr
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return NewError(CodeTimeout, err)
	}
	// 请求上下文被取消（客户端断开或超时中间件中止）时不应伪装成 500；
	// 映射为 504，与超时语义一致。
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return NewError(CodeTimeout, err)
	}
	return NewError(CodeInternal, err)
}
