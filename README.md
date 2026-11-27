# migrate

基于 GORM + Cobra 的 Go 数据库迁移工具。官方支持与实库验收范围仅为 MySQL。

> **⚠️ 维护状态**：本目录是 v1 线（`github.com/gtkit/migrate`），已进入维护模式，仅接收关键修复，新功能与安全加固只在 v2 线发布。新项目请使用 [v2](./v2/README.md)：
>
> ```bash
> go get github.com/gtkit/migrate/v2@latest
> ```

## 特性

- 迁移文件按时间戳排序，支持 up / down / reset / refresh / fresh
- 每个迁移在事务块内执行回调和账本操作。MySQL DDL 会隐式提交，迁移中途失败时需核对表结构和账本。
- 使用 MySQL `GET_LOCK` 防止多实例并发迁移，获取锁的超时可配置。
- `pending` 命令预览待执行迁移（dry-run）
- 结构化日志接口，可注入 zap / zerolog / slog 等
- Lock name 可配置，同一数据库多项目共存不冲突
- `make migration` 自动生成迁移文件和 model 脚手架
  新建脚手架目录的权限最多为 `0755`，实际权限还受本机 umask 限制。
- 所有操作返回 `error`，不在库内调用 `os.Exit`

## 安装

```bash
go get github.com/gtkit/migrate@latest
```

## 快速开始

### 1. 初始化

在程序启动时调用 `migrate.Setup`，传入 GORM 数据库连接和选项：

```go
package main

import (
    "log"

    "github.com/gtkit/migrate"
    "github.com/gtkit/migrate/command"
    "github.com/spf13/cobra"
    "gorm.io/driver/mysql"
    "gorm.io/gorm"
)

func main() {
    db, err := gorm.Open(mysql.Open("user:pass@tcp(127.0.0.1:3306)/mydb?parseTime=true"), &gorm.Config{})
    if err != nil {
        log.Fatal(err)
    }

    // 初始化迁移工具
    if err := migrate.Setup(db,
        migrate.WithProjectName("myproject"),
        migrate.WithMigrationDir("database/migrations"),
    ); err != nil {
        log.Fatal(err)
    }

    // 注册到 cobra
    rootCmd := &cobra.Command{Use: "myapp"}
    rootCmd.AddCommand(command.Commands()...)

    if err := rootCmd.Execute(); err != nil {
        log.Fatal(err)
    }
}
```

### 2. Setup 选项一览

```go
migrate.Setup(db,
    migrate.WithProjectName("myproject"),             // 项目名称，用于代码生成
    migrate.WithMigrationDir("database/migrations"),  // 迁移文件目录，默认 database/migrations
    migrate.WithMigrationsTable("app_migrations"),    // 迁移记录表，默认 migrations
    migrate.WithDDLModels(&models.User{}),             // 注册用于建表快照的模型，默认空
    migrate.WithTimeout(10 * time.Minute),            // 迁移超时时间，默认 5 分钟
    migrate.WithLockName("myproject_migrate"),         // 迁移锁名称，默认 migrate_lock
    migrate.WithLockTimeout(30 * time.Second),        // 获取迁移锁的最长等待时间，默认 10 秒
    migrate.WithLogger(myLogger),                     // 自定义日志，默认 stdout
)
```

### 3. 创建迁移文件

```bash
# 创建表
myapp make migration create_users_table

# 从已注册的模型生成建表快照
myapp make migration create_users_table --from-model

# 修改表
myapp make migration update_users_table

# 重命名表
myapp make migration rename_users_to_accounts_table

# 添加字段到表
myapp make migration add_email_to_users_table

# 添加字段并指定位置（MySQL AFTER）——生成 raw SQL，在 ADD COLUMN 后追加 AFTER 子句
myapp make migration add_email_to_users_table --after phone

# 添加唯一索引，复合索引可传 --columns email,tenant_id，自定义索引名用 --index-name
myapp make migration add_index_email_to_users_table --unique

# 删除字段
myapp make migration drop_column_avatar_from_users_table

# 删除索引，使用数据库中的实际索引名
myapp make migration drop_index_idx_users_email_from_users_table
```

执行后会在 `database/migrations/` 下生成形如 `2026_03_17_120000_create_users_table.go` 的文件。

