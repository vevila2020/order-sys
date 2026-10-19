// Package configx 负责加载并校验服务配置。
//
// 配置来源优先级：环境变量 > YAML 文件 > 代码内默认值。所有环境变量名由
// 结构体 mapstructure 标签加下划线生成（例如 http.addr -> HTTP_ADDR），
// 这样部署环境遵守 12-factor，仓库里只保留非敏感默认值。
package configx

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// Config 是服务完整配置。M0 只使用其中一部分字段，其余为后续里程碑预留，
// 提前建模可以避免之后反复改动配置契约。
type Config struct {
	App         AppConfig         `mapstructure:"app"`
	Service     ServiceConfig     `mapstructure:"service"`
	HTTP        HTTPConfig        `mapstructure:"http"`
	MySQL       MySQLConfig       `mapstructure:"mysql"`
	Redis       RedisConfig       `mapstructure:"redis"`
	Kafka       KafkaConfig       `mapstructure:"kafka"`
	Asynq       AsynqConfig       `mapstructure:"asynq"`
	Log         LogConfig         `mapstructure:"log"`
	OTLP        OTLPConfig        `mapstructure:"otlp"`
	OTel        OTelConfig        `mapstructure:"otel"`
	JWT         JWTConfig         `mapstructure:"jwt"`
	Payment     PaymentConfig     `mapstructure:"payment"`
	FlashSale   FlashSaleConfig   `mapstructure:"flash_sale"`
	Idempotency IdempotencyConfig `mapstructure:"idempotency"`
	RateLimit   RateLimitConfig   `mapstructure:"rate_limit"`
	Retention   RetentionConfig   `mapstructure:"retention"`
	// LocalAuthEnabled 是本地模拟签发 JWT 的开关；Validate 会强制它在非 local/ci 环境为 false。
	LocalAuthEnabled bool `mapstructure:"local_auth_enabled"`
}

// AppConfig 描述运行环境信息，用于日志与指标标签。
type AppConfig struct {
	Env string `mapstructure:"env"`
}

// ServiceConfig 是服务自身标识。
type ServiceConfig struct {
	Name string `mapstructure:"name"`
}

// HTTPConfig 是 HTTP 服务端参数。
type HTTPConfig struct {
	Addr         string        `mapstructure:"addr"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
	// RequestTimeout 是单个请求的 context 截止时间，独立于连接的写超时。
	RequestTimeout time.Duration `mapstructure:"request_timeout"`
	// MaxBodyBytes 限制请求体大小，防止超大 JSON 占满内存。
	MaxBodyBytes  int64         `mapstructure:"max_body_bytes"`
	ShutdownGrace time.Duration `mapstructure:"shutdown_grace"`
}

// MySQLConfig 是权威存储连接参数；DSN 含密码，只能由环境变量注入。
type MySQLConfig struct {
	DSN          string `mapstructure:"dsn"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
}

// RedisConfig 是 Redis 连接参数（M3 开始使用）。
type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
}

// KafkaConfig 是业务消息配置（M3 开始使用）。
type KafkaConfig struct {
	Brokers []string `mapstructure:"brokers"`
	GroupID string   `mapstructure:"group_id"`
}

// AsynqConfig 是后台任务队列配置（M3 开始使用）。
type AsynqConfig struct {
	RedisAddr string `mapstructure:"redis_addr"`
}

// LogConfig 是结构化日志配置。
type LogConfig struct {
	Level string `mapstructure:"level"`
}

// OTLPConfig 是追踪导出配置，M0 仅保留配置项。
type OTLPConfig struct {
	Endpoint string `mapstructure:"endpoint"`
}

// OTelConfig 是 OpenTelemetry SDK 参数；采样率在 M5 接入时使用。
type OTelConfig struct {
	TracesSamplerArg float64 `mapstructure:"traces_sampler_arg"`
}

// JWTConfig 描述本地模拟认证参数。
type JWTConfig struct {
	Secret string        `mapstructure:"secret"`
	TTL    time.Duration `mapstructure:"ttl"`
}

// PaymentConfig 是模拟支付渠道参数。
type PaymentConfig struct {
	CallbackToken string        `mapstructure:"callback_token"`
	Timeout       time.Duration `mapstructure:"timeout"`
}

