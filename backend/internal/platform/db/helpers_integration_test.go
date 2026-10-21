//go:build integration

package db_test

import (
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"github.com/vevila2020/order-sys/internal/platform/configx"
	"github.com/vevila2020/order-sys/internal/platform/testdb"
)

// mysqlConfig 把 harness 的 DSN 包成所需的配置结构。
func mysqlConfig(tdb *testdb.DB) configx.MySQLConfig {
	return mysqlConfigWithDSN(tdb.DSN)
}

// mysqlConfigWithDSN 用给定 DSN 构造一份合理的池配置。
func mysqlConfigWithDSN(dsn string) configx.MySQLConfig {
	return configx.MySQLConfig{
		DSN:          dsn,
		MaxOpenConns: 5,
		MaxIdleConns: 2,
	}
}

// countSkus 返回指定 sku_id 的行数，用于断言提交/回滚结果。
func countSkus(t *testing.T, gdb *gorm.DB, skuID string) int64 {
	t.Helper()

	var n int64
	if err := gdb.Raw(`SELECT COUNT(*) FROM skus WHERE sku_id = ?`, skuID).Scan(&n).Error; err != nil {
		t.Fatalf("统计 skus: %v", err)
	}
	return n
}

// containsAny 判断 s 是否包含任一子串（不区分大小写）。
func containsAny(s string, subs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// retryableStub 是一个"看起来像 MySQL 死锁"的错误，用于在不制造真实锁竞争的
// 情况下验证重试计数、退避与取消行为。
//
// 它实现 error 与 As(target) 两部分：As 把目标填成 *mysqldriver.MySQLError，
// 这样 db.mysqlErrorCode 走的是与生产完全相同的提取路径，而不是测试专用分支。
type retryableStub struct{}

func (retryableStub) Error() string { return "stub: deadlock" }

// As 让 errors.As(err, **mysqldriver.MySQLError) 成功，错误码固定为 1213。
func (retryableStub) As(target any) bool {
	p, ok := target.(**mysqldriver.MySQLError)
	if !ok {
		return false
	}
	*p = &mysqldriver.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"}
	return true
}
