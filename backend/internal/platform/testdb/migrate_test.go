//go:build integration

package testdb

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// expectedTables 是迁移 000001 应建立的业务表。
// 与 up 文件逐张对应；新增迁移时必须同步这里，否则测试会漏掉新表。
var expectedTables = []string{
	"idempotency_records",
	"inventories",
	"order_items",
	"orders",
	"outbox_events",
	"payments",
	"reservation_items",
	"reservations",
	"skus",
}

// TestMigrationsUpDownUp 验证迁移可前向、可回滚、可重复执行（M1 退出标准）。
//
// 只跑 up 是不够的：down 的顺序错误（先删被引用表）只会在回滚时暴露，
// 而回滚恰恰是事故现场才会用到的路径，等到那时才发现就太晚了。
func TestMigrationsUpDownUp(t *testing.T) {
	ctx := context.Background()

	db, err := New(ctx, t)
	if err != nil {
		t.Fatalf("准备测试数据库失败（集成测试需要 Docker）: %v", err)
	}

	// --- up ---
	if err := db.MigrateUp(); err != nil {
		t.Fatalf("首次 up 失败: %v", err)
	}

	tables, err := db.Tables(ctx)
	if err != nil {
		t.Fatalf("读取表清单: %v", err)
	}
	if got, want := strings.Join(tables, ","), strings.Join(expectedTables, ","); got != want {
		t.Fatalf("up 后的表清单不符\n got: %v\nwant: %v", tables, expectedTables)
	}

	version, dirty, err := db.Version()
	if err != nil {
		t.Fatalf("读取迁移版本: %v", err)
	}
	if dirty {
		t.Fatalf("up 后版本为 dirty，说明迁移中途失败")
	}
	if version != 1 {
		t.Fatalf("版本 = %d, want 1", version)
	}

	// --- up 幂等：重复执行不应报错，也不应改变版本 ---
	if err := db.MigrateUp(); err != nil {
		t.Fatalf("重复 up 失败（应返回 ErrNoChange 并被忽略）: %v", err)
	}
	if v, _, err := db.Version(); err != nil || v != 1 {
		t.Fatalf("重复 up 后版本 = %d (err=%v), want 1", v, err)
	}

	// --- down ---
	if err := db.MigrateDown(); err != nil {
		t.Fatalf("down 失败: %v", err)
	}
	tables, err = db.Tables(ctx)
	if err != nil {
		t.Fatalf("读取表清单: %v", err)
	}
	if len(tables) != 0 {
		t.Fatalf("down 后仍有表残留: %v", tables)
	}

	// --- up again ---
	if err := db.MigrateUp(); err != nil {
		t.Fatalf("第二次 up 失败（迁移文件不可重复执行）: %v", err)
	}
	tables, err = db.Tables(ctx)
	if err != nil {
		t.Fatalf("读取表清单: %v", err)
	}
	if got, want := strings.Join(tables, ","), strings.Join(expectedTables, ","); got != want {
		t.Fatalf("二次 up 后的表清单不符\n got: %v\nwant: %v", tables, expectedTables)
	}
}

