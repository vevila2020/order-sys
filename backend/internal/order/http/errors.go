package http

import (
	"errors"
	"time"

	"github.com/vevila2020/order-sys/internal/order/app"
	"github.com/vevila2020/order-sys/internal/order/domain"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/idempotency"
)

// toAppError 把订单模块的错误映射为统一错误码。
//
// 幂等相关分支放在最前面：它们决定客户端该"换键"还是"稍后重试"，是最容易
// 被误映射成 500 的一类错误。
func toAppError(err error) *httpx.AppError {
	if err == nil {
		return nil
	}
	if appErr, ok := httpx.AsAppError(err); ok {
		return appErr
	}

	switch {
	case errors.Is(err, idempotency.ErrKeyReused):
		return httpx.NewError(httpx.CodeIdempotencyConflict, err)
	case errors.Is(err, idempotency.ErrInProgress):
		// Retry-After 由服务端给出建议：同键的另一个请求正在处理，通常很快结束。
		return httpx.NewError(httpx.CodeIdempotencyInProgress, err).WithRetryAfter(idempotencyRetryAfter)
	case errors.Is(err, idempotency.ErrInvalidKey), errors.Is(err, idempotency.ErrInvalidRequest):
		return httpx.NewError(httpx.CodeValidationFailed, err)

	case errors.Is(err, app.ErrInsufficientStock):
		return httpx.NewError(httpx.CodeInsufficientStock, err)
	case errors.Is(err, app.ErrOrderNotFound), errors.Is(err, app.ErrOrderForbidden):
		// 无权访问与不存在返回同一个 404：区分二者等于泄露订单是否存在。
		return httpx.NewError(httpx.CodeNotFound, err)
	case errors.Is(err, app.ErrOrderStateConflict):
		return httpx.NewError(httpx.CodeInvalidStateTransition, err)
	case errors.Is(err, domain.ErrInvalidStateTransition):
		// 终态订单再次取消：状态机不允许，而不是并发冲突。
		return httpx.NewError(httpx.CodeInvalidStateTransition, err)

	case errors.Is(err, domain.ErrInvalidUserID),
		errors.Is(err, domain.ErrInvalidOrderID),
		errors.Is(err, domain.ErrInvalidSKUID),
		errors.Is(err, domain.ErrInvalidQuantity),
		errors.Is(err, domain.ErrInvalidPrice),
		errors.Is(err, domain.ErrEmptyOrder),
		errors.Is(err, domain.ErrDuplicateSKU),
		errors.Is(err, app.ErrInvalidInput):
		return httpx.NewError(httpx.CodeValidationFailed, err)
	}

	// 其余交给统一兜底（INTERNAL），内部细节只进日志。
	return nil
}

// idempotencyRetryAfter 是"同键处理中"时建议的重试间隔。
//
// 取 1 秒：下单事务预算 5 秒，1 秒足够让先到者完成，又不会让客户端等待过久。
// 与 db 包的死锁退避建议保持一致，客户端可以用同一套退避策略。
const idempotencyRetryAfter = time.Second
