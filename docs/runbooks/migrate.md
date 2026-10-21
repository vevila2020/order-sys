# 数据库迁移运行手册

迁移工具：`golang-migrate/v4`。迁移文件是**唯一的 schema 事实来源**，
不使用 GORM `AutoMigrate`。

- 文件位置：`backend/migrations/`
- 命名：`NNNNNN_description.up.sql` / `.down.sql`，序号六位、递增、不复用。
- 版本记录表：`schema_migrations`（由 golang-migrate 自动维护）。

## 强制约定

| 约束 | 原因 |
| --- | --- |
| DSN 必须带 `multiStatements=true` | golang-migrate 的 MySQL 驱动按语句切分后整批下发；不开启时一个文件里的多条语句会失败 |
| 字符集 `utf8mb4`、排序规则 `utf8mb4_unicode_ci` | 与部署服务器一致；`utf8mb4_0900_ai_ci` 的等价字符集不同，混用会导致同一查询两边结果不一致 |
| 时间列 `DATETIME(6)`，存 UTC | `TIMESTAMP` 会按会话时区转换，客户端漏设 `time_zone` 就写入偏移值 |
| 金额 `BIGINT UNSIGNED` | 整数最小货币单位，禁止浮点 |
| `down` 按外键逆序删除，不用 `CASCADE` | 顺序错误会在回滚时才暴露；`CASCADE` 会静默删掉没列出的表 |
| 跨模块只存 ID，不建外键 | 模块间是数据所有权边界；外键会把未来拆服务变成硬依赖 |

生产 DSN **不要**开 `multiStatements`：应用代码用参数化查询不需要它，
开启会扩大 SQL 注入的影响面。它只用于迁移。

## 执行迁移

本地/CI 用集成测试驱动，不需要手工执行：

```powershell
# 迁移 up/down/up、字符集、CHECK 约束、索引顺序、列类型
cd backend
go test -tags=integration ./internal/platform/testdb -count=1 -v
```

对真实库执行时，用 `migrate` CLI 或写一个一次性命令，DSN 从环境变量读取，
不要写进命令行参数（会进 shell 历史与进程列表）：

```powershell
# Debian（在服务器上）
export MYSQL_DSN='dev:<密码>@tcp(127.0.0.1:3306)/order_sys?parseTime=true&loc=UTC&multiStatements=true'
migrate -database "mysql://${MYSQL_DSN}" -path ./backend/migrations up
```

```powershell
# Windows PowerShell 7（经隧道连远程库，先启动 scripts/ssh-tunnel.ps1）
$env:MYSQL_DSN = 'dev:<密码>@tcp(127.0.0.1:13306)/order_sys?parseTime=true&loc=UTC&multiStatements=true'
migrate -database "mysql://$env:MYSQL_DSN" -path .\backend\migrations up
```

## 验证迁移是否成功

```sql
-- 版本与 dirty 标记；dirty=1 表示上一次迁移中途失败
SELECT * FROM schema_migrations;

-- 表清单
SELECT table_name, table_collation
  FROM information_schema.tables
 WHERE table_schema = DATABASE()
 ORDER BY table_name;
```

期望：`version = 1`、`dirty = 0`、9 张业务表、collation 全部为 `utf8mb4_unicode_ci`。

## `dirty` 状态处置

迁移中途失败会把版本标记为 `dirty = 1`，此后 `migrate up` 一律拒绝执行。
这是保护机制：schema 处于未知的半应用状态。

**处置步骤：**

1. 查清楚失败到哪一步：

   ```sql
   SELECT * FROM schema_migrations;
   SHOW TABLES;
   ```

2. **开发库**：直接重建。

   ```powershell
   # 确认没有需要保留的数据后
   migrate -database "mysql://${MYSQL_DSN}" -path ./backend/migrations force 0
   migrate -database "mysql://${MYSQL_DSN}" -path ./backend/migrations down -all
   ```

3. **有数据的库**：**不要**执行 `force` 或 `down`。`force` 只改版本号、
   不修复 schema；`down` 会删表丢数据。正确做法是：
   - 备份；
   - 手工把 schema 调整到上一个干净版本的状态（或新建空库重新迁移）；
   - 用 `force <上一个成功版本>` 修正版本号；
   - 重新执行 `up`；
   - 逐项核对表结构与数据。

   `force` 是"我知道我在做什么"的逃生门，不是修复工具。

## 禁止事项

- **禁止修改已发布的迁移文件。** 已应用到任何环境的迁移文件一旦改动，
  不同环境的 schema 会分叉而版本号相同。修正要新增一个迁移。
- **禁止用 `AutoMigrate` 补表。** 它不产生版本记录，会让迁移状态与实际 schema 脱节。
- **禁止在 down 里 `DROP DATABASE`。** down 只删它自己建的表。
- **禁止在生产执行 `down -all`。**

## 新增一次迁移的检查清单

- [ ] 新文件序号 = 当前最大序号 + 1，`up`/`down` 成对。
- [ ] 每张新表显式写 `DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci`。
- [ ] 每个索引的注释写清楚查询场景（需求 §9.3）。
- [ ] `down` 的删除顺序与外键方向相反。
- [ ] 更新 `internal/platform/testdb/migrate_test.go` 的 `expectedTables`。
- [ ] 集成测试 up → down → up 通过。
- [ ] 在 `CHANGELOG.md` 记录。

## 关联

- [本地开发](./local-dev.md)
- [ADR-0003 事务与并发控制](../adr/0003-transaction-and-locking.md)
- 需求规格 §4.5（数据字典）、§9.3（编码约定）
