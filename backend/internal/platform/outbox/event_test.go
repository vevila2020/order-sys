package outbox_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vevila2020/order-sys/internal/platform/outbox"
)

func validEvent() outbox.Event {
	return outbox.Event{
		EventID:       "evt-1",
		EventType:     "order.created",
		AggregateType: "order",
		AggregateID:   "ord-1",
		OccurredAt:    time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		Data:          json.RawMessage(`{"order_id":"ord-1"}`),
	}
}

func TestEventValidateAcceptsWellFormed(t *testing.T) {
	if err := validEvent().Validate(); err != nil {
		t.Fatalf("合法事件被拒绝: %v", err)
	}
}

func TestEventValidateRejectsMissingFields(t *testing.T) {
	mutations := map[string]func(*outbox.Event){
		"event_id":       func(e *outbox.Event) { e.EventID = "  " },
		"event_type":     func(e *outbox.Event) { e.EventType = "" },
		"aggregate_type": func(e *outbox.Event) { e.AggregateType = "" },
		"aggregate_id":   func(e *outbox.Event) { e.AggregateID = "" },
		"occurred_at":    func(e *outbox.Event) { e.OccurredAt = time.Time{} },
		"data":           func(e *outbox.Event) { e.Data = nil },
	}
	for name, mutate := range mutations {
		ev := validEvent()
		mutate(&ev)
		if err := ev.Validate(); !errors.Is(err, outbox.ErrInvalidEvent) {
			t.Errorf("缺少 %s: err = %v, want ErrInvalidEvent", name, err)
		}
	}
}

// 信封字段会在库外被消费，格式漂移比字段缺失更难发现，因此在写入前锁定。
func TestEventTypeNaming(t *testing.T) {
	valid := []string{
		"order.created", // 需求 §9.6 事件目录的两段式
		"inventory.reserved",
		"payment.succeeded",
		"flashsale.rejected",
		"inventory.reservation.confirmed", // 三段式留给需要区分实体的场景
	}
	for _, et := range valid {
		ev := validEvent()
		ev.EventType = et
		if err := ev.Validate(); err != nil {
			t.Errorf("event_type %q 应合法: %v", et, err)
		}
	}

	invalid := []string{
		"orderCreated",       // 不是三段式
		"Order.Created",      // 大写
		"order..created",     // 空段
		"order.created.v2.x", // 段数不对
		"order.created-v2",   // 连字符不在允许集内
		strings.Repeat("a", 101),
	}
	for _, et := range invalid {
		ev := validEvent()
		ev.EventType = et
		if err := ev.Validate(); !errors.Is(err, outbox.ErrInvalidEvent) {
			t.Errorf("event_type %q: err = %v, want ErrInvalidEvent", et, err)
		}
	}
}

// payload 必须自带 schema_version 与 data 层：消费方靠前者做兼容演进，
// 后者避免业务字段与信封字段重名。
func TestPayloadCarriesEnvelope(t *testing.T) {
	ev := validEvent()
	ev.TraceID = "trace-1"

	raw, err := ev.Payload()
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}

	var got struct {
		SchemaVersion int             `json:"schema_version"`
		TraceID       string          `json:"trace_id"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload 不是合法 JSON: %v", err)
	}
	if got.SchemaVersion != outbox.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, outbox.SchemaVersion)
	}
	if got.TraceID != "trace-1" {
		t.Errorf("trace_id = %q, want trace-1", got.TraceID)
	}
	if string(got.Data) != `{"order_id":"ord-1"}` {
		t.Errorf("data = %s", got.Data)
	}
}

// trace_id 为空时省略而不是写空串：消费方用字段存在性判断链路是否可关联。
func TestPayloadOmitsEmptyTraceID(t *testing.T) {
	raw, err := validEvent().Payload()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "trace_id") {
		t.Fatalf("空 trace_id 不应出现: %s", raw)
	}
}

// 非法 JSON 必须在构造期被拒绝，而不是留给 MySQL 的 JSON 列报错。
func TestPayloadRejectsInvalidJSON(t *testing.T) {
	ev := validEvent()
	ev.Data = json.RawMessage(`{"broken":`)
	if _, err := ev.Payload(); !errors.Is(err, outbox.ErrInvalidEvent) {
		t.Fatalf("err = %v, want ErrInvalidEvent", err)
	}
}