迁移模板固定物理表名和列名。建表使用迁移文件内的结构快照。加列和更新模板需要填写明确的 SQL 定义，留空时会返回错误。生成目录会自动创建，生成的 Go 文件会按 gofmt 格式输出。

| 操作 | Up | Down |
|---|---|---|
| `create` | 用文件内的结构快照建表 | 删除同一张表 |
| `create --from-model` | 用已注册模型的字段和 GORM 标签生成完整建表快照 | 删除同一张表 |
| `add` | 填完列定义后执行显式加列，`--after` 使用 MySQL 列定位 | 删除同一列 |
| `add_index` | 创建指定索引，默认索引名为 `idx_<表>_<列...>` | 删除同一索引 |
| `update` | 填完变更 SQL 后执行 | 补齐反向 SQL 后注册 |
| `rename` | 将旧表名改为新表名 | 将新表名改回旧表名 |
| `drop` / `drop_column` / `drop_index` | 按物理表、列或索引名执行删除 | 默认不可逆，回滚前返回错误 |

新生成的 MySQL 建表、加列和加索引迁移会读取现有对象的定义。对象缺失时执行创建，定义一致时保留对象并记入迁移记录。定义不同、读取失败或无法确认一致时返回错误，迁移保留为待执行。登记后，回滚按迁移文件里的 Down 删除对应对象，包括原先已存在的对象。

重命名命令使用 `rename_<旧表>_to_<新表>_table`，两个物理表名须不同，名称中只能有一个 `_to_` 分隔符。生成的 Up 和 Down 可直接执行，目标表名已存在时会返回数据库错误。业务模型的 `TableName()` 由应用在切换到新表时更新。

`--from-model` 用 GORM 的 `TableName()` 或命名策略确定物理表名，再与命令中的表名精确匹配。生成器复制导出字段及标签，展开普通匿名嵌入字段，跳过 `gorm:"-"`，并写入字段类型需要的导入。模型只在生成时读取。字段引用自定义类型时，该类型需要能从迁移包访问。找不到匹配模型时会报错且不落盘。此模式只生成迁移文件。

普通建表模板生成 ID 和时间戳字段。在首次执行迁移前，将当时的业务字段补进迁移文件内的 `snapshot`。生成的 model 和 repository 脚手架使用应用项目中的分页包。

普通 `create` 还会生成以下文件：

```text
internal/models/model.go                    # BaseID、BaseTimeField（仅首次生成）
internal/models/user.go                     # GORM model 骨架（含 ListPaging、CRUD 方法）
internal/repository/user/user_i.go          # Repository 结构体 + New(db) 构造函数
internal/repository/user/user_util.go       # Get / GetBy / All / IsExist / Paginate
```

### 4. 执行迁移

```bash
# 预览待执行的迁移（dry-run，不实际执行）
myapp migrate pending

# 执行所有未迁移的文件
myapp migrate up

# 查看所有迁移状态
myapp migrate status

# 回滚最后一批迁移
myapp migrate down

# 回滚所有迁移
myapp migrate reset

# 回滚所有后重新执行
myapp migrate refresh

# 删除所有表后重新执行（⚠️ 危险，会丢失数据）
myapp migrate fresh
```

`fresh` 和 `refresh` 会在修改表或回滚前检查重放清单。缺少已注册文件、迁移未注册或缺少 `Up` 时会报错。回滚目标缺少 `Down` 时，整批拒绝执行。支持显式确认及指定版本回滚的命令见 v2 README。


### 5. Makefile 集成

推荐在项目中创建 `migrate.mk`，通过 `include migrate.mk` 引入到主 `Makefile`，统一使用 `make` 命令操作：

```makefile
# Makefile
include migrate.mk
```

`migrate.mk` 示例：

```makefile
MIGRATE_ENV = $(if $(ENV),$(ENV),dev)

migrate:                ## 执行迁移
	go run . migrate up -c $(MIGRATE_ENV)

migrate-down:           ## 回滚最后一批
	go run . migrate down -c $(MIGRATE_ENV)

migrate-status:         ## 查看迁移状态
	go run . migrate status -c $(MIGRATE_ENV)

migrate-pending:        ## 预览待执行迁移（dry-run）
	go run . migrate pending -c $(MIGRATE_ENV)

migration:              ## 生成 migration 文件: make migration create_users_table
	go run . make migration $(filter-out $@,$(MAKECMDGOALS)) -c dev

model:                  ## 生成 model + repository: make model user
	go run . make model $(filter-out $@,$(MAKECMDGOALS)) -c dev

%:
	@:
```

