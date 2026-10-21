//go:build integration

package testdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	// 以副作用导入注册 "mysql" 驱动：sql.Open("mysql", ...) 依赖它。
	// 放在 testdb.go 而不是测试文件里，保证任何使用本 harness 的包都不必重复导入。
	_ "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
)

const (
	// mysqlImage 固定到补丁版本，与部署环境和 docker-compose.yml 保持一致。
	// 用 :8 这类浮动标签会让"今天通过、明天失败"变成常态，而且没人能复现。
	mysqlImage = "mysql:8.4.3"

	// charset/collation 与迁移文件、部署服务器一致。这里显式传参而不是依赖镜像
	// 默认值：镜像默认是 utf8mb4_0900_ai_ci，与项目使用的 unicode_ci 在等价字符集
	// 上不同，会让"本地通过、线上行为不同"的查询问题无法在测试中暴露。
	charset   = "utf8mb4"
	collation = "utf8mb4_unicode_ci"

	// containerStartTimeout 覆盖镜像首次拉取之外的容器启动时间。
	// 冷启动时 MySQL 初始化数据目录需要数秒；CI 上更慢。
	containerStartTimeout = 3 * time.Minute
)

// DB 是一个已迁移到最新版本、测试结束自动销毁的数据库实例。
//
// 每个 DB 拥有独立的 schema（库名），但共享同一个 MySQL 容器。
// 隔离单位是 schema 而不是容器：启动 MySQL 约需 9 秒，一个包里有十几个集成
// 测试就是两分钟；而 CREATE DATABASE + 迁移只需几十毫秒。schema 级隔离同样能
// 保证测试之间互不可见，因此是更好的取舍。
type DB struct {
	// DSN 可直接交给 gorm.Open(mysql.Open(dsn)) 使用。
	// 注意它包含 multiStatements=true：迁移需要，但生产 DSN 不应开启。
	DSN string
	// SQL 是已打开的 *sql.DB，供迁移与直接 SQL 断言使用。
	SQL *sql.DB

	// schema 是本实例独占的库名，清理时整个 DROP。
	schema string
	// admin 指向容器的连接（不指定库），用于建库与删库。
	admin *sql.DB
}

// 共享容器状态。
//
// 为什么不放到 t.Cleanup：容器应当在整个测试二进制结束时销毁一次，而不是跟着
// 某一个测试走。使用 sync.Once + 进程级清理的理由见 sharedContainer。
var (
	containerOnce sync.Once
	container     *tcmysql.MySQLContainer
	containerErr  error
	containerMu   sync.Mutex // 保护 schemaSeq
	schemaSeq     int
)

// sharedContainer 惰性启动容器，并在整个测试进程内复用。
//
// 清理挂在 TestMain 之外的策略：Go 没有"包级 Cleanup"钩子，但 sync.Once 启动的
// 容器在进程退出时会被 testcontainers 的 ryuk 回收（它在容器启动时注册了
// 进程退出即可清理的旁路容器）。因此这里不显式 Terminate，避免某个测试提前
// 关掉别人还在用的容器。若需要立即回收，测试结束时 manual 执行 docker ps 查看。
func sharedContainer(ctx context.Context) (*tcmysql.MySQLContainer, error) {
	containerOnce.Do(func() {
		// 用 root 连接：迁移需要建表与外键，普通用户权限不足。
		// 该密码只存在于本次容器生命周期内，不写入日志或磁盘。
		container, containerErr = tcmysql.Run(ctx, mysqlImage,
			tcmysql.WithDatabase("bootstrap"),
			tcmysql.WithUsername("root"),
			tcmysql.WithPassword("test-root-password"),
		)
	})
	return container, containerErr
}

