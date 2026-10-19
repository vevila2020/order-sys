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
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		finished <- time.Now()
		w.WriteHeader(http.StatusOK)
	})}

	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), srv, &time.Time{}
}

func TestServerWaitsForInFlightRequest(t *testing.T) {
	release := make(chan struct{})
	url, srv, _ := startBlockingServer(t, release)

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.Get(url)
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
	case <-shutdownDone:
		t.Fatal("Shutdown returned before the in-flight request completed")
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
		resp, err := http.Get(url)
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
	defer ln.Close()

	srv := &http.Server{Addr: ln.Addr().String(), Handler: http.NewServeMux()}
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