常用命令速查：

| Makefile 命令 | 作用 |
|---------------|------|
| `make migrate` | 执行所有未运行的迁移 |
| `make migrate-down` | 回滚最后一批迁移 |
| `make migrate-status` | 查看迁移状态 |
| `make migrate-pending` | 预览待执行迁移 |
| `make model user` | 生成 model + repository |
| `make migration create_users_table` | 生成 migration 文件 |

### 6. 命令输出示例

```bash
$ myapp migrate pending
Pending migrations (2):
  1. 2026_03_17_120000_create_users_table
  2. 2026_03_17_120100_create_orders_table

Run 'migrate up' to execute these migrations.
```

```bash
$ myapp migrate up
Running migrations...
[INFO]  migrating                                          file=2026_03_17_120000_create_users_table batch=1
[INFO]  migrated                                           file=2026_03_17_120000_create_users_table
[INFO]  migrating                                          file=2026_03_17_120100_create_orders_table batch=1
[INFO]  migrated                                           file=2026_03_17_120100_create_orders_table
Migrations completed.
```

```bash
$ myapp migrate status
Migration Status:
--------------------------------------------------
  2026_03_17_120000_create_users_table              Ran (batch 1)
  2026_03_17_120100_create_orders_table              Ran (batch 1)
  2026_03_18_090000_add_email_to_users_table         Pending
```

## 自定义日志

默认日志输出到 stdout。生产环境建议注入结构化日志实现。

### Logger 接口

```go
type Logger interface {
    Info(msg string, keysAndValues ...any)
    Warn(msg string, keysAndValues ...any)
    Error(msg string, keysAndValues ...any)
}
```

签名兼容 `zap.SugaredLogger`、`slog` 的 key-value 风格。

### 接入 gtkit/logger

执行器在每条 `Up` 或 `Down` 开始时记录迁移名和批次，事务返回后记录成功或失败和耗时。只需在应用入口通过 `WithLogger` 注入适配器，生成的回调可以直接返回执行错误。

```go
import gtkitlog "github.com/gtkit/logger/v2"

type migrationLogger struct {
    logger *gtkitlog.Logger
}

func (l migrationLogger) Info(msg string, kv ...any)  { l.logger.Infow(msg, kv...) }
func (l migrationLogger) Warn(msg string, kv ...any)  { l.logger.Warnw(msg, kv...) }
func (l migrationLogger) Error(msg string, kv ...any) { l.logger.Errorw(msg, kv...) }

// 在应用初始化日志后，使用已配置的 appLogger。
err := migrate.Setup(db, migrate.WithLogger(migrationLogger{logger: appLogger}))
```

### 接入 zap

```go
type zapLogger struct {
    s *zap.SugaredLogger
}

func (l *zapLogger) Info(msg string, kv ...any)  { l.s.Infow(msg, kv...) }
func (l *zapLogger) Warn(msg string, kv ...any)  { l.s.Warnw(msg, kv...) }
func (l *zapLogger) Error(msg string, kv ...any) { l.s.Errorw(msg, kv...) }

// 使用
sugar := zap.NewProduction().Sugar()
migrate.Setup(db, migrate.WithLogger(&zapLogger{s: sugar}))
```

### 接入 zerolog

```go
type zerologLogger struct {
    l zerolog.Logger
}

func (z *zerologLogger) Info(msg string, kv ...any) {
    z.l.Info().Fields(kvToMap(kv)).Msg(msg)
}
func (z *zerologLogger) Warn(msg string, kv ...any) {
    z.l.Warn().Fields(kvToMap(kv)).Msg(msg)
}
func (z *zerologLogger) Error(msg string, kv ...any) {
    z.l.Error().Fields(kvToMap(kv)).Msg(msg)
}

func kvToMap(kv []any) map[string]any {
    m := make(map[string]any, len(kv)/2)
    for i := 0; i+1 < len(kv); i += 2 {
        m[fmt.Sprint(kv[i])] = kv[i+1]
    }
    return m
}
```

### 接入 slog（Go 1.21+）