// New 创建一份独立的 schema 并执行全部迁移，返回可用实例。
//
// 生命周期绑定到 t：测试结束时删除 schema 并关闭连接，容器的销毁交给进程。
// 返回错误而不是 panic，便于调用方在需要时判断"是不是 Docker 不可用"。
func New(ctx context.Context, t *testing.T) (*DB, error) {
	t.Helper()

	startCtx, cancel := context.WithTimeout(ctx, containerStartTimeout)
	defer cancel()

	ctr, err := sharedContainer(startCtx)
	if err != nil {
		return nil, fmt.Errorf("启动 mysql 容器: %w", err)
	}

	baseDSN, err := ctr.ConnectionString(startCtx,
		"parseTime=true",
		"loc=UTC",
		"charset="+charset,
		"collation="+collation,
		// 迁移文件里有多条语句，golang-migrate 的 MySQL 驱动要求驱动层允许
		// 一次下发多条语句。生产 DSN 不开这个（见 ADR 与 .env.example）。
		"multiStatements=true",
	)
	if err != nil {
		return nil, fmt.Errorf("构造 dsn: %w", err)
	}

	// 用容器默认库作为管理连接：建库/删库都要连到某个库上执行。
	admin, err := sql.Open("mysql", baseDSN)
	if err != nil {
		return nil, fmt.Errorf("打开管理连接: %w", err)
	}
	if err := admin.PingContext(startCtx); err != nil {
		_ = admin.Close()
		return nil, fmt.Errorf("管理连接不可用: %w", err)
	}

	// schema 名带自增序号与测试名：序号避免并发测试撞名，测试名让失败信息里的
	// 库名能直接指向出错的用例。库名只允许 [A-Za-z0-9_]，其余字符统一替换。
	containerMu.Lock()
	schemaSeq++
	seq := schemaSeq
	containerMu.Unlock()
	schema := fmt.Sprintf("t_%d_%s", seq, sanitizeName(t.Name()))
	// 库名会拼进 SQL，必须先确认字符集受限，避免标识符注入。
	if !validSchemaName(schema) {
		_ = admin.Close()
		return nil, fmt.Errorf("非法的测试库名: %s", schema)
	}

	if _, err := admin.ExecContext(startCtx, "CREATE DATABASE `"+schema+"`"+
		" DEFAULT CHARACTER SET "+charset+" COLLATE "+collation); err != nil {
		_ = admin.Close()
		return nil, fmt.Errorf("创建测试库 %s: %w", schema, err)
	}

	//nolint:contextcheck // 清理必须用独立 context：传入的测试 ctx 在失败/超时时已被取消，沿用它会让 DROP 直接失败
	t.Cleanup(func() {
		// 刻意使用 context.Background() 而不是 New 收到的 ctx：Cleanup 常在测试
		// ctx 已被取消（超时、t.Fatal 触发的取消）之后运行，沿用它等于放弃清理，
		// 会在容器里留下测试库。这里给出独立的一分钟预算。
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		// DROP 失败不影响测试结论，但库会留在容器里直到容器销毁。
		_, _ = admin.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS `"+schema+"`")
		_ = admin.Close()
	})

	dsn := replaceDatabase(baseDSN, schema)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	// sql.Open 是惰性的，这里显式验证连通性，避免把"库建好了但连接失败"的错误
	// 推迟到第一个查询，那时报错信息会指向业务代码而不是环境。
	if err := db.PingContext(startCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连接数据库: %w", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return &DB{DSN: dsn, SQL: db, schema: schema, admin: admin}, nil
}

// NewMigrated 是 New 的常用组合：启动容器并迁移到最新版本。
// Docker 不可用时调用 t.Fatalf，因为集成测试没有真实数据库就没有意义。
func NewMigrated(ctx context.Context, t *testing.T) *DB {
	t.Helper()

	tdb, err := New(ctx, t)
	if err != nil {
		t.Fatalf("准备测试数据库失败（集成测试需要 Docker）: %v", err)
	}
	if err := tdb.MigrateUp(); err != nil {
		t.Fatalf("执行迁移失败: %v", err)
	}
	return tdb
}

// MigrateUp 把 schema 迁移到最新版本。
func (d *DB) MigrateUp() error {
	m, conn, err := d.newMigrator()
	if err != nil {
		return err
	}
	defer closeMigrator(m, conn)

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// MigrateDown 回滚全部迁移，用于验证 down 文件与逆序依赖。
func (d *DB) MigrateDown() error {
	m, conn, err := d.newMigrator()
	if err != nil {
		return err
	}
	defer closeMigrator(m, conn)

	// Down 在已无迁移可回滚时也会返回 ErrNoChange，这里视为成功。
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}

// Version 返回当前迁移版本与 dirty 标记。
// dirty 为 true 表示上一次迁移中途失败，需要人工介入（见 docs/runbooks/migrate.md）。
func (d *DB) Version() (version uint, dirty bool, err error) {
	m, conn, err := d.newMigrator()
	if err != nil {
		return 0, false, err
	}
	defer closeMigrator(m, conn)
	return m.Version()
}

// Tables 返回当前 schema 下的全部业务表名（不含 migrate 自己的版本表）。
func (d *DB) Tables(ctx context.Context) ([]string, error) {
	rows, err := d.SQL.QueryContext(ctx, `
		SELECT table_name
		  FROM information_schema.tables
		 WHERE table_schema = DATABASE()
		   AND table_name <> 'schema_migrations'
		 ORDER BY table_name`)
	if err != nil {
		return nil, fmt.Errorf("查询表清单: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("扫描表名: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历表清单: %w", err)
	}
	return out, nil
}

// newMigrator 构造指向本实例的 migrate 句柄，并返回它自己独占的连接。
//
// 这里刻意**不复用** d.SQL。migrate 的 MySQL 驱动会把传入的 *sql.DB 当成自己的
// 财产：Close 时调用 db.Close()。若把测试用的连接交出去，第一次迁移结束就把 d.SQL
// 关掉了，后续断言会得到 "sql: database is closed"——这正是本 harness 初版踩到的
// 坑，也因此 closeMigrator 必须关闭这条独立连接。
//
// 复用 DSN 的开销可以忽略（容器端口固定），换来的是一条清晰的所有权边界。
func (d *DB) newMigrator() (*migrate.Migrate, *sql.DB, error) {
	conn, err := sql.Open("mysql", d.DSN)
	if err != nil {
		return nil, nil, fmt.Errorf("为迁移打开连接: %w", err)
	}
	// 迁移需要在同一连接上看到自己建的表，因此限制为单连接并立即验证。
	conn.SetMaxOpenConns(1)

	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("迁移连接不可用: %w", err)
	}

	driver, err := migratemysql.WithInstance(conn, &migratemysql.Config{})
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("构造 migrate 驱动: %w", err)
	}

	dir, err := migrationsDir()
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	src := "file://" + filepath.ToSlash(dir)

	m, err := migrate.NewWithDatabaseInstance(src, "mysql", driver)
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("构造 migrator: %w", err)
	}
	return m, conn, nil
}

