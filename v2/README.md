# migrate

基于 GORM + Cobra 的 Go 数据库迁移工具，支持 MySQL、PostgreSQL、SQLite。

## 特性

- 迁移文件按时间戳排序，支持 up / down / reset / refresh / fresh
- Advisory Lock 防止多实例并发迁移冲突（MySQL `GET_LOCK`、PostgreSQL `pg_advisory_lock`）
- `pending` 命令预览待执行迁移（dry-run）
- `lint` 命令检查 migration 文件、registry 和已执行记录漂移
- 结构化日志接口，可注入 zap / zerolog / slog 等
- Lock name 可配置，同一数据库多项目共存不冲突
- `make migration` 自动生成迁移文件和 model 脚手架
- `make ddl diff` 可对比当前模型生成的 strict DDL 与已提交 SQL 文件
- 所有操作返回 `error`，不在库内调用 `os.Exit`

## 安装

```bash
go get github.com/gtkit/migrate/v2@latest
```

## 适用场景

这个工具适合下面几类项目：

- 基于 GORM 的 Go 服务，需要统一的 migration 命令和执行顺序
- 希望默认目录结构对齐 `go-gin-api` 风格项目
- 希望把“模型定义”和“DDL 文件”一起纳入代码审查
- 希望在 CI 里阻止 migration 漂移、DDL 漂移、危险回滚场景

如果你的项目完全不使用 GORM model 驱动 schema，这个工具仍然能管理 migration 执行，但 `make model`、`make ddl`、`make ddl diff` 的价值会下降。

## 命令总览

| 命令 | 作用 | 典型用途 |
|------|------|----------|
| `make model <name>` | 生成 model + repository 默认代码 | 新建业务实体 |
| `make migration <name>` | 生成 migration 文件 | 新建表、加列、删列、删索引 |
| `make ddl <model>` | 生成 strict create-table DDL | 提交 DDL 文件、对齐 schema |
| `make ddl diff <model>` | 比较当前模型 DDL 与已提交 SQL | CI 检查 schema 漂移 |
| `migrate pending` | 查看待执行 migration | 上线前确认 |
| `migrate up` | 执行未运行 migration | 发布时执行 |
| `migrate down` | 回滚最后一批 migration | 紧急回滚 |
| `migrate reset` | 回滚全部 migration | 测试环境重置 |
| `migrate refresh` | 回滚全部再重放 | 测试环境验证 |
| `migrate fresh` | 删库内所有表再重放 migration | 仅测试环境 |
| `migrate status` | 查看运行状态 | 运维排查 |
| `migrate lint` | 检查文件、registry、数据库记录漂移 | CI / 发布前检查 |

## 快速开始

### 1. 初始化

在程序启动时调用 `migrate.Setup`，传入 GORM 数据库连接和选项：

```go
package main

import (
    "log"

    "github.com/gtkit/migrate/v2"
    "github.com/gtkit/migrate/v2/command"
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

最常见的初始化方式：

```go
package main

import (
    "log"
    "time"

    "github.com/gtkit/migrate/v2"
    "yourapp/internal/models"
)