```go
type slogLogger struct {
    l *slog.Logger
}

func (s *slogLogger) Info(msg string, kv ...any)  { s.l.Info(msg, kv...) }
func (s *slogLogger) Warn(msg string, kv ...any)  { s.l.Warn(msg, kv...) }
func (s *slogLogger) Error(msg string, kv ...any) { s.l.Error(msg, kv...) }

// slog 的签名天然匹配，直接包一层即可
migrate.Setup(db, migrate.WithLogger(&slogLogger{l: slog.Default()}))
```

### 静默日志（测试场景）

```go
migrate.Setup(db, migrate.WithLogger(&migration.NopLogger{}))
```

## 多项目共用数据库

当多个服务共用一个数据库时，可以分别配置迁移记录表。需要迁移同一组业务表的服务应使用同一个锁名。

```go
// user-service
migrate.Setup(db,
    migrate.WithMigrationsTable("user_svc_migrations"),
    migrate.WithLockName("user_svc_migrate"),
)

// order-service
migrate.Setup(db,
    migrate.WithMigrationsTable("order_svc_migrations"),
    migrate.WithLockName("order_svc_migrate"),
)
```

`WithMigrationsTable` 默认使用 `migrations`。表名须以 ASCII 字母或下划线开头，最多 63 个 ASCII 字母、数字或下划线。首尾空白会被去除，空白或非法表名会使 `Setup` 返回错误。

建表、迁移状态、批次记录和回滚都使用配置的表。已有迁移的数据库切换表名时，迁移状态会从新选中的表读取。`fresh` 仍会清理整个数据库中的表，只适用于项目独占的数据库。

## 编程式调用

除了 CLI 命令，也可以在代码中直接调用 Migrator：

```go
m := migration.NewMigrator("database/migrations", db,
    migration.WithMigrationsTable("app_migrations"),
    migration.WithLockName("myproject_migrate"),
    migration.WithLogger(myLogger),
)

ctx := context.Background()

// 检查待执行的迁移
pending, err := m.Pending(ctx)

// 执行迁移
err = m.Up(ctx)

// 回滚最后一批
err = m.Rollback(ctx)

// 回滚指定步数
err = m.RollbackSteps(ctx, 3)

// 查看状态
statuses, err := m.Status(ctx)
```

## 集成测试

v1 发版检查默认要求 `MIGRATE_TEST_MYSQL_DSN`，在竞态和覆盖率检查中运行 MySQL 实库测试。请使用独立的空测试库，库名须包含 `test`。指定 `INTEGRATION=off` 可显式跳过实库，普通测试和覆盖率检查仍会运行。

```bash
export MIGRATE_TEST_MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/migrate_test?parseTime=true'
GOWORK=off MIGRATE_TEST_REQUIRE_DATABASES=1 go test -tags=integration -race -count=1 ./migration -run '^TestMigratorMySQLIntegration$'
```

实库用例验证建表、回滚和迁移记录、同名迁移锁竞争与超时后的锁释放，以及 `fresh` 成功或删表中途失败时是否在同一 MySQL 会话恢复 `foreign_key_checks` 原值。测试覆盖初始开启和关闭两种状态，并在结束后清理测试表。

`fresh` 用例还会验证删表连接断开或恢复语句被 MySQL 拒绝后，故障会话从服务端退出，且连接池补充新会话。

迁移记录表用例验证默认表名、自定义表的写入、状态和回滚，并核对默认表中的记录在自定义表的操作期间保持原值。

## 项目结构

```
migrate/
├── migrate.go              # 入口：Setup、Config、Cobra 命令
├── version.go
├── command/
│   └── command.go          # 聚合所有命令
├── console/
│   └── console.go          # 终端颜色输出
├── file/
│   └── file.go             # 文件操作工具
├── make/
│   ├── make.go             # 代码生成核心
│   ├── make_cmd.go         # make cmd 命令
│   ├── make_migration.go   # make migration 命令
│   ├── make_model.go       # make model 命令
│   └── stubs/              # 代码模板
└── migration/
    ├── migrator.go         # 迁移执行核心
    ├── migration_file.go   # 迁移注册表
    ├── model.go            # migrations 表模型
    ├── database.go         # 多数据库支持
    ├── lock.go             # 分布式迁移锁
    └── logger.go           # 日志接口
```

## License

MIT
