// Command worker 启动后台任务进程。
//
// M0 只提供管理端口（live/ready/metrics），不启动 Kafka 消费者与 asynq；
// 这些能力分别在 M3/M4 接入。独立的管理端口避免后台任务与业务流量争抢端口。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/healthx"
	"github.com/vevila2020/order-sys/internal/platform/httpserver"
	"github.com/vevila2020/order-sys/internal/platform/httpx"
	"github.com/vevila2020/order-sys/internal/platform/loggerx"
	"github.com/vevila2020/order-sys/internal/platform/observability"
	"github.com/vevila2020/order-sys/internal/version"
)

// workerAdminAddr 是 worker 管理端口的默认地址。
// 与 API 的 :8080 分离，避免运维探针和业务流量互相影响。
const workerAdminAddr = ":8081"

func main() {
	os.Exit(run())
}

// run 返回退出码，保持 main 精简并且可测试。
func run() int {
	configPath := flag.String("config", os.Getenv("CONFIG_FILE"), "path to yaml config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String())
		return httpx.ExitOK
	}

	cfg, err := configx.Load(*configPath, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		return httpx.ExitFailure
	}

	log := loggerx.Init(loggerx.Options{
		ServiceName: cfg.Service.Name + "-worker",
		Env:         cfg.App.Env,
		Level:       cfg.Log.Level,
	})
	defer loggerx.Sync(log)
	log.Info("starting worker admin server",
		zap.String("addr", workerAdminAddr),
		zap.String("build", version.String()),
	)

	registry := healthx.NewRegistry(time.Second)
	registry.Register(healthx.Check{
		Name:     "config",
		Critical: true,
		Func:     func(ctx context.Context) error { return ctx.Err() },
	})

	// worker 的管理端口只暴露探针与指标，不挂业务路由。
	mux := http.NewServeMux()
	mux.Handle("/metrics", observability.NewMetrics().Handler())
	mux.HandleFunc("/health/live", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": string(healthx.StatusOK)})
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		report := registry.Run(r.Context())
		status := http.StatusOK
		if report.Status == healthx.StatusFail {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, report)
	})

	server := httpserver.NewHTTPServer(workerAdminAddr, mux, cfg.HTTP.ReadTimeout, cfg.HTTP.WriteTimeout)
	return httpx.RunServer(server, cfg.HTTP.ShutdownGrace, log)
}

// writeJSON 写入管理端口的 JSON 响应；序列化失败时退化为 500 空体，
// 因为管理端口不承担业务契约。
func writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
