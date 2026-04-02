# migrate

基于 GORM + Cobra 的 Go 数据库迁移工具，支持 MySQL、PostgreSQL、SQLite。

## 特性

- 迁移文件按时间戳排序，支持 up / down / reset / refresh / fresh
- Advisory Lock 防止多实例并发迁移冲突（MySQL `GET_LOCK`、PostgreSQL `pg_advisory_lock`）
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

# 删除字段
myapp make migration drop_column_avatar_from_users_table

# 删除索引
myapp make migration drop_index_email_from_users_table
```

执行后会在 `database/migrations/` 下生成形如 `2026_03_17_120000_create_users_table.go` 的文件。

不同 action 使用不同的 migration 模板：

| action | up 行为 | down 行为 |
|--------|---------|-----------|
| `create` | `CreateTable` | `DropTable` |
| `update` | `AutoMigrate` | 标记为不可逆，需人工补全 |
| `add` | `AddColumn`（带存在性检查） | `DropColumn`（带存在性检查） |
| `drop` | `DropTable` / `DropColumn` / `DropIndex` | 对应的反向操作 |

示例（`create`）：

```go
package migrations

import (
    "myproject/internal/models"
    "gorm.io/gorm"

    "github.com/gtkit/migrate/migration"
)

func init() {
    up := func(db *gorm.DB) error {
        return db.Migrator().CreateTable(&models.User{})
    }

    down := func(db *gorm.DB) error {
        return db.Migrator().DropTable(&models.User{})
    }

    migration.Add("2026_03_17_120000_create_users_table", up, down)
}
```

`create` 操作会同时生成以下文件：

```text
internal/models/model.go          # BaseID、BaseTimeField（仅首次生成）
internal/models/user.go           # GORM model 骨架（含 ListPaging、CRUD 方法）
internal/repository/user/user_i.go      # Repository 结构体 + New 构造函数
internal/repository/user/user_util.go   # Get / GetBy / All / IsExist / Paginate
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

### 5. 命令输出示例

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
