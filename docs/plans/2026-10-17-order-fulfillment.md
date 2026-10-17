# 订单履约中台 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在一个 Go module 里做出订单、库存、支付、通知四个进程，用 MySQL、Redis 和 Kafka 走完下单、预占、支付、完成或关闭。

**Architecture:** 四个进程各自独占一个 MySQL 库，通过事务 outbox 把领域事件送到 Kafka。订单服务决定订单是否完成；库存服务只在收到 `order.completed` 后确认预占。领域状态放在不依赖 Gin、GORM 和 Kafka 的 `app` 包里，基础设施只在 `cmd` 和 `store` 适配。

**Tech Stack:** Go、Gin、GORM、MySQL 8、go-redis、segmentio/kafka-go、Viper、Zap、golang-migrate、google/uuid。

设计依据：`docs/plans/2026-10-17-order-fulfillment-design.md`。

---

## 约定

- 模块路径是 `order-fulfillment`。
- 测试命令在仓库根目录执行。
- 每完成一个任务提交一次。提交信息使用下面任务里的原文。
- 单元测试不连接 Docker。MySQL、Redis、Kafka 只在任务 14 的端到端测试里使用。
- 平台包可以被四个服务调用。服务包之间不能互相 import。

---

### Task 1: 初始化模块并加载配置

**Files:**

- Create: `go.mod`
- Create: `.gitignore`
- Create: `internal/platform/configx/config.go`
- Test: `internal/platform/configx/config_test.go`

**Step 1: 写失败测试**

创建 `internal/platform/configx/config_test.go`：

```go
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
```

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/platform/configx -count=1`

Expected: FAIL，`Load` 未定义。若还没有 `go.mod`，先执行 `go mod init order-fulfillment`，再跑一次，仍然因为缺少 `Load` 失败。

**Step 3: 写最小实现**

`.gitignore`：

```gitignore
/bin/
*.exe
*.test
```

`internal/platform/configx/config.go`：

```go
package configx

import (
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	HTTP struct {
		Addr   string `mapstructure:"addr"`
		APIKey string `mapstructure:"api_key"`
	} `mapstructure:"http"`
	MySQL struct {
		DSN string `mapstructure:"dsn"`
	} `mapstructure:"mysql"`
	Redis struct {
		Addr string `mapstructure:"addr"`
	} `mapstructure:"redis"`
	Kafka struct {
		Brokers       []string `mapstructure:"brokers"`
		GroupID       string   `mapstructure:"group_id"`
		PublishTopic  string   `mapstructure:"publish_topic"`
		ConsumeTopics []string `mapstructure:"consume_topics"`
	} `mapstructure:"kafka"`
}

