# migrate

基于 GORM + Cobra 的 Go 数据库迁移工具，支持 MySQL、PostgreSQL、SQLite。

> **⚠️ 维护状态**：本目录是 v1 线（`github.com/gtkit/migrate`），已进入维护模式，仅接收关键修复，新功能与安全加固只在 v2 线发布。新项目请使用 [v2](./v2/README.md)：
>
> ```bash
> go get github.com/gtkit/migrate/v2@latest
> ```

## 特性

- 迁移文件按时间戳排序，支持 up / down / reset / refresh / fresh
- 每个迁移的执行逻辑与迁移记录写入在同一事务中提交：PostgreSQL / SQLite 支持事务型 DDL，失败自动整体回滚，不留"已执行但无记录"的中间状态（MySQL 的 DDL 会隐式提交、无法回滚，属其固有限制）
- Advisory Lock 防止多实例并发迁移冲突（MySQL `GET_LOCK`、PostgreSQL `pg_advisory_lock`），获取锁超时可配置，超时返回错误而非无限阻塞
- `pending` 命令预览待执行迁移（dry-run）
- 结构化日志接口，可注入 zap / zerolog / slog 等
- Lock name 可配置，同一数据库多项目共存不冲突
- `make migration` 自动生成迁移文件和 model 脚手架
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

# 修改表
myapp make migration update_users_table

# 添加字段到表
myapp make migration add_email_to_users_table

# 添加字段并指定位置（MySQL AFTER）——生成 raw SQL，在 ADD COLUMN 后追加 AFTER 子句
myapp make migration add_email_to_users_table --after phone

# 删除字段
myapp make migration drop_column_avatar_from_users_table

# 删除索引
myapp make migration drop_index_email_from_users_table
```

执行后会在 `database/migrations/` 下生成形如 `2026_03_17_120000_create_users_table.go` 的文件。

**迁移模板均自包含、显式、可审查**：`create` 生成结构快照 struct，`add`/`update`/`drop`/`drop_column`/`drop_index` 生成**显式 raw SQL**，都不 import 业务 model、不使用 `AutoMigrate`（避免随 model 演进漂移）。`add`/`update` 及删索引模板留有 `TODO` 占位，需补全列/变更定义（大表建议标注 `ALGORITHM`/`LOCK` 在线 DDL 策略）后才能通过 `migrate lint`。

| action | up 行为 | down 行为 |
|--------|---------|-----------|
| `create` | 快照 struct `CreateTable` | `DropTable` |
| `update` | raw `ALTER TABLE`（TODO 待补全） | raw 反向 `ALTER`（TODO 待补全） |
| `add` | raw `ALTER TABLE ADD COLUMN`（TODO 列定义，带存在性检查） | raw `DROP COLUMN`（带存在性检查） |
| `drop` (表) | `DropTable`（带存在性检查） | 标记为不可逆，需人工补全 |
| `drop_column` / `drop_index` | raw `DROP COLUMN` / `DROP INDEX`（带存在性检查） | 标记为不可逆，需人工补全 |

示例（`create`）——迁移文件**自包含表结构快照，有意不引用业务 model**：业务 model 会随需求演进，而迁移必须锁定「创建当时」的结构，才能保证任何环境、任何时间执行都得到一致的表：

```go
package migrations

import (
    "time"

    "gorm.io/gorm"

    "github.com/gtkit/migrate/migration"
)

// userV20260317120000 是本迁移建表时的结构快照，锁定创建当时的表结构。
type userV20260317120000 struct {
    ID        int64          `gorm:"column:id;primaryKey;autoIncrement"`
    CreatedAt time.Time      `gorm:"column:created_at;type:datetime;index"`
    UpdatedAt time.Time      `gorm:"column:updated_at;type:datetime;index"`
    DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;type:datetime;index"`
    // 在此补全建表字段（不要 import 业务 model）。
}

func (userV20260317120000) TableName() string { return "users" }

func init() {
    up := func(db *gorm.DB) error {
        return db.Migrator().CreateTable(&userV20260317120000{})
    }

    down := func(db *gorm.DB) error {
        return db.Migrator().DropTable("users")
    }

    migration.Add("2026_03_17_120000_create_users_table", up, down)
}
```

> 生成的业务 model 脚手架（`internal/models/user.go`）与本快照初始一致，但二者之后各自演进：model 随业务变化，迁移文件一旦执行不再修改，字段变更请新建 `add_`/`update_` 迁移。

`create` 操作会同时生成以下文件：

```text
internal/models/model.go                    # BaseID、BaseTimeField（仅首次生成）
internal/models/user.go                     # GORM model 骨架（含 ListPaging、CRUD 方法）
internal/repository/user/repository.go      # Repository 结构体 + New(db) 构造函数
internal/repository/user/repository_util.go # Get / GetBy / All / IsExist / Paginate
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

# 回滚到指定版本（回滚所有比该版本新的迁移，不含该版本本身）
myapp migrate down-to 2026_03_17_120000_create_users_table

# 回滚所有迁移（破坏性，需 --force）
myapp migrate reset --force

# 回滚所有后重新执行（破坏性，需 --force；需连接池 MaxOpenConns ≥ 2）
myapp migrate refresh --force

# 删除所有表后重新执行（⚠️ 危险，会丢失数据；需 --force；需连接池 MaxOpenConns ≥ 2）
myapp migrate fresh --force

# 把 pending 迁移标记为已应用而不执行其 SQL（用于接入已有等价结构的存量库，需 --force）
myapp migrate mark-applied --force
# 只标记到指定版本为止
myapp migrate mark-applied --to 2026_03_17_120000_create_users_table --force
```

> `mark-applied` 只写迁移记录、不执行建表/改表，且**不校验数据库真实结构是否与这些迁移等价**——仅用于数据库结构已等价于这些迁移净效果的存量库（如从其他工具迁移过来）。标错会让后续 `up` 跳过真实建表、造成 schema 漂移，故强制 `--force`。
>
> `reset` / `refresh` / `fresh` 会回滚或删除数据，必须显式加 `--force` 才执行，缺失时直接报错拒绝，避免误触丢数据。
>
> `fresh` / `refresh` 需要连接池至少 2 条连接（一条持迁移锁、另一条删表/回滚重建）；`SetMaxOpenConns(1)` 时会提前报错而非死等超时。

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

当多个服务共享同一个数据库时，使用 `WithLockName` 避免迁移锁冲突：

```go
// user-service
migrate.Setup(db, migrate.WithLockName("user_svc_migrate"))

// order-service
migrate.Setup(db, migrate.WithLockName("order_svc_migrate"))
```

不同的 lock name 会生成不同的 advisory lock key，各项目的迁移互不阻塞。

## 编程式调用

除了 CLI 命令，也可以在代码中直接调用 Migrator：

```go
m := migration.NewMigrator("database/migrations", db,
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