func setupMigrate(db *gorm.DB) {
    if err := migrate.Setup(db,
        migrate.WithProjectName("yourapp"),
        migrate.WithMigrationDir("database/migrations"),
        migrate.WithModelDir("internal/models"),
        migrate.WithRepositoryDir("internal/repository"),
        migrate.WithDDLDir("database/ddl"),
        migrate.WithDDLModels(
            &models.User{},
            &models.Order{},
        ),
        migrate.WithTimeout(10*time.Minute),
        migrate.WithLockName("yourapp_migrate"),
    ); err != nil {
        log.Fatal(err)
    }
}
```

各配置项含义：

| 配置 | 默认值 | 说明 |
|------|--------|------|
| `WithProjectName` | `project_name` | 生成代码里的 module import 前缀 |
| `WithMigrationDir` | `database/migrations` | migration 文件目录 |
| `WithModelDir` | `internal/models` | model 生成目录 |
| `WithRepositoryDir` | `internal/repository` | repository 生成目录 |
| `WithDDLDir` | `database/ddl` | DDL SQL 输出目录 |
| `WithDDLModels` | 空 | 注册可用于 `make ddl` / `make ddl diff` 的模型 |
| `WithTimeout` | `5m` | `migrate` 命令执行超时 |
| `WithLockName` | `migrate_lock` | 分布式 advisory lock 名称 |
| `WithLogger` | stdout logger | 自定义结构化日志 |

注意：

- `migrate.Setup(...)` 是必须的；不调用就不能使用 `migrate` 或 `make ddl` 相关命令。
- `WithDDLModels(...)` 只影响 DDL 生成和 diff，不影响 migration 执行。
- 命令行里的目录 flag 优先级高于 `Setup` 里的默认配置。

```go
migrate.Setup(db,
    migrate.WithProjectName("myproject"),             // 项目名称，用于代码生成
    migrate.WithMigrationDir("database/migrations"),  // 迁移文件目录，默认 database/migrations
    migrate.WithModelDir("internal/models"),          // model 目录，默认 internal/models
    migrate.WithRepositoryDir("internal/repository"), // repository 目录，默认 internal/repository
    migrate.WithDDLDir("database/ddl"),               // DDL 输出目录，默认 database/ddl
    migrate.WithDDLModels(&models.User{}),            // 注册可生成 DDL 的模型
    migrate.WithTimeout(10 * time.Minute),            // 迁移超时时间，默认 5 分钟
    migrate.WithLockName("myproject_migrate"),         // 迁移锁名称，默认 migrate_lock
    migrate.WithLogger(myLogger),                     // 自定义日志，默认 stdout
)
```

### 3. 创建迁移文件

#### 3.1 `make model`

生成一个基础 model 和 repository：

```bash
myapp make model user
```

默认会生成：

```text
internal/models/model.go                        # BaseID、BaseTimeField（仅首次生成）
internal/models/doc.go                          # 包注释（仅首次生成）
internal/models/user.go                         # GORM model 骨架
internal/repository/user/user_i.go              # Repository 结构体 + New 构造函数
internal/repository/user/user_util.go           # Get / GetBy / All / IsExist / Paginate
```

其中：

- `model.go` / `doc.go` 只会在不存在时补齐，不会反复覆盖
- `user.go` 是 GORM model 骨架，包含 `ListPaging` 分页结构体、`Create` / `Save` / `Delete` / `CreateOrUpdate` / `MarshalBinary` / `UnmarshalBinary` 等常用方法
- `user_i.go` 定义 `Repository` 结构体、`New(db *gorm.DB)` 构造函数、`mdbCtx` context 注入
- `user_util.go` 包含 `Get` / `GetBy` / `All` / `IsExist` / `Paginate` 基础 CRUD 方法
- 时间字段默认使用 `datetime` 类型（而非 `timestamp`），兼容阿里云 RDS 严格模式

如果你已经有自定义 model/repository 实现，建议只在新实体创建初期使用此命令，之后按项目规范手工演化。

#### 3.2 `make migration`

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

支持的命名模式：

| 模式 | 示例 | 生成行为 |
|------|------|----------|
| `create_<table>_table` | `create_users_table` | 生成建表 migration，并自动生成 model/repository |
| `update_<table>_table` | `update_users_table` | 生成 `AutoMigrate` 模板，`down` 默认标记为不可逆 |
| `add_<column>_to_<table>_table` | `add_email_to_users_table` | 生成加列 migration |
| `drop_column_<column>_from_<table>_table` | `drop_column_email_from_users_table` | 生成删列 migration，`down` 默认标记为人工补全 |
| `drop_index_<name>_from_<table>_table` | `drop_index_email_from_users_table` | 生成删索引 migration，`down` 默认标记为人工补全 |

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

    "github.com/gtkit/migrate/v2/migration"
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

`create` 操作会同时在 `internal/models/` 和 `internal/repository/<model>/` 下生成默认代码（model 骨架 + repository CRUD）；如果 `internal/models/model.go`、`internal/models/doc.go`、`database/migrations/doc.go` 不存在，也会自动补齐。

注意：

- 生成器会尽量推断字段名和表名，但缩写字段如 `UserID`、`URL` 这类场景仍建议人工检查。
- `drop column` / `drop index` / 某些 `update` migration 默认会用 `migration.Irreversible(...)` 显式提示“需要人工写 down”，这是有意为之，避免生产误回滚。

### 3.3 覆盖默认生成目录

默认目录对齐 `go-gin-api` 风格：

- `internal/models`
- `internal/repository`
- `database/migrations`
- `database/ddl`

如需单次覆盖，可在命令行指定：

```bash
myapp make --model-dir internal/entities \
  --repository-dir internal/data/repositories \
  --migration-dir db/migrations \
  migration create_users_table
```

同理也可以覆盖 DDL 目录：

```bash
myapp make --ddl-dir db/ddl ddl --all
```

`make model`、`make migration`、`make ddl`、`make ddl diff` 都支持这些目录 flag，命令级 flag 优先级高于 `migrate.Setup(...)` 中的默认配置。

### 4. 执行迁移

```bash
# 预览待执行的迁移（dry-run，不实际执行）
myapp migrate pending

# 执行所有未迁移的文件
myapp migrate up

# 查看所有迁移状态
myapp migrate status

# 检查 migration 漂移与回滚风险
myapp migrate lint
myapp migrate lint --strict

# 回滚最后一批迁移
myapp migrate down

# 回滚所有迁移
myapp migrate reset

# 回滚所有后重新执行
myapp migrate refresh