// FlashSaleConfig 是秒杀受理参数（M3 使用）。
type FlashSaleConfig struct {
	QueueTimeout time.Duration `mapstructure:"queue_timeout"`
}

// IdempotencyConfig 描述幂等记录保留期（M1 使用）。
type IdempotencyConfig struct {
	TTL time.Duration `mapstructure:"ttl"`
}

// RateLimitConfig 是限流配置。PerUser 形如 "100/min"，解析结果放在 Limit/Window。
type RateLimitConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	PerUser string `mapstructure:"per_user"`
	// Limit 与 Window 由 Validate 从 PerUser 解析，不接受外部配置。
	Limit  int           `mapstructure:"-"`
	Window time.Duration `mapstructure:"-"`
}

// RetentionConfig 是数据保留期配置（M4 使用）。
type RetentionConfig struct {
	Idempotency time.Duration `mapstructure:"idempotency"`
	Outbox      time.Duration `mapstructure:"outbox"`
	Processed   time.Duration `mapstructure:"processed"`
	DeadLetter  time.Duration `mapstructure:"dead_letter"`
	Delivery    time.Duration `mapstructure:"delivery"`
	Audit       time.Duration `mapstructure:"audit"`
}

// defaults 是本地开发基线。使用 SetDefault 而非仅依赖 YAML，原因是 viper 的
// AutomaticEnv 只有在键已存在时才会参与 Unmarshal；显式默认值同时保证
// 空配置也能启动并通过校验。
var defaults = map[string]any{
	"app.env":                  "local",
	"service.name":             "order-sys",
	"http.addr":                ":8080",
	"http.read_timeout":        "10s",
	"http.write_timeout":       "15s",
	"http.request_timeout":     "10s",
	"http.max_body_bytes":      int64(1 << 20),
	"http.shutdown_grace":      "15s",
	"mysql.dsn":                "",
	"mysql.max_open_conns":     25,
	"mysql.max_idle_conns":     10,
	"redis.addr":               "localhost:6379",
	"redis.password":           "",
	"kafka.brokers":            []string{"localhost:9092"},
	"kafka.group_id":           "order-sys",
	"asynq.redis_addr":         "",
	"log.level":                "info",
	"otlp.endpoint":            "",
	"otlp.traces_sampler_arg":  0.1,
	"jwt.secret":               "",
	"jwt.ttl":                  "1h",
	"payment.callback_token":   "",
	"payment.timeout":          "15m",
	"flash_sale.queue_timeout": "5m",
	"idempotency.ttl":          "24h",
	"rate_limit.enabled":       false,
	"rate_limit.per_user":      "100/min",
	"retention.idempotency":    "48h",
	"retention.outbox":         "168h",
	"retention.processed":      "168h",
	"retention.dead_letter":    "720h",
	"retention.delivery":       "720h",
	"retention.audit":          "8760h",
	"local_auth_enabled":       true,
}

// localJWTSecret 是 APP_ENV=local 时的开发密钥，长度满足 HS256 的 32 字节下限。
// 它只用于本地模拟身份，非 local 环境必须显式注入 JWT_SECRET。
const localJWTSecret = "dev-only-insecure-jwt-secret-32bytes!" //nolint:gosec // 仅 local 环境的开发默认值

// localPaymentToken 是本地模拟支付回调令牌，非 local 环境必须显式注入。
const localPaymentToken = "dev-only-simulated-payment-token" //nolint:gosec // 仅 local 环境的开发默认值

// Load 从 path 读取 YAML 并应用环境变量覆盖，然后执行 Validate。
// path 为空时只使用默认值与环境变量；envPrefix 为空表示环境变量不带前缀。
func Load(path, envPrefix string) (Config, error) {
	v := viper.New()
	// SetEnvKeyReplacer 把嵌套键转换为扁平环境变量名，这是 12-factor 覆盖的关键。
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.SetEnvPrefix(envPrefix)
	v.AutomaticEnv()
	for key, val := range defaults {
		v.SetDefault(key, val)
	}
	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return Config{}, fmt.Errorf("read config %q: %w", path, err)
		}
	}

	var cfg Config
	// StringToSliceHookFunc 支持 KAFKA_BROKERS=a,b 这类逗号分隔的环境变量。
	if err := v.Unmarshal(&cfg, viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	))); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate 检查必填项、格式与取值范围，并把 RATE_LIMIT_PER_USER 解析为数值。
