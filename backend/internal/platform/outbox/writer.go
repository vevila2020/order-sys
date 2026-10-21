package outbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/vevila2020/order-sys/internal/platform/db"
)

// Writer 把事件写入 outbox_events。
//
// 接口而不是具体类型：各模块的 infra 只依赖这个端口，测试可以用假实现验证
// "哪些事件被写入"，不必连数据库。
type Writer interface {
	// Write 在调用方事务内插入一条 PENDING 事件。
	//
	// tx 必须是业务写入所在的同一个事务：换一个事务就失去了 Outbox 的意义
	// （业务回滚而事件留下，或反之）。
	Write(ctx context.Context, tx db.Tx, event Event) error
}

// NewWriter 构造 Outbox 写入器。无状态，事务句柄按调用传入。
func NewWriter() Writer { return writer{} }

type writer struct{}

// Write 实现 Writer.Write。
//
// 只写 PENDING 与 available_at = 当前时间：
//   - status 由数据库默认值 PENDING 提供，这里显式写出，让 SQL 自解释；
//   - available_at 立即到期，relay 下一轮就能取走。退避是 relay 在发布失败后
//     才修改的字段，写入端不应预留延迟；
//   - attempts 与 published_at 留给 relay，M1 不触碰（任务约定）。
func (writer) Write(ctx context.Context, tx db.Tx, event Event) error {
	if tx == nil {
		return errors.New("outbox: tx 不能为空")
	}
	payload, err := event.Payload()
	if err != nil {
		return err
	}

	// 时间统一转 UTC：OccurredAt 可能带着本地时区，DATETIME(6) 不保存时区，
	// 不转换会让库里出现"看起来是过去/未来"的时间。
	occurredAt := event.OccurredAt.UTC()

	err = tx.WithContext(ctx).Exec(
		`INSERT INTO outbox_events
		        (event_id, event_type, aggregate_type, aggregate_id, payload,
		         status, attempts, available_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 'PENDING', 0, UTC_TIMESTAMP(6), ?)`,
		event.EventID, event.EventType, event.AggregateType, event.AggregateID,
		string(payload), occurredAt,
	).Error
	if err != nil {
		// event_id 重复说明同一事件被写了两次（重试路径忘了跳过，或调用方
		// 复用了 ID）。唯一索引拦下来是对的：重复事件会让消费方多执行一次。
		if db.IsDuplicateKey(err) {
			return fmt.Errorf("%w: event_id=%s 已存在", ErrDuplicateEvent, event.EventID)
		}
		return fmt.Errorf("outbox: 写入事件 %s: %w", event.EventID, err)
	}
	return nil
}

// ErrDuplicateEvent 表示同一 event_id 已存在。
//
// 与 ErrInvalidEvent 分开：前者是调用方的幂等/重试问题，后者是信封构造问题，
// 排查方向完全不同。
var ErrDuplicateEvent = errors.New("outbox: 事件已存在")
