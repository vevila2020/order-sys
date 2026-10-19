package loggerx

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/vevila2020/order-sys/internal/platform/ctxkeys"
)

func TestLoggerWritesJSONWithBaseFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{ServiceName: "order-sys", Env: "test", Level: "info", Output: &buf})
	l.Info("started")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, buf.String())
	}
	for _, key := range []string{"ts", "level", "msg", "service_name", "env", "version"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing field %q in %v", key, got)
		}
	}
	if got["level"] != "info" || got["msg"] != "started" {
		t.Errorf("unexpected level/msg: %v", got)
	}
}

func TestLoggerFiltersByLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{ServiceName: "s", Env: "test", Level: "warn", Output: &buf})
	l.Info("hidden")
	l.Warn("shown")

	if bytes.Contains(buf.Bytes(), []byte("hidden")) {
		t.Errorf("info should be filtered at warn level: %q", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("shown")) {
		t.Errorf("warn should be emitted: %q", buf.String())
	}
}

func TestWithContextAddsOnlyPresentFields(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{ServiceName: "s", Env: "test", Level: "info", Output: &buf})

	ctx := context.WithValue(context.Background(), ctxkeys.RequestID, "req-1")
	ctx = context.WithValue(ctx, ctxkeys.TraceID, "")
	WithContext(ctx, l).Info("handled")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["request_id"] != "req-1" {
		t.Errorf("request_id: got %v", got["request_id"])
	}
	if _, ok := got["trace_id"]; ok {
		t.Errorf("empty trace_id should be skipped: %v", got)
	}
}

func TestSyncDoesNotPanic(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{ServiceName: "s", Env: "test", Level: "info", Output: &buf})
	Sync(l)
	Sync(nil)
}

func TestDefaultWithoutInitIsUsable(t *testing.T) {
	// 全局 logger 未初始化时必须是可安全调用的 nop logger。
	Default().Info("no panic")
}
