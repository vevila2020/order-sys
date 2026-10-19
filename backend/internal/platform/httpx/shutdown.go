package httpx

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
)

// 进程退出码约定：0 表示正常关闭；非 0 表示启动失败或关闭超时。
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitTimeout = 2
)

// SignalContext 返回收到 SIGINT/SIGTERM 时取消的 context。
// 使用 signal.NotifyContext 而不是手写 channel，保证信号到达后上下文
// 能正确传递给所有后台循环。
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// RunServer 启动 HTTP 服务并阻塞等待关闭信号，在宽限期内完成在途请求。
//
// 返回退出码而不是调用 os.Exit，便于测试和多服务编排（worker 管理端口）复用。
// 启动失败或关闭超时返回非 0，正常关闭返回 0。
func RunServer(server *http.Server, grace time.Duration, log *zap.Logger) int {
	ctx, stop := SignalContext()
	defer stop()

	// 在独立 goroutine 中监听；主流程等待信号，避免把 Shutdown 逻辑
	// 写在 ListenAndServe 之后的不可达位置。
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		// 启动失败（端口占用等）或服务被外部关闭。
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", zap.Error(err))
			return ExitFailure
		}
		return ExitOK
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// 停止接收新连接，等待在途请求；宽限期由配置提供。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		// 超时则强制关闭，保证进程最终退出。
		_ = server.Close()
		log.Error("graceful shutdown timed out", zap.Error(err))
		return ExitTimeout
	}
	log.Info("http server stopped")
	return ExitOK
}
