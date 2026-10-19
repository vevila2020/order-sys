// Package ctxkeys 集中定义放入 context 的关联标识键。
//
// 单独成包的原因：httpx、loggerx、middleware 都需要读写同一批键，若各自
// 定义未导出类型，跨包读取会拿不到值。使用专用类型而非裸 string 还能满足
// golangci-lint 的 SA1029（禁止用内置类型作为 context key）。
package ctxkeys

// Key 是 context 键类型；所有取值都由本包常量声明，避免键冲突。
type Key string

const (
	// RequestID 是 HTTP 请求 ID，由 request_id 中间件写入。
	RequestID Key = "request_id"
	// TraceID 是链路追踪 ID，M5 接入 OTel 后由追踪中间件写入。
	TraceID Key = "trace_id"
	// SpanID 是当前 span ID。
	SpanID Key = "span_id"
	// OrderID 是业务关联的订单 ID。
	OrderID Key = "order_id"
	// EventID 是事件关联 ID。
	EventID Key = "event_id"
)