// TestSchemaCharsetAndCollation 断言所有业务表使用项目的字符集与排序规则。
//
// 这项检查单独存在的原因：迁移文件里每张表都写了 COLLATE，但漏写一张不会让
// 任何功能测试失败，只会在生产环境出现"同一个查询本地命中、线上不命中"。
// 这里用 information_schema 直接核对物理结果，而不是相信文件内容。
func TestSchemaCharsetAndCollation(t *testing.T) {
	ctx := context.Background()
	db := NewMigrated(ctx, t)

	rows, err := db.SQL.QueryContext(ctx, `
		SELECT table_name, table_collation
		  FROM information_schema.tables
		 WHERE table_schema = DATABASE()
		   AND table_name <> 'schema_migrations'
		 ORDER BY table_name`)
	if err != nil {
		t.Fatalf("查询字符集: %v", err)
	}
	defer func() { _ = rows.Close() }()

	seen := 0
	for rows.Next() {
		var table, collation string
		if err := rows.Scan(&table, &collation); err != nil {
			t.Fatalf("扫描: %v", err)
		}
		seen++
		if collation != "utf8mb4_unicode_ci" {
			t.Errorf("表 %s 的排序规则 = %s, want utf8mb4_unicode_ci", table, collation)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历: %v", err)
	}
	if seen != len(expectedTables) {
		t.Fatalf("检查了 %d 张表, want %d", seen, len(expectedTables))
	}
}

// TestInventoryBalanceConstraint 验证库存恒等式的 CHECK 真的会拦截非法行。
//
// CHECK 约束是"代码写错时的最后一道防线"，因此必须证明它在目标 MySQL 版本上
// 确实生效：MySQL 8.0.16 之前会解析并忽略 CHECK，只在 8.0.16+ 才强制执行。
// 如果将来有人把镜像降级，这个测试会立刻失败。
func TestInventoryBalanceConstraint(t *testing.T) {
	ctx := context.Background()
	db := NewMigrated(ctx, t)

	exec := func(query string, args ...any) error {
		_, err := db.SQL.ExecContext(ctx, query, args...)
		return err
	}

	// 合法行：1+0+0+0 = 1。
	if err := exec(`
		INSERT INTO skus (sku_id, name, price_cents, status, created_at, updated_at)
		VALUES ('sku-balance-ok', 'ok', 100, 'ACTIVE', UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))`); err != nil {
		t.Fatalf("插入合法 SKU 失败: %v", err)
	}
	if err := exec(`
		INSERT INTO inventories (sku_id, total, available, allocated, reserved, sold, updated_at)
		VALUES ('sku-balance-ok', 1, 1, 0, 0, 0, UTC_TIMESTAMP(6))`); err != nil {
		t.Fatalf("插入守恒的库存行失败: %v", err)
	}

	// 非法行：available + allocated + reserved + sold = 2 != total = 1，必须被拒绝。
	err := exec(`
		INSERT INTO inventories (sku_id, total, available, allocated, reserved, sold, updated_at)
		VALUES ('sku-balance-ok', 1, 2, 0, 0, 0, UTC_TIMESTAMP(6))`)
	if err == nil {
		t.Fatal("破坏库存恒等式的行被接受了，CHECK 约束没有生效")
	}
	// 3819 是 MySQL 的 ER_CHECK_CONSTRAINT_VIOLATED。断言错误码而不是错误文本，
	// 避免因为 MySQL 的小版本改了措辞而让测试变脆。
	if !isMySQLErrorCode(err, uint16(3819)) {
		t.Fatalf("期望 CHECK 约束错误 3819，实际: %v", err)
	}

	// 关键：被拒绝的写入不能改变已有行。
	var available uint64
	if err := db.SQL.QueryRowContext(ctx,
		`SELECT available FROM inventories WHERE sku_id = 'sku-balance-ok'`).Scan(&available); err != nil {
		t.Fatalf("读取库存: %v", err)
	}
	if available != 1 {
		t.Fatalf("available = %d, want 1", available)
	}
}

// TestUniqueConstraints 验证各表用于并发兜底的唯一键确实存在。
//
// 这些唯一键的正确性由后续仓储层的 1062 处理依赖（重放、幂等、每单一个支付单），
// 所以迁移阶段就要证明它们建出来了。
func TestUniqueConstraints(t *testing.T) {
	ctx := context.Background()
	db := NewMigrated(ctx, t)

	cases := []struct {
		table string
		index string
	}{
		{"idempotency_records", "uk_idempotency"},
		{"orders", "uk_orders_order_id"},
		{"order_items", "uk_order_items_order_sku"},
		{"reservations", "uk_reservations_order_id"},
		{"reservation_items", "uk_reservation_items_reservation_sku"},
		{"payments", "uk_payments_order_id"},
		{"outbox_events", "uk_outbox_events_event_id"},
		{"inventories", "uk_inventories_sku_id"},
		{"skus", "uk_skus_sku_id"},
	}

	for _, c := range cases {
		var nonUnique int
		err := db.SQL.QueryRowContext(ctx, `
			SELECT COUNT(*)
			  FROM information_schema.statistics
			 WHERE table_schema = DATABASE()
			   AND table_name = ?
			   AND index_name = ?
			   AND non_unique = 0`, c.table, c.index).Scan(&nonUnique)
		if err != nil {
			t.Fatalf("查询索引 %s.%s: %v", c.table, c.index, err)
		}
		if nonUnique == 0 {
			t.Errorf("表 %s 缺少唯一索引 %s", c.table, c.index)
		}
	}
}

// TestOutboxPendingIndexColumnOrder 断言 relay 取件索引的列顺序。
//
// 索引只有在列顺序与查询的 WHERE/ORDER BY 匹配时才有用。把 (status,
// available_at, id) 写成 (id, status, available_at) 不会报错，但 M3 的 relay
// 会退化成全表扫描 + 文件排序。这里把顺序钉住。
func TestOutboxPendingIndexColumnOrder(t *testing.T) {
	ctx := context.Background()
	db := NewMigrated(ctx, t)

	rows, err := db.SQL.QueryContext(ctx, `
		SELECT column_name
		  FROM information_schema.statistics
		 WHERE table_schema = DATABASE()
		   AND table_name = 'outbox_events'
		   AND index_name = 'idx_outbox_pending'
		 ORDER BY seq_in_index`)
	if err != nil {
		t.Fatalf("查询索引列: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("扫描列名: %v", err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历列名: %v", err)
	}

	want := []string{"status", "available_at", "id"}
	if !equalStrings(cols, want) {
		t.Fatalf("idx_outbox_pending 列顺序 = %v, want %v", cols, want)
	}
}

// TestColumnTypes 抽查几类容易写错的列类型。
//
// 这三类错误都不会让功能测试失败，但会在数月后变成排查困难的问题：
//   - 金额用 DECIMAL/FLOAT：浮点误差会累积到对账；
//   - 时间用 TIMESTAMP：会话时区不同导致读写偏移；
//   - 时间精度不是 6：同一秒内的多次迁移排序不稳定。
func TestColumnTypes(t *testing.T) {
	ctx := context.Background()
	db := NewMigrated(ctx, t)

	checks := []struct {
		table, column, wantType string
	}{
		{"orders", "total_cents", "bigint unsigned"},
		{"order_items", "unit_price_cents", "bigint unsigned"},
		{"payments", "amount_cents", "bigint unsigned"},
		{"orders", "created_at", "datetime(6)"},
		{"orders", "updated_at", "datetime(6)"},
		{"payments", "finished_at", "datetime(6)"},
		{"idempotency_records", "expires_at", "datetime(6)"},
		{"idempotency_records", "request_hash", "char(64)"},
		{"skus", "sku_id", "char(36)"},
	}

	for _, c := range checks {
		var got string
		err := db.SQL.QueryRowContext(ctx, `
			SELECT column_type
			  FROM information_schema.columns
			 WHERE table_schema = DATABASE()
			   AND table_name = ?
			   AND column_name = ?`, c.table, c.column).Scan(&got)
		if err == sql.ErrNoRows {
			t.Errorf("列 %s.%s 不存在", c.table, c.column)
			continue
		}
		if err != nil {
			t.Fatalf("查询列类型 %s.%s: %v", c.table, c.column, err)
		}
		if got != c.wantType {
			t.Errorf("%s.%s 类型 = %q, want %q", c.table, c.column, got, c.wantType)
		}
	}
}

// isMySQLErrorCode 判断 err 是否为指定编号的 MySQL 错误。
//
// 用 errors.As 取 *mysql.MySQLError 而不是匹配错误文本：文案会随驱动或服务端
// 版本变化，错误码是稳定的协议字段。
func isMySQLErrorCode(err error, code uint16) bool {
	var me *mysqldriver.MySQLError
	if !errors.As(err, &me) {
		return false
	}
	return me.Number == code
}

// equalStrings 比较两个字符串切片是否逐元素相等。
func equalStrings(a, b []string) bool {
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