// sanitizeName 把任意测试名转换为合法且短的库名片段。
//
// MySQL 标识符上限 64 字符，而子测试名可能很长（例如
// "TestPlaceOrder/insufficient_stock/multi_sku_rollback"）。这里保留字母数字与
// 下划线、其余替换为下划线，并截断到 40 字符，给前缀与序号留出空间。
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	if out == "" {
		out = "unnamed"
	}
	return out
}

// validSchemaName 在拼 SQL 前确认库名只含安全字符。
// 这是纵深防御：sanitizeName 已经过滤，但库名还会被拼进 CREATE/DROP 语句，
// 多一道断言可以避免将来有人改动命名规则时引入标识符注入。
func validSchemaName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// replaceDatabase 把 DSN 里的库名换成 schema。
//
// go-sql-driver 的 DSN 形如 user:pass@tcp(host:port)/dbname?params，库名在
// 最后一个 '/' 与 '?' 之间。用字符串切分而不是正则：DSN 里还可能出现括号与
// 特殊字符，正则容易误伤。
func replaceDatabase(dsn, schema string) string {
	params := ""
	body := dsn
	if i := strings.Index(dsn, "?"); i >= 0 {
		body, params = dsn[:i], dsn[i:]
	}
	slash := strings.LastIndex(body, "/")
	if slash < 0 {
		return dsn
	}
	return body[:slash+1] + schema + params
}

// closeMigrator 关闭 migrator 及其独占连接，忽略两个来源各自的错误。
//
// migrate.Migrate.Close 返回 (sourceErr, databaseErr)，不是单个 error，因此不能
// 写成 `defer func(){ _ = m.Close() }()`。关闭失败不影响已经完成的迁移结果，也不该
// 覆盖调用方真正关心的错误，所以这里明确丢弃。
//
// conn 通常已被 Close 内部关闭（驱动持有它），再 Close 一次是幂等的 no-op，
// 这里保留是为了在所有提前返回路径上都不泄漏连接。
func closeMigrator(m *migrate.Migrate, conn *sql.DB) {
	srcErr, dbErr := m.Close()
	_, _ = srcErr, dbErr
	_ = conn.Close()
}

// migrationsDir 返回 backend/migrations 的绝对路径。
//
// 用 runtime.Caller 定位本文件，再回退三级（testdb -> platform -> internal ->
// backend），这样无论 go test 从哪个工作目录启动都能找到迁移文件。
// 依赖相对路径 "./migrations" 会在不同包目录下失效。
//
// 路径统一转成斜杠形式：golang-migrate 的 file:// 源要求 URL 风格，
// Windows 的反斜杠会被当成转义序列。
func migrationsDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("无法定位 testdb 源文件路径")
	}
	// .../backend/internal/platform/testdb/testdb.go -> .../backend
	backend := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
	dir := filepath.Join(backend, "migrations")

	if !strings.HasSuffix(filepath.ToSlash(dir), "/migrations") {
		return "", fmt.Errorf("迁移目录路径异常: %s", dir)
	}
	return dir, nil
}

// EscapeDSNForLog 返回可安全写入日志的 DSN：隐藏密码部分。
// 测试失败信息里偶尔需要打出 DSN 定位问题，直接打印会把容器密码写进 CI 日志。
func EscapeDSNForLog(dsn string) string {
	u, err := url.Parse("mysql://" + dsn)
	if err != nil || u.User == nil {
		return "<unparsable dsn>"
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword(u.User.Username(), "redacted")
	}
	return u.String()
}
