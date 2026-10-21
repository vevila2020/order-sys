package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/vevila2020/order-sys/internal/platform/configx"
)

const (
	// pingTimeout 是启动自检的超时。启动期宁可快速失败并退出，也不要让进程
	// 在半初始化状态下对外提供服务。
	pingTimeout = 5 * time.Second

	// connMaxLifetime 短于 MySQL 的 wait_timeout（默认 8h），
	// 避免复用被服务端单方面关闭的连接而报 "invalid connection"。
	connMaxLifetime = 30 * time.Minute

	// connMaxIdleTime 让空闲连接主动归还，避免连接池长期持有大量闲置会话。
	connMaxIdleTime = 10 * time.Minute
)

// Open 建立受控的连接池并做启动自检。
//
// 池大小由配置决定，并强制 MaxIdle <= MaxOpen（配置校验已保证，这里再兜一次：
// 连接池配置错误会在运行期表现为"连接被立即关闭"，排查成本很高）。
//
// 不使用全局单例：*gorm.DB 由 main 构造并显式注入（需求 §3.3）。
func Open(ctx context.Context, cfg configx.MySQLConfig) (*gorm.DB, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, errors.New("mysql dsn 为空：必须通过 MYSQL_DSN 环境变量注入")
	}

	gdb, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{
		// 关闭默认事务：单条写语句自带原子性，再包一层 BEGIN/COMMIT 只是多两次
		// 往返。需要多语句原子性的场景显式使用 TransactionManager（ADR-0003）。
		SkipDefaultTransaction: true,
		// 日志交给 zap 适配后的 logger；这里先保持静默，避免 GORM 把完整 SQL
		// 打到 stdout（其中可能含参数值，属于敏感信息）。
		Logger: gormlogger.Discard,
		// 说明：这里刻意不设置 NamingStrategy。迁移文件是 schema 的唯一事实来源，
		// 表名必须由各模块的模型显式实现 TableName() 决定；再叠加一层命名策略，
		// 等于给"表名从哪来"制造第二个答案。
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库连接: %w", err)
	}

	sqlDB, err := gdb.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接池: %w", err)
	}

	// 校验配置在运行期语义正确。MaxIdle > MaxOpen 时 driver 会静默把空闲连接
	// 立即关闭，表现为"每次查询都新建连接"。
	maxOpen, maxIdle := cfg.MaxOpenConns, cfg.MaxIdleConns
	if maxIdle > maxOpen {
		return nil, fmt.Errorf("mysql.max_idle_conns (%d) 不能大于 mysql.max_open_conns (%d)", maxIdle, maxOpen)
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(connMaxLifetime)
	sqlDB.SetConnMaxIdleTime(connMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		// 错误里不含 DSN：调用方只应看到"连不上"，具体原因进日志由 main 决定。
		return nil, fmt.Errorf("数据库启动自检失败: %w", err)
	}

	return gdb, nil
}

// Close 关闭连接池，供优雅关闭时调用。
func Close(gdb *gorm.DB) error {
	if gdb == nil {
		return nil
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return fmt.Errorf("获取底层连接池: %w", err)
	}
	return sqlDB.Close()
}

// Stats 暴露连接池统计，供健康检查与指标使用。
// 返回 sql.DBStats 的副本，调用方无法通过它修改连接池。
func Stats(gdb *gorm.DB) (sql.DBStats, error) {
	if gdb == nil {
		return sql.DBStats{}, errors.New("数据库未初始化")
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return sql.DBStats{}, fmt.Errorf("获取底层连接池: %w", err)
	}
	return sqlDB.Stats(), nil
}

// Pinger 返回供健康检查注册的函数：探测数据库是否可用。
//
// 单独暴露而不是让 healthx 直接持有 *gorm.DB，是为了让 health 包不依赖 GORM
// （healthx 属于 platform，但不应把 ORM 类型扩散出去）。
func Pinger(gdb *gorm.DB, timeout time.Duration) func(context.Context) error {
	return func(ctx context.Context) error {
		if gdb == nil {
			return errors.New("数据库未初始化")
		}
		sqlDB, err := gdb.DB()
		if err != nil {
			return fmt.Errorf("获取底层连接池: %w", err)
		}
		pingCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return sqlDB.PingContext(pingCtx)
	}
}
