package eventx

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEnvelopeJSONUsesDesignFields(t *testing.T) {
	occurred := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	env, err := New("order.created", "ord-1", []byte(`{"order_id":"ord-1"}`), occurred)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"event_id", "event_type", "aggregate_id", "occurred_at", "payload"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing %s in %s", key, raw)
		}
	}
	if got["event_type"] != "order.created" || got["aggregate_id"] != "ord-1" {
		t.Fatalf("identity = %s", raw)
	}
	payload, ok := got["payload"].(map[string]any)
	if !ok || payload["order_id"] != "ord-1" {
		t.Fatalf("payload = %#v", got["payload"])
	}
}

func TestNewRejectsInvalidPayload(t *testing.T) {
	_, err := New("order.created", "ord-1", []byte(`not-json`), time.Now().UTC())
	if err == nil {
		t.Fatal("expected error")
	}
}
