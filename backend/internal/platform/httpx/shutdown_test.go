package httpx

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"
)

// startBlockingServer 启动一个必须等 release 关闭才返回的 handler，
// 用于验证 Shutdown 会等待在途请求。
func startBlockingServer(t *testing.T, release <-chan struct{}) (string, *http.Server, *time.Time) {
	t.Helper()
	finished := make(chan time.Time, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// 这是测试用的最短 server 构造，只关心 Shutdown 的等待语义。
	// 生产路径的 ReadHeaderTimeout 由 httpserver.NewHTTPServer 统一设置，
	// 因此对 G112（Slowloris）显式豁免，避免测试复制生产配置而掩盖真实意图。
	srv := &http.Server{ //nolint:gosec // 测试 server，生产配置见 httpserver.NewHTTPServer
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
			finished <- time.Now()
			w.WriteHeader(http.StatusOK)
		}),
	}

	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), srv, &time.Time{}
}

func TestServerWaitsForInFlightRequest(t *testing.T) {
	release := make(chan struct{})
	url, srv, _ := startBlockingServer(t, release)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// 地址来自测试自建的监听器，G107 按"变量 URL"报警在这里不成立。
		resp, err := http.Get(url) //nolint:gosec // 测试内自建地址
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	// 给客户端一点时间建立连接并进入 handler。
	time.Sleep(50 * time.Millisecond)

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()

	select {
	case err := <-shutdownDone:
		// Shutdown 提前返回即为失败，err 一并输出便于定位原因。
		t.Fatalf("Shutdown returned before the in-flight request completed: %v", err)
	case <-time.After(100 * time.Millisecond):
		// 预期：在途请求未完成时 Shutdown 仍在等待。
	}

	close(release)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	<-done
}

func TestShutdownTimesOutWithGraceExceeded(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	url, srv, _ := startBlockingServer(t, release)
	_ = url

	go func() {
		// 同上：地址来自测试自建监听器。
		resp, err := http.Get(url) //nolint:gosec // 测试内自建地址
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); err == nil {
		t.Fatal("expected shutdown timeout error")
	}
}

func TestRunServerReturnsOKWhenListenFails(t *testing.T) {
	// 端口已被占用时启动失败必须返回非 0，不能静默退出。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	// 该 server 用于制造"地址已被占用"，不会真正对外服务，
	// 因此不配置 ReadHeaderTimeout（G112 豁免理由同 startBlockingServer）。
	srv := &http.Server{Addr: ln.Addr().String(), Handler: http.NewServeMux()} //nolint:gosec // 不实际 Serve
	if code := RunServer(srv, time.Second, zap.NewNop()); code != ExitFailure {
		t.Fatalf("exit code: got %d want %d", code, ExitFailure)
	}
}

func TestSignalContextCancels(t *testing.T) {
	// 只验证构造与取消路径，不真正发送进程信号（会影响测试进程自身）。
	ctx, cancel := SignalContext()
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context did not cancel")
	}
}
