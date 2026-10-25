package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/vevila2020/order-sys/internal/order/domain"
	"github.com/vevila2020/order-sys/internal/platform/db"
	"github.com/vevila2020/order-sys/internal/platform/outbox"
)

// CloseReasonUserCancelled 是用户主动取消时写入订单的关闭原因。
//
// 取值固定：它会被写入 orders.close_reason，客户端与对账脚本按字符串匹配，
// 不能写成自由文本。
const CloseReasonUserCancelled = "USER_CANCELLED"

// CancelOrder 取消订单并释放在途资源。
//
// 事务内的顺序是固定的：
//
//  1. 条件更新订单状态 PENDING_PAYMENT → CANCELLED；
//  2. 条件迁移预占 RESERVED → RELEASED，并把预占量归还库存；
//  3. 取消支付单（PENDING → CANCELLED）；
//  4. 写 order.cancelled 事件。
//
// 为什么先改订单状态：它是并发仲裁点。取消与支付回调同时到达时，两者的第一条
// 写操作都是对同一行的条件更新，只有一个能命中；另一个拿到 0 行后按稳定结果
// 返回，因此"释放最多一次"由数据库保证，而不是靠应用层判断。
//
// 重复取消返回成功而不是 409：需求 §8.4 的幂等语义——同一终态的重放，业务结果
// 与第一次相同。终态是别的状态（已完成/已失败/已超时）才返回 409。
func (s *Service) CancelOrder(ctx context.Context, viewerUserID, orderID string) error {
	return s.tx.Do(ctx, func(ctx context.Context, tx db.Tx) error {
		order, err := s.orders.GetByID(ctx, tx, orderID)
		if err != nil {
			return err
		}
		if order.UserID() != viewerUserID {
			// 与查询一致：他人订单按"不存在"处理，不泄露存在性。
			return ErrOrderForbidden
		}

		// 已经是取消状态：重放。直接返回成功，不再写事件、不再动库存。
		if order.Status() == domain.StatusCancelled {
			return nil
		}
		// 其他终态不允许取消。这里先判断，避免走一次注定 0 行的条件更新，
		// 也让错误语义更准确（"已完成"而不是"并发冲突"）。
		if order.Status().IsTerminal() {
			return fmt.Errorf("%w: status=%s", domain.ErrInvalidStateTransition, order.Status())
		}

		// 1. 条件更新订单状态。
		//
		// 0 行说明并发方（支付回调或另一个取消请求）先一步推进了状态。
		// 重新读取后按稳定结果处理：若已是 CANCELLED 视为成功重放，
		// 否则返回冲突——不能把"支付刚成功"误报成取消成功。
		err = s.orders.Transition(ctx, tx, orderID,
			domain.StatusPendingPayment, domain.StatusCancelled, CloseReasonUserCancelled)
		if err != nil {
			if !errors.Is(err, ErrOrderStateConflict) {
				return err
			}
			return s.resolveCancelConflict(ctx, tx, orderID)
		}

		// 2. 释放预占。released=false 表示预占已是终态或不存在：
		//    都不是故障，继续取消支付单即可（预占与支付是两条独立资源）。
		if _, err := s.releaser.ReleaseByOrderID(ctx, tx, orderID); err != nil {
			return err
		}

		// 3. 取消支付单。
		if err := s.paymentRead.CancelByOrderID(ctx, tx, orderID); err != nil {
			return err
		}

		// 4. Outbox 事件与业务同事务。
		return s.writeCancelledEvent(ctx, tx, orderID, order.UserID())
	})
}

// resolveCancelConflict 在条件更新未命中后决定对外结果。
//
// 只有"已经是 CANCELLED"才算成功重放；其余终态（COMPLETED / PAYMENT_FAILED /
// PAYMENT_EXPIRED）意味着订单被别人推进到了另一个结论，必须报冲突。
func (s *Service) resolveCancelConflict(ctx context.Context, tx db.Tx, orderID string) error {
	current, err := s.orders.GetByID(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if current.Status() == domain.StatusCancelled {
		return nil
	}
	return fmt.Errorf("%w: status=%s", ErrOrderStateConflict, current.Status())
}

// writeCancelledEvent 写入 order.cancelled 事件。
func (s *Service) writeCancelledEvent(ctx context.Context, tx db.Tx, orderID, userID string) error {
	payload, err := json.Marshal(map[string]any{
		"order_id":     orderID,
		"user_id":      userID,
		"close_reason": CloseReasonUserCancelled,
	})
	if err != nil {
		return fmt.Errorf("编码 order.cancelled 负载: %w", err)
	}
	return s.outbox.Write(ctx, tx, outbox.Event{
		EventID:       uuid.NewString(),
		EventType:     "order.cancelled",
		AggregateType: "order",
		AggregateID:   orderID,
		OccurredAt:    s.now().UTC(),
		Data:          payload,
	})
}
