package outbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SchemaVersion 是当前事件信封的版本。
//
// 消费方据此同时兼容 N 与 N-1（需求 §9.6）。提升版本意味着字段语义变化，
// 不是"新增可选字段"——后者不需要升版。
const SchemaVersion = 1

// 事件类型与聚合类型的合法字符集限制。事件名进入 Kafka topic / 路由键，
// 放任任意字符串会让消费方按前缀匹配时出现歧义。
const (
	maxEventTypeLen     = 100 // 与迁移里 VARCHAR(100) 对齐
	maxAggregateTypeLen = 64  // 与迁移里 VARCHAR(64) 对齐
)

// ErrInvalidEvent 表示事件信封字段不完整或格式不合法。
//
// 在写入前拒绝而不是依赖数据库报错：错误信息能直接指出缺哪个字段，
// 而不是一句 "Data too long for column" 或 "Column cannot be null"。
var ErrInvalidEvent = errors.New("outbox: 事件信封不合法")

// Event 是一条待写入 Outbox 的领域事件。
//
// 字段与需求 §9.6 的信封一一对应。trace_id 没有独立列，随 payload 一起存储，
// 这样 relay 发布时可以把它原样带到消息头里。
type Event struct {
	// EventID 是事件的全局唯一标识，由调用方用 uuid 生成。
	// 数据库的 uk_outbox_events_event_id 防止同一事件被重复写入。
	EventID string
	// EventType 形如 <domain>.<entity>.<past-tense-event>，例如 order.created。
	EventType string
	// AggregateType 是聚合类型（order / inventory / payment）。
	AggregateType string
	// AggregateID 是聚合标识，也是 Kafka 分区键——保证同一聚合的事件有序。
	AggregateID string
	// OccurredAt 是事件发生时间，统一 UTC。
	OccurredAt time.Time
	// TraceID 可选；为空表示当前上下文没有追踪信息（M5 接入 OTel 后由中间件写入）。
	TraceID string
	// Data 是业务负载，必须是合法 JSON。
	Data json.RawMessage
}

// Validate 检查信封字段。
//
// 校验集中在写入前：一条缺字段的事件写进库后，relay 发布时才会失败，那时
// 已经离故障现场很远，排查成本高得多。
func (e Event) Validate() error {
	if strings.TrimSpace(e.EventID) == "" {
		return fmt.Errorf("%w: event_id 不能为空", ErrInvalidEvent)
	}
	if err := validateEventType(e.EventType); err != nil {
		return err
	}
	if strings.TrimSpace(e.AggregateType) == "" || len(e.AggregateType) > maxAggregateTypeLen {
		return fmt.Errorf("%w: aggregate_type 为空或超过 %d 字符", ErrInvalidEvent, maxAggregateTypeLen)
	}
	if strings.TrimSpace(e.AggregateID) == "" {
		return fmt.Errorf("%w: aggregate_id 不能为空", ErrInvalidEvent)
	}
	if e.OccurredAt.IsZero() {
		return fmt.Errorf("%w: occurred_at 不能为零值", ErrInvalidEvent)
	}
	// Data 为空会让消费方无法区分"没有负载"与"字段丢失"；统一要求显式 `{}`。
	// json.Valid 同时拦住手写的半截 JSON。
	if len(e.Data) == 0 || !json.Valid(e.Data) {
		return fmt.Errorf("%w: data 必须是非空合法 JSON", ErrInvalidEvent)
	}
	return nil
}

// validateEventType 校验 <domain>.<event> 或 <domain>.<entity>.<event> 命名。
//
// 需求 §9.6 的正文写的是三段式 <domain>.<entity>.<past-tense-event>，但它同时
// 给出的逻辑事件目录全部是两段：order.created、inventory.reserved、
// payment.succeeded。两处矛盾时以目录为准——目录是消费方实际订阅的契约，
// 正文的段数只是描述性措辞。因此这里接受 2–3 段：
//   - 2 段是既有目录的形式；
//   - 3 段留给需要区分实体的场景（例如 inventory.reservation.confirmed）。
//
// 只做结构校验，不维护事件目录白名单：白名单会让新增事件必须先改平台包，
// 破坏模块自治；结构校验足以防止拼写风格漂移（如 "orderCreated"）。
func validateEventType(eventType string) error {
	if eventType == "" || len(eventType) > maxEventTypeLen {
		return fmt.Errorf("%w: event_type 为空或超过 %d 字符", ErrInvalidEvent, maxEventTypeLen)
	}
	parts := strings.Split(eventType, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return fmt.Errorf("%w: event_type 必须是 <domain>.<event> 或 <domain>.<entity>.<event>，got %q", ErrInvalidEvent, eventType)
	}
	for _, p := range parts {
		if p == "" {
			return fmt.Errorf("%w: event_type 存在空段，got %q", ErrInvalidEvent, eventType)
		}
		for i := 0; i < len(p); i++ {
			c := p[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
				continue
			}
			return fmt.Errorf("%w: event_type 只允许小写字母、数字与下划线，got %q", ErrInvalidEvent, eventType)
		}
	}
	return nil
}

// Payload 返回写入 payload 列的 JSON。
//
// 列里存的是"信封 + 负载"而不是裸业务负载：
//
//   - schema_version 是消费方做兼容演进的依据，迁移文件的注释也约定了它随
//     payload 一起存；
//   - trace_id 没有独立列，只能放在这里，否则 relay 发布后链路就断了；
//   - data 嵌套一层，避免业务字段与信封字段重名（例如业务自身也有 version）。
//
// 这样 relay 可以原样把 payload 作为消息体发布，不必再拼装信封。
func (e Event) Payload() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	envelope := struct {
		SchemaVersion int             `json:"schema_version"`
		TraceID       string          `json:"trace_id,omitempty"`
		Data          json.RawMessage `json:"data"`
	}{
		SchemaVersion: SchemaVersion,
		TraceID:       e.TraceID,
		Data:          e.Data,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: 编码 payload: %v", ErrInvalidEvent, err)
	}
	return raw, nil
}
