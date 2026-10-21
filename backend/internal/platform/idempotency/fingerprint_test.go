package idempotency_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/vevila2020/order-sys/internal/platform/idempotency"
)

// 指纹的稳定性是幂等语义的全部基础：它一旦不稳定，合法的重试会被判成
// IDEMPOTENCY_CONFLICT，或者更糟——不同的请求被判成同一个键并重放错误的结果。
func TestFingerprintIgnoresKeyOrderAndWhitespace(t *testing.T) {
	cases := [][2]string{
		{`{"sku_id":"sku-1","quantity":2}`, `{"quantity":2,"sku_id":"sku-1"}`},
		{`{"a":1,"b":{"c":2,"d":3}}`, "{\n  \"b\": { \"d\": 3, \"c\": 2 },\n  \"a\": 1\n}"},
		{`{"list":[1,2],"x":true}`, ` { "x" : true , "list" : [ 1 , 2 ] } `},
	}
	for i, pair := range cases {
		a, err := idempotency.Fingerprint([]byte(pair[0]))
		if err != nil {
			t.Fatalf("case %d 计算指纹失败: %v", i, err)
		}
		b, err := idempotency.Fingerprint([]byte(pair[1]))
		if err != nil {
			t.Fatalf("case %d 计算指纹失败: %v", i, err)
		}
		if a != b {
			t.Errorf("case %d 语义相同的请求体指纹不同: %s vs %s\n  %s\n  %s", i, a, b, pair[0], pair[1])
		}
		if len(a) != 64 {
			t.Errorf("case %d 指纹长度 = %d, want 64（CHAR(64)）", i, len(a))
		}
	}
}

// 类型不同、数值写法不同必须得到不同指纹：否则把 quantity 从数字改成字符串
// 会被当作同一次请求重放，掩盖客户端的 bug。
func TestFingerprintDistinguishesTypesAndNumberForms(t *testing.T) {
	pairs := [][2]string{
		{`{"quantity":1}`, `{"quantity":"1"}`},
		{`{"quantity":1}`, `{"quantity":1.0}`},
		{`{"quantity":1}`, `{"quantity":1e0}`},
		{`{"a":null}`, `{"a":false}`},
	}
	for i, pair := range pairs {
		a, err := idempotency.Fingerprint([]byte(pair[0]))
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		b, err := idempotency.Fingerprint([]byte(pair[1]))
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if a == b {
			t.Errorf("case %d 不同的请求体得到相同指纹: %s vs %s", i, pair[0], pair[1])
		}
	}
}

// 数组顺序有语义：同一批商品的排列顺序不同，不应视为同一次请求。
func TestFingerprintKeepsArrayOrder(t *testing.T) {
	a, err := idempotency.Fingerprint([]byte(`{"items":["a","b"]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := idempotency.Fingerprint([]byte(`{"items":["b","a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("数组顺序不同却得到相同指纹：顺序有语义（ADR-0006 第 2 节）")
	}
}

// 超过 2^53 的整数默认会被解析成 float64 而丢精度，UseNumber 是防住它的唯一手段。
func TestFingerprintPreservesLargeIntegers(t *testing.T) {
	a, err := idempotency.Fingerprint([]byte(`{"amount_cents":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	// 9007199254740992 与 ...93 在 float64 下是同一个值。
	b, err := idempotency.Fingerprint([]byte(`{"amount_cents":9007199254740992}`))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("大整数丢精度：两个不同的金额算出相同指纹")
	}
}

func TestFingerprintRejectsInvalidJSON(t *testing.T) {
	cases := []string{
		``,
		`{`,
		`{"a":1} trailing`,
		`{"a":1}{"b":2}`,
		`not json`,
	}
	for _, body := range cases {
		if _, err := idempotency.Fingerprint([]byte(body)); !errors.Is(err, idempotency.ErrInvalidRequest) {
			t.Errorf("body %q: err = %v, want ErrInvalidRequest", body, err)
		}
	}
}

// 超大请求体在解析前就被拒绝：解析会按输入规模占内存。
func TestFingerprintRejectsOversizedBody(t *testing.T) {
	body := []byte(`"` + strings.Repeat("a", idempotency.MaxBodyBytes) + `"`)
	if _, err := idempotency.Fingerprint(body); !errors.Is(err, idempotency.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestKeyValidation(t *testing.T) {
	valid := []string{
		"order-2026-10-21",
		"abc:def.ghi_jkl",
		"12345678",
	}
	for _, raw := range valid {
		got, err := idempotency.Key(raw)
		if err != nil {
			t.Errorf("key %q 应合法: %v", raw, err)
		}
		if got != raw {
			t.Errorf("key %q 被改写为 %q：键不做大小写折叠或裁剪以外的变换", raw, got)
		}
	}

	invalid := []string{
		strings.Repeat("a", 7),   // 太短
		strings.Repeat("a", 129), // 太长
		"has space",              // 空格
		"has/slash",              // 非法字符
		"中文键中文键中文键",              // 非 ASCII
		"tab\tkey",               // 控制字符
		strings.Repeat("a", 200), // 超长
	}
	for _, raw := range invalid {
		if _, err := idempotency.Key(raw); !errors.Is(err, idempotency.ErrInvalidKey) {
			t.Errorf("key %q: err = %v, want ErrInvalidKey", raw, err)
		}
	}
}

// 键允许前后空白：HTTP 头的值常被客户端库补齐，裁剪不改变语义。
func TestKeyTrimsWhitespace(t *testing.T) {
	got, err := idempotency.Key("  abcdefgh  ")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != "abcdefgh" {
		t.Fatalf("got %q, want abcdefgh", got)
	}
}

// 键区分大小写：`Abc` 与 `abc` 是两个键。若折叠大小写，两个独立请求会互相重放。
func TestKeyIsCaseSensitive(t *testing.T) {
	a, err := idempotency.Key("Abcdefgh")
	if err != nil {
		t.Fatal(err)
	}
	b, err := idempotency.Key("abcdefgh")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("幂等键不应折叠大小写")
	}
}

func TestSnapshotEncodesJSON(t *testing.T) {
	raw, err := idempotency.Snapshot(map[string]string{"order_id": "ord-1"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(raw) != `{"order_id":"ord-1"}` {
		t.Fatalf("snapshot = %s", raw)
	}
	if _, err := idempotency.Snapshot(func() {}); err == nil {
		t.Fatal("不可序列化的值应返回错误，而不是 panic")
	}
}
