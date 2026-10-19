// Command api 启动 order-sys 的同步 HTTP 服务。
//
// 装配顺序刻意保持显式：解析配置 → 构造日志 → 构造各平台组件 → 装配路由 →
// 阻塞运行。任何启动期错误都直接退出并返回非 0 退出码，不留下半初始化进程。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/vevila2020/order-sys/internal/platform/authx"
	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/healthx"
	"github.com/vevila2020/order-sys/internal/platform/httpserver"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/loggerx"
	"github.com/vevila2020/order-sys/internal/platform/observability"
	"github.com/vevila2020/order-sys/internal/platform/ratelimit"
	"github.com/vevila2020/order-sys/internal/version"
)

func main() {
	os.Exit(run())
}

// run 返回退出码，便于测试和保持 main 精简。
func run() int {
	configPath := flag.String("config", defaultConfigPath(), "path to yaml config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String())
		return httpx.ExitOK
	}

	cfg, err := configx.Load(*configPath, "")
	if err != nil {
		// 配置错误时 logger 尚未初始化，直接写 stderr；错误信息不含密钥值。
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		return httpx.ExitFailure
	}

	log := loggerx.Init(loggerx.Options{
		ServiceName: cfg.Service.Name,
		Env:         cfg.App.Env,
		Level:       cfg.Log.Level,
	})
	defer loggerx.Sync(log)
	log.Info("starting api",
		zap.String("addr", cfg.HTTP.Addr),
		zap.String("build", version.String()),
	)

	tokens, err := authx.NewTokenService(cfg.JWT.Secret, cfg.JWT.TTL)
	if err != nil {
		log.Error("init auth", zap.Error(err))
		return httpx.ExitFailure
	}

	// M0 的 ready 只做配置自检；MySQL/Redis/Kafka 检查在 M1/M3 注册。
	registry := healthx.NewRegistry(time.Second)
	registry.Register(healthx.Check{
		Name:     "config",
		Critical: true,
		Func: func(ctx context.Context) error {
			// 配置在启动时已校验，这里只确认进程上下文仍有效。
			return ctx.Err()
		},
	})

	engine := httpserver.NewEngine(httpserver.ServerDeps{
		Config: httpserver.ServerConfig{
			MaxBodyBytes:     cfg.HTTP.MaxBodyBytes,
			RequestTimeout:   cfg.HTTP.RequestTimeout,
			RateLimitEnabled: cfg.RateLimit.Enabled,
		},
		Logger:    log,
		Metrics:   observability.NewMetrics(),
		Health:    registry,
		Tokens:    tokens,
		Limiter:   ratelimit.Noop{},
		LocalAuth: localAuthAllowed(cfg),
	})

	server := httpserver.NewHTTPServer(cfg.HTTP.Addr, engine, cfg.HTTP.ReadTimeout, cfg.HTTP.WriteTimeout)
	return httpx.RunServer(server, cfg.HTTP.ShutdownGrace, log)
}

// defaultConfigPath 允许通过 CONFIG_FILE 指定文件；留空时只使用默认值与环境变量，
// 这样容器内无需挂载配置文件也能启动。
func defaultConfigPath() string { return os.Getenv("CONFIG_FILE") }

// localAuthAllowed 同时校验环境与开关，任一不满足都不挂载签发端点。
// 双重校验是 ADR-0007 的要求，防止只改一个配置就误开放认证。
func localAuthAllowed(cfg configx.Config) bool {
	return cfg.LocalAuthEnabled && (cfg.App.Env == "local" || cfg.App.Env == "ci")
}
