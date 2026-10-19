package configx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeYAML 在临时目录写入配置片段，测试互不共享状态。
func writeYAML(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return path
}

func TestLoadAppliesYAMLDefaultsAndEnvOverrides(t *testing.T) {
	// local 环境允许默认 JWT 密钥，因此只需提供 DSN 之外的必填项即可加载成功。
	path := writeYAML(t, `
app:
  env: local
http:
  addr: ":18080"
  max_body_bytes: 2048
mysql:
  dsn: "user:pass@tcp(localhost:3306)/order_sys"
log:
  level: warn
`)
	t.Setenv("HTTP_ADDR", ":19090")
	t.Setenv("KAFKA_BROKERS", "k1:9092,k2:9092")
	t.Setenv("RATE_LIMIT_PER_USER", "10/s")

	cfg, err := Load(path, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Addr != ":19090" {
		t.Errorf("env override not applied: got %q", cfg.HTTP.Addr)
	}
	if got, want := cfg.HTTP.MaxBodyBytes, int64(2048); got != want {
		t.Errorf("yaml value: got %d want %d", got, want)
	}
	if got, want := cfg.Kafka.Brokers, []string{"k1:9092", "k2:9092"}; !equal(got, want) {
		t.Errorf("brokers: got %v want %v", got, want)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("log level: got %q", cfg.Log.Level)
	}
	if cfg.RateLimit.Limit != 10 || cfg.RateLimit.Window != time.Second {
		t.Errorf("rate parsed: got %d/%s", cfg.RateLimit.Limit, cfg.RateLimit.Window)
	}
	if cfg.HTTP.ReadTimeout != 10*time.Second {
		t.Errorf("default read timeout: got %s", cfg.HTTP.ReadTimeout)
	}
}

func TestLoadFillsLocalSecretsButNeverNonLocal(t *testing.T) {
	// 这是 FR-026/NFR-005 的核心：非 local 环境不能回退到开发密钥。
	path := writeYAML(t, "app:\n  env: staging\n")
	_, err := Load(path, "")
	if err == nil {
		t.Fatal("expected error for staging without secrets")
	}
	if !strings.Contains(err.Error(), "jwt.secret") || !strings.Contains(err.Error(), "payment.callback_token") {
		t.Fatalf("unexpected error: %v", err)
	}
	// 错误信息不得泄露密钥值（这里用哨兵值验证回显行为）。
	if strings.Contains(err.Error(), localJWTSecret) || strings.Contains(err.Error(), localPaymentToken) {
		t.Fatalf("error leaks secret value: %v", err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	t.Setenv("APP_ENV", "local")
	t.Setenv("HTTP_MAX_BODY_BYTES", "10") // 低于 1 KiB 下限
	t.Setenv("LOG_LEVEL", "verbose")
	t.Setenv("RATE_LIMIT_PER_USER", "0/min")
	path := writeYAML(t, "app:\n  env: local\n")

	_, err := Load(path, "")
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"http.max_body_bytes", "log.level", "rate_limit.per_user"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

func TestLoadRejectsNonLocalAuthEnabled(t *testing.T) {
	// staging 不在 local/ci 白名单内，开启本地签发必须被拒绝；依赖显式密钥
	// 保证测试命中的是开关校验而不是密钥缺失校验。
	t.Setenv("APP_ENV", "staging")
	t.Setenv("LOCAL_AUTH_ENABLED", "true")
	t.Setenv("JWT_SECRET", strings.Repeat("k", 32))
	t.Setenv("PAYMENT_CALLBACK_TOKEN", "token")
	path := writeYAML(t, "app:\n  env: staging\n")

	_, err := Load(path, "")
	if err == nil || !strings.Contains(err.Error(), "local_auth_enabled") {
		t.Fatalf("expected local_auth_enabled error, got %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml"), ""); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseRate(t *testing.T) {
	cases := []struct {
		spec    string
		limit   int
		window  time.Duration
		wantErr bool
	}{
		{"100/min", 100, time.Minute, false},
		{"5/s", 5, time.Second, false},
		{"3/h", 3, time.Hour, false},
		{"100", 0, 0, true},
		{"x/min", 0, 0, true},
		{"10/day", 0, 0, true},
	}
	for _, tc := range cases {
		limit, window, err := ParseRate(tc.spec)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", tc.spec)
			}
			continue
		}
		if err != nil || limit != tc.limit || window != tc.window {
			t.Errorf("%q: got %d/%s (%v)", tc.spec, limit, window, err)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestExampleConfigsLoad(t *testing.T) {
	// 示例配置必须能配合环境变量直接启动，防止文档与代码漂移。
	t.Setenv("APP_ENV", "local")
	t.Setenv("MYSQL_DSN", "order:order@tcp(localhost:3306)/order_sys")

	cfg, err := Load("../../../config/local.example.yaml", "")
	if err != nil {
		t.Fatalf("local example: %v", err)
	}
	if cfg.HTTP.RequestTimeout != 10*time.Second {
		t.Errorf("request timeout: got %s", cfg.HTTP.RequestTimeout)
	}
	if cfg.OTel.TracesSamplerArg != 0.1 {
		t.Errorf("otel sampler: got %v", cfg.OTel.TracesSamplerArg)
	}

	// ci 示例本身不含密钥，必须显式注入后才能加载。
	t.Setenv("APP_ENV", "ci")
	t.Setenv("JWT_SECRET", strings.Repeat("k", 32))
	t.Setenv("PAYMENT_CALLBACK_TOKEN", "token")
	if _, err := Load("../../../config/ci.example.yaml", ""); err != nil {
		t.Fatalf("ci example: %v", err)
	}
}
