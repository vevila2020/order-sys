package configx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReadsFileAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "order.yaml")
	body := []byte(`
http:
  addr: ":8081"
  api_key: "file-key"
mysql:
  dsn: "file-dsn"
redis:
  addr: "localhost:6379"
kafka:
  brokers: ["localhost:9092"]
  group_id: "order"
  publish_topic: "order.events"
  consume_topics: ["inventory.events", "payment.events"]
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORDER_HTTP_API_KEY", "env-key")

	cfg, err := Load(path, "ORDER")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != ":8081" || cfg.HTTP.APIKey != "env-key" {
		t.Fatalf("http config = %+v", cfg.HTTP)
	}
	if cfg.MySQL.DSN != "file-dsn" || cfg.Redis.Addr != "localhost:6379" {
		t.Fatalf("deps = mysql %s redis %s", cfg.MySQL.DSN, cfg.Redis.Addr)
	}
	if cfg.Kafka.PublishTopic != "order.events" || len(cfg.Kafka.ConsumeTopics) != 2 {
		t.Fatalf("kafka = %+v", cfg.Kafka)
	}
}