// 错误信息只包含键名和原因，绝不回显密钥、密码或 DSN 的值。
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	switch c.App.Env {
	case "local", "ci", "staging":
	default:
		add("app.env must be one of local, ci, staging")
	}
	if strings.TrimSpace(c.Service.Name) == "" {
		add("service.name is required")
	}
	if strings.TrimSpace(c.HTTP.Addr) == "" {
		add("http.addr is required")
	}
	if c.HTTP.ReadTimeout <= 0 {
		add("http.read_timeout must be positive")
	}
	if c.HTTP.WriteTimeout <= 0 {
		add("http.write_timeout must be positive")
	}
	if c.HTTP.RequestTimeout <= 0 {
		add("http.request_timeout must be positive")
	}
	if c.HTTP.ShutdownGrace <= 0 {
		add("http.shutdown_grace must be positive")
	}
	const minBody, maxBody = int64(1024), int64(10 << 20)
	if c.HTTP.MaxBodyBytes < minBody || c.HTTP.MaxBodyBytes > maxBody {
		add("http.max_body_bytes must be between %d and %d", minBody, maxBody)
	}
	if c.MySQL.MaxOpenConns <= 0 || c.MySQL.MaxOpenConns > 1000 {
		add("mysql.max_open_conns must be between 1 and 1000")
	}
	if c.MySQL.MaxIdleConns < 0 || c.MySQL.MaxIdleConns > c.MySQL.MaxOpenConns {
		add("mysql.max_idle_conns must be between 0 and mysql.max_open_conns")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level must be one of debug, info, warn, error")
	}
	if c.OTel.TracesSamplerArg < 0 || c.OTel.TracesSamplerArg > 1 {
		add("otel.traces_sampler_arg must be between 0 and 1")
	}
	if c.JWT.TTL <= 0 {
		add("jwt.ttl must be positive")
	}
	if c.Payment.Timeout <= 0 {
		add("payment.timeout must be positive")
	}
	if c.FlashSale.QueueTimeout <= 0 {
		add("flash_sale.queue_timeout must be positive")
	}
	if c.Idempotency.TTL <= 0 {
		add("idempotency.ttl must be positive")
	}

	// 非 local/ci 环境禁用本地签发端点，并要求显式注入密钥；这里只补默认值，
	// 密钥值本身不会出现在任何错误信息里。
	if c.App.Env == "local" {
		if c.JWT.Secret == "" {
			c.JWT.Secret = localJWTSecret
		}
		if c.Payment.CallbackToken == "" {
			c.Payment.CallbackToken = localPaymentToken
		}
	}
	if len(c.JWT.Secret) < 32 {
		add("jwt.secret is required and must be at least 32 bytes")
	}
	if c.Payment.CallbackToken == "" {
		add("payment.callback_token is required")
	}
	if c.App.Env != "local" && c.App.Env != "ci" && c.LocalAuthEnabled {
		add("local_auth_enabled must be false unless app.env is local or ci")
	}

	limit, window, err := ParseRate(c.RateLimit.PerUser)
	if err != nil {
		add("rate_limit.per_user: %v", err)
	} else {
		c.RateLimit.Limit = limit
		c.RateLimit.Window = window
	}

	return errors.Join(errs...)
}

// ParseRate 解析 "<次数>/<窗口>" 形式的限流配置，窗口支持 s/m/h。
func ParseRate(spec string) (int, time.Duration, error) {
	parts := strings.Split(strings.TrimSpace(spec), "/")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected <limit>/<window>, e.g. 100/min")
	}
	limit, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || limit <= 0 {
		return 0, 0, fmt.Errorf("limit must be a positive integer")
	}
	unit := strings.TrimSpace(parts[1])
	var window time.Duration
	switch unit {
	case "s", "sec", "second":
		window = time.Second
	case "m", "min", "minute":
		window = time.Minute
	case "h", "hour":
		window = time.Hour
	default:
		return 0, 0, fmt.Errorf("unsupported window %q (want s, m or h)", unit)
	}
	return limit, window, nil
}