func Load(path, envPrefix string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	if err := v.ReadInConfig(); err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
```

执行 `go get github.com/spf13/viper`。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/platform/configx -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add go.mod go.sum .gitignore internal/platform/configx
git commit -m "feat: load service config from yaml and env"
```

---

### Task 2: 事件信封

**Files:**

- Create: `internal/platform/eventx/envelope.go`
- Test: `internal/platform/eventx/envelope_test.go`

**Step 1: 写失败测试**

```go
package eventx

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEnvelopeJSONUsesDesignFields(t *testing.T) {
	occurred := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	env, err := New("order.created", "ord-1", []byte(`{"order_id":"ord-1"}`), occurred)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"event_id", "event_type", "aggregate_id", "occurred_at", "payload"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing %s in %s", key, raw)
		}
	}
	if got["event_type"] != "order.created" || got["aggregate_id"] != "ord-1" {
		t.Fatalf("identity = %s", raw)
	}
	payload, ok := got["payload"].(map[string]any)
	if !ok || payload["order_id"] != "ord-1" {
		t.Fatalf("payload = %#v", got["payload"])
	}
}

func TestNewRejectsInvalidPayload(t *testing.T) {
	_, err := New("order.created", "ord-1", []byte(`not-json`), time.Now().UTC())
	if err == nil {
		t.Fatal("expected error")
	}
}
```

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/platform/eventx -count=1`

Expected: FAIL，`New` 未定义。

**Step 3: 写最小实现**

```go
package eventx

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Envelope struct {
	EventID     string          `json:"event_id"`
	EventType   string          `json:"event_type"`
	AggregateID string          `json:"aggregate_id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Payload     json.RawMessage `json:"payload"`
}

func New(eventType, aggregateID string, payload []byte, occurredAt time.Time) (Envelope, error) {
	if !json.Valid(payload) {
		return Envelope{}, json.Unmarshal(payload, &struct{}{})
	}
	return Envelope{
		EventID:     uuid.NewString(),
		EventType:   eventType,
		AggregateID: aggregateID,
		OccurredAt:  occurredAt.UTC(),
		Payload:     json.RawMessage(payload),
	}, nil
}
```

执行 `go get github.com/google/uuid`。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/platform/eventx -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/platform/eventx go.mod go.sum
git commit -m "feat: add domain event envelope"
```

---

### Task 3: 健康检查与 API Key

**Files:**

- Create: `internal/platform/httpserver/router.go`
- Test: `internal/platform/httpserver/router_test.go`

**Step 1: 写失败测试**

```go
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHealthDoesNotNeedAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := New(Options{APIKey: "k"})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestMissingAPIKeyIsUnauthorized(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := New(Options{APIKey: "k"})
	r.POST("/api/v1/probe", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probe", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestAPIKeyAllowsRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := New(Options{APIKey: "k"})
	r.POST("/api/v1/probe", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/probe", nil)
	req.Header.Set("X-Api-Key", "k")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}
```

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/platform/httpserver -count=1`

Expected: FAIL，`New` 未定义。

**Step 3: 写最小实现**

```go
package httpserver

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type Options struct {
	APIKey string
}

func New(opts Options) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.Use(func(c *gin.Context) {
		if c.Request.URL.Path == "/health" {
			c.Next()
			return
		}
		if c.GetHeader("X-Api-Key") != opts.APIKey || opts.APIKey == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "unauthorized",
				"message": "missing or invalid api key",
			})
			return
		}
		c.Next()
	})
	return r
}
```

执行 `go get github.com/gin-gonic/gin`。

中间件注册在 `/health` 之后，所以健康检查不会被密钥拦住。业务路由在 `New` 返回后注册，会经过这个中间件。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/platform/httpserver -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/platform/httpserver go.mod go.sum
git commit -m "feat: add health route and api key middleware"
```

---

### Task 4: Outbox relay

**Files:**

- Create: `internal/platform/outbox/relay.go`
- Test: `internal/platform/outbox/relay_test.go`

**Step 1: 写失败测试**

```go
package outbox

import (
	"context"
	"errors"
	"testing"
)

type memStore struct {
	rows      []Record
	published []int64
}

func (m *memStore) LockUnpublished(context.Context, int) ([]Record, error) {
	out := make([]Record, 0)
	published := map[int64]bool{}
	for _, id := range m.published {
		published[id] = true
	}
	for _, row := range m.rows {
		if !published[row.ID] {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memStore) MarkPublished(_ context.Context, id int64) error {
	m.published = append(m.published, id)
	return nil
}

type scriptPublisher struct {
	failOn int
	calls  int
}

func (p *scriptPublisher) Publish(context.Context, string, string, []byte) error {
	p.calls++
	if p.calls == p.failOn {
		return errors.New("kafka down")
	}
	return nil
}

func TestRelayMarksOnlyPublishedRows(t *testing.T) {
	store := &memStore{rows: []Record{
		{ID: 1, EventID: "e1", Topic: "order.events", AggregateID: "o1", Payload: []byte(`{}`)},
		{ID: 2, EventID: "e2", Topic: "order.events", AggregateID: "o1", Payload: []byte(`{}`)},
	}}
	pub := &scriptPublisher{failOn: 2}
	err := RelayOnce(context.Background(), store, pub, 10)
	if err == nil {
		t.Fatal("expected publish error")
	}
	if len(store.published) != 1 || store.published[0] != 1 {
		t.Fatalf("published = %v", store.published)
	}
}
```

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/platform/outbox -count=1`

Expected: FAIL，`RelayOnce` 未定义。

**Step 3: 写最小实现**

```go
package outbox

import "context"

type Record struct {
	ID          int64
	EventID     string
	Topic       string
	AggregateID string
	Payload     []byte
}

type Store interface {
	LockUnpublished(ctx context.Context, limit int) ([]Record, error)
	MarkPublished(ctx context.Context, id int64) error
}

type Publisher interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

func RelayOnce(ctx context.Context, store Store, pub Publisher, limit int) error {
	rows, err := store.LockUnpublished(ctx, limit)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := pub.Publish(ctx, row.Topic, row.AggregateID, row.Payload); err != nil {
			return err
		}
		if err := store.MarkPublished(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}
```

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/platform/outbox -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/platform/outbox
git commit -m "feat: relay unpublished outbox rows in order"
```

---

### Task 5: 订单状态机

**Files:**

- Create: `internal/order/app/state.go`
- Test: `internal/order/app/state_test.go`

**Step 1: 写失败测试**

覆盖这些迁移：

- `PENDING` + reserved → `RESERVED`
- `PENDING` + rejected → `CLOSED` / `OUT_OF_STOCK`
- `RESERVED` + paid → `COMPLETED`
- `RESERVED` + payment failed → `CLOSED` / `PAYMENT_FAILED`
- `PENDING` 或 `RESERVED` + cancel → `CLOSED` / `CANCELLED`
- `COMPLETED` + cancel 返回冲突
- `CLOSED` + paid 保持 `CLOSED`，并且 `Changed() == false`

```go
package app

import "testing"

func TestPaymentMovesReservedOrderToCompleted(t *testing.T) {
	o := Order{Status: StatusPending}.MustReserve()
	next, err := o.ApplyPaymentSucceeded()
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != StatusCompleted || !next.Changed() {
		t.Fatalf("order = %+v", next)
	}
}

func TestCancelCompletedOrderConflicts(t *testing.T) {
	o := Order{Status: StatusCompleted}
	_, err := o.Cancel()
	if err != ErrConflict {
		t.Fatalf("err = %v", err)
	}
}

func TestLatePaymentDoesNotReopenClosedOrder(t *testing.T) {
	o := Order{Status: StatusClosed, CloseReason: ReasonCancelled}
	next, err := o.ApplyPaymentSucceeded()
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != StatusClosed || next.Changed() {
		t.Fatalf("order = %+v", next)
	}
}
```

`MustReserve` 只在测试里使用：调用 `ApplyReserved`，失败则 `t` 不可用，所以把它写成测试文件里的辅助函数，不要放进生产代码。生产测试直接写两步。把上面的 `MustReserve` 换成测试内联：

```go
reserved, err := (Order{Status: StatusPending}).ApplyReserved()
if err != nil {
	t.Fatal(err)
}
```

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/order/app -count=1`

Expected: FAIL，类型未定义。

**Step 3: 写最小实现**

```go
package app

import "errors"

type Status string

const (
	StatusPending   Status = "PENDING"
	StatusReserved  Status = "RESERVED"
	StatusCompleted Status = "COMPLETED"
	StatusClosed    Status = "CLOSED"
)

type CloseReason string

const (
	ReasonNone          CloseReason = ""
	ReasonOutOfStock    CloseReason = "OUT_OF_STOCK"
	ReasonPaymentFailed CloseReason = "PAYMENT_FAILED"
	ReasonCancelled     CloseReason = "CANCELLED"
)

var (
	ErrConflict           = errors.New("conflict")
	ErrInvalidTransition  = errors.New("invalid transition")
)

type Order struct {
	Status      Status
	CloseReason CloseReason
	changed     bool
}

func (o Order) Changed() bool { return o.changed }

func (o Order) ApplyReserved() (Order, error) {
	if o.Status != StatusPending {
		return o, nil
	}
	o.Status = StatusReserved
	o.changed = true
	return o, nil
}

func (o Order) ApplyRejected() (Order, error) {
	if o.Status != StatusPending {
		return o, nil
	}
	o.Status = StatusClosed
	o.CloseReason = ReasonOutOfStock
	o.changed = true
	return o, nil
}

func (o Order) ApplyPaymentSucceeded() (Order, error) {
	if o.Status != StatusReserved {
		return o, nil
	}
	o.Status = StatusCompleted
	o.changed = true
	return o, nil
}

func (o Order) ApplyPaymentFailed() (Order, error) {
	if o.Status != StatusReserved {
		return o, nil
	}
	o.Status = StatusClosed
	o.CloseReason = ReasonPaymentFailed
	o.changed = true
	return o, nil
}

func (o Order) Cancel() (Order, error) {
	if o.Status == StatusCompleted || o.Status == StatusClosed {
		return o, ErrConflict
	}
	if o.Status != StatusPending && o.Status != StatusReserved {
		return o, ErrInvalidTransition
	}
	o.Status = StatusClosed
	o.CloseReason = ReasonCancelled
	o.changed = true
	return o, nil
}
```

迟到事件返回原订单和 `nil` 错误。调用方看到 `Changed() == false` 时不写 outbox。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/order/app -count=1`

Expected: PASS。把 Step 1 里列出但没写出的迁移补成表驱动测试，全部通过后再提交。

**Step 5: 提交**

```powershell
git add internal/order/app
git commit -m "feat: add order state transitions"
```

---

### Task 6: 下单用例与幂等

**Files:**

- Create: `internal/order/app/service.go`
- Test: `internal/order/app/service_test.go`

**Step 1: 写失败测试**

用内存假件断言：

- 合法命令会保存 `PENDING` 订单、明细，并产生一条 `order.created`。
- 同一个幂等键第二次不新增订单，返回第一次的 ID。
- 空明细、数量小于 1、空 `user_id` 返回校验错误。

```go
func TestCreateIsIdempotent(t *testing.T) {
	svc := NewService(&memOrders{}, &memIdem{})
	cmd := CreateCommand{
		IdempotencyKey: "same-key-1",
		UserID:         "u1",
		Items:          []Item{{SKUID: "sku-1", Quantity: 1}},
	}
	first, err := svc.Create(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Create(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Created != true || second.Created != false {
		t.Fatalf("first %+v second %+v", first, second)
	}
}
```

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/order/app -count=1 -run TestCreateIsIdempotent`

Expected: FAIL，`NewService` 未定义。

**Step 3: 写最小实现**

定义接口，不引入 Redis：

```go
type Item struct {
	SKUID    string
	Quantity int
}

type CreateCommand struct {
	IdempotencyKey string
	UserID         string
	Items          []Item
}

type CreateResult struct {
	ID      string
	Status  Status
	Created bool
	Items   []Item
}

type OrderRecord struct {
	ID          string
	UserID      string
	Status      Status
	CloseReason CloseReason
	Items       []Item
}

type SavedEvent struct {
	EventType   string
	AggregateID string
	Payload     []byte
}

type OrderStore interface {
	InsertPending(ctx context.Context, rec OrderRecord, event SavedEvent) error
	Find(ctx context.Context, id string) (OrderRecord, error)
}

type IdempotencyStore interface {
	PutIfAbsent(ctx context.Context, key, orderID string) (current string, created bool, err error)
}
```

`Create` 的行为：

1. 校验键长度 8 到 128，`user_id` 和明细合法。失败返回 `ErrBadRequest`。
2. 生成 UUID。调用 `PutIfAbsent`。
3. `created == false` 时 `Find` 已有订单，返回 `Created: false`。
4. `created == true` 时插入 `PENDING` 订单，并保存 `order.created` 负载 `{"order_id","user_id","items":[{"sku_id","quantity"}]}`。

假件放在 `service_test.go`。`PutIfAbsent` 用 map。第一次写入并返回 created，第二次返回已有值。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/order/app -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/order/app
git commit -m "feat: create orders with an idempotency key"
```

---

### Task 7: 订单查询、取消和 HTTP

**Files:**

- Modify: `internal/order/app/service.go`
- Test: `internal/order/app/service_test.go`
- Create: `internal/order/handler/handler.go`
- Test: `internal/order/handler/handler_test.go`

**Step 1: 写应用层失败测试**

- `Get` 返回订单和明细。未知 ID 返回 `ErrNotFound`。
- `Cancel` 对 `RESERVED` 订单写成 `CLOSED` / `CANCELLED`，并要求 store 记录 `order.cancelled`。
- `Cancel` 对 `COMPLETED` 返回 `ErrConflict`，store 不新增事件。

Store 增加：

```go
Update(ctx context.Context, rec OrderRecord, event *SavedEvent) error
```

`event` 为 nil 表示不写 outbox。取消负载：

```json
{"order_id":"...","reason":"CANCELLED"}
```

**Step 2: 跑应用测试，确认失败**

Run: `go test ./internal/order/app -count=1 -run "TestGet|TestCancel"`

Expected: FAIL

**Step 3: 实现应用方法并补 HTTP 测试**

HTTP 用 httptest 和 handler 依赖的接口，不启动真实服务：

- `POST /api/v1/orders` 缺 `Idempotency-Key` 返回 `400`，正文 `error=bad_request`。
- 创建成功返回 `201` 和 `pending` 状态字段 `status`。
- 重复键返回 `200` 和同一个 `id`。
- 未知订单 `GET` 返回 `404`。
- 已完成订单取消返回 `409`，正文 `error=conflict`。

路由挂到 Task 3 的 `httpserver.New` 上。请求带 `X-Api-Key`。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/order/... -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/order
git commit -m "feat: query and cancel orders over http"
```

---

### Task 8: 订单消费库存和支付事件

**Files:**

- Create: `internal/order/app/consume.go`
- Test: `internal/order/app/consume_test.go`

**Step 1: 写失败测试**

`ApplyEvent(ctx, eventx.Envelope)`：

- `inventory.reserved` 把 `PENDING` 改为 `RESERVED`，不写 outbox。
- `inventory.rejected` 关闭订单，原因 `OUT_OF_STOCK`，不写 outbox。
- `payment.succeeded` 把 `RESERVED` 改为 `COMPLETED`，写 `order.completed`，负载只有 `order_id`。
- `payment.succeeded` 遇到 `CLOSED` 不写 outbox。
- `payment.failed` 关闭订单，原因 `PAYMENT_FAILED`。
- 同一 `event_id` 第二次调用不再改状态。Store 需要 `AlreadyProcessed` 和 `MarkProcessed`，二者与 `Update` 由假件模拟成一个成功路径。假件用 map 记录 event_id。

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/order/app -count=1 -run TestApply`

Expected: FAIL

**Step 3: 写最小实现**

解析 payload 的 `order_id`。按 `event_type` 调用 Task 5 的方法。只有 `Changed()` 时调用 `Update`。`payment.succeeded` 且变为 `COMPLETED` 时传入 `order.completed` 事件。处理成功后标记 `event_id`。重复 `event_id` 直接返回。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/order/app -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/order/app
git commit -m "feat: advance orders from inventory and payment events"
```

---

### Task 9: 库存预占纯逻辑

**Files:**

- Create: `internal/inventory/app/state.go`
- Create: `internal/inventory/app/service.go`
- Test: `internal/inventory/app/service_test.go`

**Step 1: 写失败测试**

内存 SKU：`sku-1=2`，`sku-2=5`。

- 预占 `sku-1` 数量 2：成功，可售变 0，预占状态 `RESERVED`，产生 `inventory.reserved`。
- 同一订单再预占：不二次扣减，返回已有预占。
- `sku-1` 数量 3：返回拒绝，可售仍是 2，产生 `inventory.rejected`，原因 `OUT_OF_STOCK`。
- 两行里第二行不足：两行可售都不变。
- `Confirm` 只把 `RESERVED` 变成 `CONFIRMED` 并发 `inventory.confirmed`。
- `Release` 只把 `RESERVED` 变成 `RELEASED`，可售加回，并发 `inventory.released`。
- 对 `CONFIRMED` 调 `Release` 是空操作。

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/inventory/app -count=1`

Expected: FAIL

**Step 3: 写最小实现**

`Reserve` 按 `sku_id` 排序后检查全部分够，再一次性扣减。未知 SKU 与库存不足都拒绝，原因都是 `OUT_OF_STOCK`。

接口：

```go
type StockStore interface {
	Reserve(ctx context.Context, orderID string, items []Item, reservedEvent, rejectedEvent Event) (Reservation, error)
	FindByOrder(ctx context.Context, orderID string) (Reservation, bool, error)
	Confirm(ctx context.Context, orderID string, event Event) (Reservation, bool, error)
	Release(ctx context.Context, orderID string, event Event) (Reservation, bool, error)
}
```

假件在测试文件里实现条件更新。`bool` 表示这次调用是否发生了状态变化。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/inventory/app -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/inventory/app
git commit -m "feat: reserve confirm and release stock"
```

---

### Task 10: 库存 HTTP 与事件处理

**Files:**

- Create: `internal/inventory/handler/handler.go`
- Test: `internal/inventory/handler/handler_test.go`
- Create: `internal/inventory/app/consume.go`
- Test: `internal/inventory/app/consume_test.go`

**Step 1: 写失败测试**

HTTP：

- `POST /api/v1/skus` 接受 `sku_id` 和 `available`。`available < 0` 返回 `400`。
- 成功后 `GET /api/v1/skus/:id` 返回刚写入的数量。
- 未知 SKU 返回 `404`。

消费：

- `order.created` 调用预占。
- `order.completed` 调用确认。
- `order.cancelled` 和 `payment.failed` 调用释放。
- `payment.succeeded` 被忽略，不调用确认。
- 重复 `event_id` 不二次扣减。

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/inventory/... -count=1`

Expected: FAIL

**Step 3: 写最小实现**

SKU 读写接口先用内存实现放在 `internal/inventory/app/catalog.go`，HTTP handler 依赖它。Redis 和 MySQL 留到 Task 13 的适配器，方法签名现在就定成：

```go
type Catalog interface {
	Put(ctx context.Context, skuID string, available int) error
	Get(ctx context.Context, skuID string) (int, error)
}
```

`available < 0` 返回 `ErrBadRequest`。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/inventory/... -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/inventory
git commit -m "feat: expose skus and consume fulfillment events"
```

---

### Task 11: 支付单与回调

**Files:**

- Create: `internal/payment/app/service.go`
- Test: `internal/payment/app/service_test.go`
- Create: `internal/payment/handler/handler.go`
- Test: `internal/payment/handler/handler_test.go`

**Step 1: 写失败测试**

- 消费 `inventory.reserved` 创建 `PENDING` 支付单。同一订单第二次不新增。
- `GET` 依赖的 `FindByOrder` 能找到它。
- 回调 `success` 变为 `SUCCEEDED`，事件 `payment.succeeded`，负载含 `order_id` 和 `payment_id`。
- 再回调 `success` 不产生第二件事件。
- 再回调 `fail` 返回 `ErrConflict`。
- 回调 `fail` 从 `PENDING` 变为 `FAILED`，事件 `payment.failed`。
- `result` 不是 `success` 或 `fail` 时 HTTP 返回 `400`。
- 未知支付单回调返回 `404`。

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/payment/... -count=1`

Expected: FAIL

**Step 3: 写最小实现**

状态只允许 `PENDING`、`SUCCEEDED`、`FAILED`。回调先看数据库状态，再决定是否写事件。HTTP 用 Task 3 的路由器和 `X-Api-Key`。

`GET /api/v1/payments?order_id=` 缺少查询参数时返回 `400`。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/payment/... -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/payment
git commit -m "feat: open payments from reservations and accept callbacks"
```

---

### Task 12: 通知记录与查询

**Files:**

- Create: `internal/notification/app/service.go`
- Test: `internal/notification/app/service_test.go`
- Create: `internal/notification/handler/handler.go`
- Test: `internal/notification/handler/handler_test.go`

**Step 1: 写失败测试**

- 记录一件 `order.created` 后，按 `order_id` 可以查到 `event_type`。
- 同一 `event_id` 再次记录不增加行数。
- 负载没有 `order_id` 时返回错误，不写行。
- `GET /api/v1/deliveries?order_id=ord-1` 返回数组。没有记录时是 `[]`，状态 `200`。
- 缺少 `order_id` 返回 `400`。

**Step 2: 跑测试，确认失败**

Run: `go test ./internal/notification/... -count=1`

Expected: FAIL

**Step 3: 写最小实现**

```go
type Delivery struct {
	EventID   string
	OrderID   string
	EventType string
	Payload   []byte
}
```

列表按记录顺序返回。假件用切片保存。

**Step 4: 跑测试，确认通过**

Run: `go test ./internal/notification/... -count=1`

Expected: PASS

**Step 5: 提交**

```powershell
git add internal/notification
git commit -m "feat: record and query notification deliveries"
```

---

### Task 13: 迁移、适配器和四个进程

**Files:**

- Create: `migrations/order/000001_init.up.sql`
- Create: `migrations/order/000001_init.down.sql`
- Create: `migrations/inventory/000001_init.up.sql`
- Create: `migrations/inventory/000001_init.down.sql`
- Create: `migrations/payment/000001_init.up.sql`
- Create: `migrations/payment/000001_init.down.sql`
- Create: `migrations/notification/000001_init.up.sql`
- Create: `migrations/notification/000001_init.down.sql`
- Create: `internal/platform/mysqlx/migrate.go`
- Create: `internal/order/store/gorm.go`
- Create: `internal/inventory/store/gorm.go`
- Create: `internal/payment/store/gorm.go`
- Create: `internal/notification/store/gorm.go`
- Create: `internal/platform/kafkax/kafka.go`
- Create: `cmd/order/main.go`
- Create: `cmd/inventory/main.go`
- Create: `cmd/payment/main.go`
- Create: `cmd/notification/main.go`
- Create: `config/order.yaml`
- Create: `config/inventory.yaml`
- Create: `config/payment.yaml`
- Create: `config/notification.yaml`
- Create: `deploy/mysql/init.sql`
- Create: `deploy/docker-compose.yml`

**Step 1: 写迁移 SQL**

订单库 `migrations/order/000001_init.up.sql`：

```sql
CREATE TABLE orders (
  id CHAR(36) PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  status VARCHAR(32) NOT NULL,
  close_reason VARCHAR(32) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL
);

CREATE TABLE order_items (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  order_id CHAR(36) NOT NULL,
  sku_id VARCHAR(64) NOT NULL,
  quantity INT NOT NULL,
  KEY idx_order_items_order_id (order_id)
);

CREATE TABLE outbox (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  event_id CHAR(36) NOT NULL,
  topic VARCHAR(128) NOT NULL,
  event_type VARCHAR(64) NOT NULL,
  aggregate_id VARCHAR(64) NOT NULL,
  payload JSON NOT NULL,
  occurred_at DATETIME(3) NOT NULL,
  published_at DATETIME(3) NULL,
  UNIQUE KEY uk_outbox_event_id (event_id),
  KEY idx_outbox_unpublished (published_at, id)
);

CREATE TABLE processed_events (
  event_id CHAR(36) PRIMARY KEY,
  processed_at DATETIME(3) NOT NULL
);

CREATE TABLE dead_letters (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  event_id CHAR(36) NOT NULL,
  topic VARCHAR(128) NOT NULL,
  payload JSON NOT NULL,
  error_message VARCHAR(1024) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  UNIQUE KEY uk_dead_event (event_id)
);
```

`down.sql` 按相反顺序 `DROP TABLE`。

库存库在同样的 `outbox`、`processed_events`、`dead_letters` 之外增加：

```sql
CREATE TABLE skus (
  id VARCHAR(64) PRIMARY KEY,
  available INT NOT NULL,
  updated_at DATETIME(3) NOT NULL
);

CREATE TABLE reservations (
  id CHAR(36) PRIMARY KEY,
  order_id CHAR(36) NOT NULL,
  status VARCHAR(32) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  UNIQUE KEY uk_reservation_order (order_id)
);

CREATE TABLE reservation_items (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  reservation_id CHAR(36) NOT NULL,
  sku_id VARCHAR(64) NOT NULL,
  quantity INT NOT NULL
);
```

支付库增加：

```sql
CREATE TABLE payments (
  id CHAR(36) PRIMARY KEY,
  order_id CHAR(36) NOT NULL,
  status VARCHAR(32) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  UNIQUE KEY uk_payment_order (order_id)
);
```

通知库增加：

```sql
CREATE TABLE deliveries (
  id BIGINT PRIMARY KEY AUTO_INCREMENT,
  event_id CHAR(36) NOT NULL,
  order_id CHAR(36) NOT NULL,
  event_type VARCHAR(64) NOT NULL,
  payload JSON NOT NULL,
  created_at DATETIME(3) NOT NULL,
  UNIQUE KEY uk_delivery_event (event_id),
  KEY idx_deliveries_order_id (order_id, id)
);
```

通知服务不发布事件，可以不建 `outbox`。

`deploy/mysql/init.sql` 只创建四个空库：

```sql
CREATE DATABASE IF NOT EXISTS order_db;
CREATE DATABASE IF NOT EXISTS inventory_db;
CREATE DATABASE IF NOT EXISTS payment_db;
CREATE DATABASE IF NOT EXISTS notification_db;
```

**Step 2: 实现适配器**

GORM store 实现前面任务定义的接口。扣库存使用一条条件更新，受影响行数不是 1 就视为库存不足并回滚整单。预占确认和释放使用 `WHERE status = 'RESERVED'`。已处理事件插入与业务更新共用一个事务，主键冲突时返回“已处理”，调用方不把它当成失败。

Redis 适配：

- 订单幂等键 `idempo:order:{key}`，`SetNX`，TTL 24 小时。
- 订单缓存 `cache:order:{id}`，TTL 30 秒。状态变化后删除。
- 支付回调键 `idempo:payment:{id}`，`SetNX`，TTL 24 小时。
- SKU 锁 `lock:sku:{id}`，`SetNX`，过期 5 秒。创建 SKU 和 MySQL 扣减成功后，把 `stock:{id}` 设为新的 available。

Kafka 适配实现 `outbox.Publisher`。消费者使用 `kafka.Reader`，group id 来自配置。处理返回错误时按 200ms、1s、3s 重试，仍失败则写 `dead_letters` 并继续读下一条。

`mysqlx.Migrate(dsn, sourceURL)` 调用 golang-migrate。MySQL 驱动 URL 使用 `mysql://`，并带上 `multiStatements=true`。`ErrNoChange` 视为成功。

四个 `main` 的启动顺序：加载配置、打开 MySQL、迁移、打开 Redis、构造 store 和 app、启动 relay 循环、启动消费者、最后 `http.Server` 监听。收到 `SIGINT` 或 `SIGTERM` 时取消 context，给 HTTP 5 秒关闭时间。

relay 循环每 500ms 调用一次 `RelayOnce`，单次批量 50 条。

配置文件使用 localhost，方便宿主机直接跑进程。Compose 用环境变量覆盖 DSN、Redis 和 Kafka broker。四个端口是 8081、8082、8083、8084。开发用 API Key 写在 YAML 里，值为 `dev-key`。

`deploy/docker-compose.yml` 包含 MySQL 8、Redis 7、Kafka KRaft 单节点，以及四个用同一个镜像或多阶段构建跑起来的 Go 服务。Kafka 的副本数相关参数都设为 1。`KAFKA_ADVERTISED_LISTENERS` 在 Compose 网络内使用 `kafka:9092`。

**Step 3: 编译**

Run: `go test ./... -count=1`

Expected: PASS。这一步还不连接 Docker。

再执行：

Run: `go build -o bin/order.exe ./cmd/order; go build -o bin/inventory.exe ./cmd/inventory; go build -o bin/payment.exe ./cmd/payment; go build -o bin/notification.exe ./cmd/notification`

Expected: 四个可执行文件生成，退出码 0。

**Step 4: 提交**

```powershell
git add migrations internal cmd config deploy go.mod go.sum
git commit -m "feat: wire four services to mysql redis and kafka"
```

---

### Task 14: 端到端四条链路

**Files:**

- Create: `tests/e2e/flow_test.go`

**Step 1: 写端到端测试**

文件首行是 `//go:build e2e`。测试假设 Compose 已启动，四个服务已完成迁移。HTTP 客户端给四个端口都加 `X-Api-Key: dev-key`。轮询间隔 200ms，单条链路最长等待 20 秒。

四条测试：

1. `POST /api/v1/skus` 创建 `sku-e2e` 数量 10。下单数量 1。等到订单 `RESERVED`，按 `order_id` 查询支付单，回调 `success`。等到订单 `COMPLETED`。库存 `GET` 返回 9。通知查询至少有一条该订单记录。
2. 创建 `sku-empty` 数量 0 并下单。订单最终 `CLOSED`，`close_reason` 是 `OUT_OF_STOCK`。
3. 同一个 `Idempotency-Key` 和同一份正文提交两次，两个响应的 `id` 相同。
4. 创建有库存的 SKU 并下单，等到 `RESERVED` 后取消。订单 `close_reason` 是 `CANCELLED`。再等到库存回到下单前的数量。

JSON 字段使用设计文档里的名字：`status`、`close_reason`、`id`、`available`。

**Step 2: 先确认没有标签时不会跑这组测试**

Run: `go test ./... -count=1`

Expected: PASS，且输出不包含 `tests/e2e` 的测试函数。

**Step 3: 启动依赖并跑端到端**

Run: `docker compose -f deploy/docker-compose.yml up -d --build`

等待四个 `/health` 返回 200 后执行：

Run: `go test -tags=e2e ./tests/e2e -count=1 -timeout 180s`

Expected: PASS，四条测试都通过。

**Step 4: 提交**

```powershell
git add tests/e2e
git commit -m "test: cover the four fulfillment flows end to end"
```

---

### Task 15: 运行说明

**Files:**

- Create: `README.md`

**Step 1: 写 README**

用中文写清：

- 四个服务分别负责什么。
- `docker compose -f deploy/docker-compose.yml up -d --build` 如何启动。
- 宿主机端口 8081 到 8084，请求头 `X-Api-Key: dev-key`。
- 一条成功链路的 `curl` 顺序：建 SKU、下单、查订单、按 `order_id` 查支付单、回调、再查订单和通知。
- `go test ./...` 与 `go test -tags=e2e ./tests/e2e` 的区别。

不写第一期明确不做的登录、指标和网关。

**Step 2: 核对命令和文档一致**

对照 `config/*.yaml`、`deploy/docker-compose.yml` 和测试里的路径、端口、字段名。发现不一致时改 README 或改实现，让 README 描述实际命令。

**Step 3: 再跑一次单元测试**

Run: `go test ./... -count=1`

Expected: PASS

**Step 4: 提交**

```powershell
git add README.md
git commit -m "docs: explain how to run the fulfillment stack"
```

---

## 完成标准

- `go test ./...` 通过。
- Compose 启动后，`go test -tags=e2e ./tests/e2e -count=1 -timeout 180s` 的四条链路通过。
- 订单完成只来自 `RESERVED` 上的支付成功，并伴随 `order.completed`。
- 库存确认只来自 `order.completed`。`payment.succeeded` 不会直接确认库存。
- 重复下单键、重复事件和重复的相同支付回调不会产生第二笔业务写入。
