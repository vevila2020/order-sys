// Package loggerx 提供统一的结构化 JSON 日志。
//
// 日志字段约定来自需求 FR-028：每条日志都带 service_name、env、version，
// 请求内日志再通过 context 附加 request_id、trace_id、span_id、order_id、event_id。
// 该包刻意不记录请求体和任何敏感字段，避免日志库替调用方做危险的默认行为。
package loggerx

import (
	"context"
	"os"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/ctxkeys"
	"github.com/vevila2020/order-sys/internal/version"
)

// Options 是构造 logger 所需的配置，独立于 configx 便于测试直接构造。
type Options struct {
	ServiceName string
	Env         string
	Level       string
	// Output 为空时写到 stdout；测试可注入 buffer。
	Output ioWriter
}

// ioWriter 是 zap 所需的最小写入接口，避免测试依赖 os.File。
type ioWriter interface {
	Write(p []byte) (int, error)
}

var (
	// base 是进程级基础 logger，所有 WithContext 派生都从它复制字段。
	base *zap.Logger
	mu   sync.RWMutex
)

// Init 初始化全局 logger 并返回它。重复调用是安全的，后一次会替换前一次。
func Init(opts Options) *zap.Logger {
	l := New(opts)
	mu.Lock()
	base = l
	mu.Unlock()
	return l
}

// New 构造一个不修改全局状态的 logger，便于测试与 main 中显式注入。
func New(opts Options) *zap.Logger { return newLogger(opts) }

// newLogger 完成 encoder 与 core 的组装。
func newLogger(opts Options) *zap.Logger {
	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.MillisDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	out := opts.Output
	if out == nil {
		out = os.Stdout
	}
	// 同步写入 stdout，保证容器日志不丢行；高频日志的吞吐问题由日志级别控制。
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.AddSync(out),
		parseLevel(opts.Level),
	)

	// 每条日志都携带服务身份字段，使多服务日志可以直接按服务过滤。
	return zap.New(core).With(
		zap.String("service_name", opts.ServiceName),
		zap.String("env", opts.Env),
		zap.String("version", version.Version),
	)
}

// parseLevel 把配置字符串映射为 zap 级别，未知值回退到 info。
// 配置校验已保证取值合法，这里的回退只防御直接调用 New 的场景。
func parseLevel(level string) zapcore.Level {
	switch level {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// FromConfig 依据服务配置构造 logger。
func FromConfig(cfg configx.Config, output ioWriter) *zap.Logger {
	return New(Options{
		ServiceName: cfg.Service.Name,
		Env:         cfg.App.Env,
		Level:       cfg.Log.Level,
		Output:      output,
	})
}

// WithContext 返回带请求关联字段的 logger；缺失或空值的字段会被跳过，
// 因此同一函数既能用于 HTTP 请求，也能用于后台任务。
func WithContext(ctx context.Context, l *zap.Logger) *zap.Logger {
	if ctx == nil || l == nil {
		return l
	}
	keys := []ctxkeys.Key{
		ctxkeys.RequestID,
		ctxkeys.TraceID,
		ctxkeys.SpanID,
		ctxkeys.OrderID,
		ctxkeys.EventID,
	}
	fields := make([]zap.Field, 0, len(keys))
	for _, key := range keys {
		if v, ok := ctx.Value(key).(string); ok && v != "" {
			fields = append(fields, zap.String(string(key), v))
		}
	}
	if len(fields) == 0 {
		return l
	}
	return l.With(fields...)
}

// Default 返回全局 logger；未初始化时返回 nop logger，
// 使测试或提前失败的启动路径不会因 nil 指针 panic。
func Default() *zap.Logger {
	mu.RLock()
	l := base
	mu.RUnlock()
	if l == nil {
		return zap.NewNop()
	}
	return l
}

// Sync 刷新缓冲。stdout 通常返回 "invalid argument"，对非文件输出视为正常，
// 因此调用方不需要处理返回值。
func Sync(l *zap.Logger) {
	if l == nil {
		return
	}
	_ = l.Sync()
}
