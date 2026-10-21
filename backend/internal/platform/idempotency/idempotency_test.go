package idempotency_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/vevila2020/order-sys/internal/platform/idempotency"
)

// 这些用例覆盖不依赖数据库的输入校验。并发同键、1062 分支、事务回滚等行为
// 必须在真实 MySQL 上验证（唯一索引的等待语义无法用 fake 表达），见
// idempotency_integration_test.go。

func TestBeginRejectsIncompleteRecord(t *testing.T) {
	store := idempotency.NewStore(time.Hour)
	ctx := context.Background()
	var tx *gorm.DB // nil：校验必须先于任何数据库访问

	cases := []idempotency.Record{
		{UserID: "u1", Key: "abcdefgh", RequestHash: "h"},
		{Scope: "order.create", Key: "abcdefgh", RequestHash: "h"},
		{Scope: "order.create", UserID: "u1", RequestHash: "h"},
		{Scope: "order.create", UserID: "u1", Key: "abcdefgh"},
	}
	for i, rec := range cases {
		if _, err := store.Begin(ctx, tx, rec); err == nil {
			t.Errorf("case %d: 缺少必填字段应返回错误，而不是带着空值写唯一索引", i)
		}
	}
}

// nil tx 必须被显式拒绝：传 nil 会 panic 在 gorm 内部，错误信息指向无关的位置。
func TestBeginRejectsNilTx(t *testing.T) {
	store := idempotency.NewStore(time.Hour)
	rec := idempotency.Record{Scope: "order.create", UserID: "u1", Key: "abcdefgh", RequestHash: "hash"}
	if _, err := store.Begin(context.Background(), nil, rec); err == nil {
		t.Fatal("nil tx 应返回错误")
	}
	if err := store.Complete(context.Background(), nil, rec, []byte(`{}`)); err == nil {
		t.Fatal("nil tx 应返回错误")
	}
}

// Begin 的必填校验必须先于 nil tx 检查：否则传入不完整记录 + nil tx 时，
// 报错会指向"tx 为空"，掩盖真正的问题（调用方漏传字段）。
func TestBeginValidatesRecordBeforeTx(t *testing.T) {
	store := idempotency.NewStore(time.Hour)
	_, err := store.Begin(context.Background(), nil, idempotency.Record{Scope: "order.create"})
	if err == nil {
		t.Fatal("不完整记录应返回错误")
	}
	if !strings.Contains(err.Error(), "user_id") {
		t.Fatalf("err = %v, want 指向缺失字段", err)
	}
}

// TTL 非正在构造期就该被发现；否则 expires_at 会落在过去，记录立刻"过期"。
func TestBeginRejectsNonPositiveTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		store := idempotency.NewStore(ttl)
		rec := idempotency.Record{Scope: "order.create", UserID: "u1", Key: "abcdefgh", RequestHash: "hash"}
		if _, err := store.Begin(context.Background(), nil, rec); err == nil {
			t.Fatalf("ttl=%v 应返回错误", ttl)
		}
	}
}

// 空快照会让重放返回空 data，看起来像"订单消失了"。必须显式拒绝。
func TestCompleteRejectsEmptySnapshot(t *testing.T) {
	store := idempotency.NewStore(time.Hour)
	rec := idempotency.Record{Scope: "order.create", UserID: "u1", Key: "abcdefgh", RequestHash: "hash"}
	// 用非 nil 的 gorm.DB 会真的连库；但这里的校验发生在访问 tx 之前，
	// 传入零值句柄即可验证顺序。若顺序被改坏，本用例会因为 nil 解引用而失败。
	if err := store.Complete(context.Background(), &gorm.DB{}, rec, nil); err == nil {
		t.Fatal("空快照应返回错误")
	}
}

// 哨兵错误必须可被 errors.Is 命中：app 层靠它映射 HTTP 错误码，不能比较文案。
func TestSentinelsAreDistinct(t *testing.T) {
	all := []error{
		idempotency.ErrInvalidKey,
		idempotency.ErrInvalidRequest,
		idempotency.ErrKeyReused,
		idempotency.ErrInProgress,
	}
	for i := range all {
		for j := range all {
			if i != j && errors.Is(all[i], all[j]) {
				t.Errorf("哨兵错误 %d 与 %d 互相匹配：分支判定会失效", i, j)
			}
		}
	}
}
