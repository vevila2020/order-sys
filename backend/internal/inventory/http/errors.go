package http

import (
	"errors"

	"github.com/vevila2020/order-sys/internal/inventory/app"
	"github.com/vevila2020/order-sys/internal/inventory/domain"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
)

// toAppError 把 inventory 的领域/应用错误映射为统一错误码。
//
// 映射表集中在这里的动机：错误码一旦发布就是对外契约，散落在各 handler 里
// 会让同一个错误在不同端点返回不同 code，客户端无法可靠判断。
func toAppError(err error) *httpx.AppError {
	if err == nil {
		return nil
	}
	// 已经是 AppError 的直接返回（例如上层已映射过）。
	if appErr, ok := httpx.AsAppError(err); ok {
		return appErr
	}

	switch {
	case errors.Is(err, app.ErrSKUNotFound), errors.Is(err, app.ErrInventoryNotFound):
		return httpx.NewError(httpx.CodeNotFound, err)
	case errors.Is(err, app.ErrSKUExists):
		// 创建资源时重复 = 冲突。用 CONFLICT 而不是 VALIDATION_FAILED：
		// 请求本身合法，只是前提（ID 未被占用）不成立。
		return httpx.NewError(httpx.CodeConflict, err)
	case errors.Is(err, app.ErrSKUVersionConflict):
		return httpx.NewError(httpx.CodeConflict, err)
	case errors.Is(err, app.ErrReservationExists), errors.Is(err, app.ErrReservationStateConflict):
		return httpx.NewError(httpx.CodeConflict, err)
	case errors.Is(err, domain.ErrInsufficientStock):
		return httpx.NewError(httpx.CodeInsufficientStock, err)
	case errors.Is(err, domain.ErrInvalidReservationState):
		return httpx.NewError(httpx.CodeInvalidStateTransition, err)
	case errors.Is(err, domain.ErrInvalidSKUID),
		errors.Is(err, domain.ErrInvalidOrderID),
		errors.Is(err, domain.ErrInvalidPrice),
		errors.Is(err, domain.ErrInvalidQuantity),
		errors.Is(err, domain.ErrDuplicateSKU),
		errors.Is(err, app.ErrInvalidInput):
		return httpx.NewError(httpx.CodeValidationFailed, err)
	}

	// 其余（含 ErrInvariantViolated）交给统一兜底：INTERNAL，具体信息只进日志。
	return nil
}