# 删除所有表后重新执行（⚠️ 危险，会丢失数据）
myapp migrate fresh
```

各命令语义：

| 命令 | 说明 | 适合环境 |
|------|------|----------|
| `pending` | 仅列出将要执行的 migration，不实际执行 | 所有环境 |
| `up` | 执行尚未运行的 migration | 测试 / 预发 / 生产 |
| `down` | 回滚最后一个 batch | 测试 / 谨慎用于生产 |
| `reset` | 从后往前回滚所有 migration | 测试环境 |
| `refresh` | `reset` 后重新 `up` | 测试环境 |
| `fresh` | 删除库里所有表再跑 migration | 仅临时测试库 |
| `status` | 查看 migration 是否执行及 batch | 所有环境 |
| `lint` | 检查漂移、回滚风险、registry/file 不一致 | 所有环境，推荐 CI |

`up`、`down`、`reset`、`refresh`、`fresh` 都受 `WithTimeout(...)` 控制。

#### 4.1 `migrate lint`

基础用法：

```bash
myapp migrate lint
```

严格模式，把 warning 也视为失败：

```bash
myapp migrate lint --strict
```

跳过数据库已执行记录检查，只检查源码和磁盘：

```bash
myapp migrate lint --skip-db
```

lint 当前会检查：

- 磁盘上存在 migration 文件，但当前 binary 未注册
- binary 已注册 migration，但磁盘文件不存在
- migration 缺失 `up`
- migration 缺失 `down`
- migration 明确标记为 `Irreversible(...)`
- 多个 migration 共享相同时间戳前缀
- 数据库里已执行 migration，但源码/磁盘已经找不到

返回规则：

- 有 `error` 时返回非零
- `--strict` 下有 `warning` 也返回非零
- 适合直接接入 CI
### 5. 生成严格建表 DDL

先在 `Setup` 中注册你要输出 DDL 的模型：

```go
migrate.Setup(db,
    migrate.WithProjectName("myproject"),
    migrate.WithDDLModels(&models.User{}, &models.Order{}),
)
```

然后生成指定模型或全部模型的建表 DDL：

```bash
myapp make ddl user
myapp make ddl users
myapp make ddl --all

# 对比当前模型 DDL 与已提交 SQL 文件
myapp make ddl diff user
myapp make ddl diff --all
```

DDL 默认输出到 `database/ddl/`，文件名形如：

```text
database/ddl/create_users_table.sql
```

`make ddl` 会复用当前数据库方言的 GORM migrator，以 `dry-run` 方式捕获 SQL，因此输出更接近真实执行 SQL。若目标文件已存在，可加 `--force` 覆盖。

建议的使用方式：

1. 修改 GORM model
2. 执行 `make migration ...`
3. 执行 `make ddl user` 或 `make ddl --all`
4. 提交 migration + DDL 文件
5. 在 CI 中执行 `make ddl diff --all`

`make ddl diff` 会比较当前 strict DDL 和 `database/ddl/` 中已有文件。若存在漂移，会输出文本 diff 并返回非零退出码，适合接入 CI。

示例：

```bash
# 为单个模型生成 DDL
myapp make ddl user

# 为全部已注册模型生成 DDL
myapp make ddl --all

# 强制覆盖已有 SQL 文件
myapp make ddl --all --force

# 检查当前模型和已提交 SQL 是否一致
myapp make ddl diff --all
```

`make ddl diff` 的判断基准是：

- 左侧：磁盘中已有的 `database/ddl/*.sql`
- 右侧：当前代码里的 GORM model 经 strict dry-run 生成的 SQL

所以它特别适合下面这个场景：

- 你改了 model
- 忘了同步更新 DDL 文件
- CI 用 `make ddl diff --all` 直接拦住
### 6. 推荐工作流

#### 6.1 新增一张表

```bash
myapp make migration create_users_table
myapp make ddl users
myapp migrate lint
myapp migrate up
```

#### 6.2 修改现有表结构

```bash
myapp make migration add_email_to_users_table
myapp make ddl users
myapp make ddl diff users
myapp migrate lint --strict
```

#### 6.3 CI 建议

最常见的 CI 检查顺序：

```bash
go test ./...
go test -race ./...
myapp migrate lint --strict
myapp make ddl diff --all
```

如果 CI 有真实数据库，再额外加：

```bash
go test -tags=integration ./migration -run 'TestMigrator(MySQL|Postgres)Integration'
```

### 7. 命令输出示例

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

```bash
$ myapp migrate lint
[WARNING] 2026_03_24_120002_drop_email_from_users_table: migration declares manual down logic is required

Summary: 0 error(s), 1 warning(s)
```

## 集成测试

MySQL/Postgres 集成测试默认不参与普通 `go test`，需要显式指定 `integration` build tag 和数据库 DSN：

```bash
export MIGRATE_TEST_MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/migrate_test?parseTime=true'
export MIGRATE_TEST_POSTGRES_DSN='host=127.0.0.1 user=postgres password=postgres dbname=migrate_test sslmode=disable'

go test -tags=integration ./migration -run 'TestMigrator(MySQL|Postgres)Integration'
```

为了降低误操作风险，测试默认要求数据库名包含 `test`；如果你确实要对其他库运行，请显式设置 `MIGRATE_TEST_ALLOW_ANY_DB=1`。

如果你只想跑单个数据库：

```bash
go test -tags=integration ./migration -run TestMigratorMySQLIntegration
go test -tags=integration ./migration -run TestMigratorPostgresIntegration
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

// lint
report, err := m.Lint(ctx, migration.LintOptions{})
```

如果你是库调用方，推荐把 `Lint(...)` 用在：

- 本地开发时的 pre-release 检查
- 管理后台里的“发布前自检”
- CI 的 migration 验证步骤

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
