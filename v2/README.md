# migrate

基于 GORM + Cobra 的 Go 数据库迁移工具，支持 MySQL、PostgreSQL、SQLite。

> **方言说明**：迁移执行引擎（up/down/锁/记录表）完整支持三种数据库；`make migration` 生成的模板 SQL 当前为 **MySQL 语法**（反引号、`AFTER` 子句、`ALGORITHM`/`LOCK` 在线 DDL 子句），PostgreSQL/SQLite 项目生成后需按方言调整。`migrate lint` 的在线 DDL 规则仅对 MySQL 数据库生效。每个迁移的 Up/Down 与其账本记录在**同一事务**内执行，迁移函数里只能使用允许在事务块内运行的语句（PostgreSQL 的 `CREATE INDEX CONCURRENTLY` 等语句会因此报错）。

## 特性

- 迁移文件按时间戳排序，支持 up / down / reset / refresh / fresh
- Advisory Lock 防止多实例并发迁移冲突（MySQL `GET_LOCK`、PostgreSQL `pg_advisory_lock`）
- `pending` 命令预览待执行迁移（dry-run）
- `lint` 命令检查 migration 文件、registry 和已执行记录漂移
- 结构化日志接口，可注入 zap / zerolog 等
- Lock name 可配置，同一数据库多项目共存不冲突
- `make migration` 自动生成迁移文件和 model 脚手架；`create --from-model` 从已注册的 GORM model 生成结构快照，`add --type ...` 生成完整的加列 SQL，`add_index_*` 生成带在线 DDL 策略的加索引 SQL
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
| `migrate down --force [--step N]` | 回滚最后一批 migration，`--step N` 改为回滚最新 N 条（需 `--force`） | 紧急回滚 |
| `migrate down-to <version> --force` | 回滚到指定版本（需 `--force`，目标须已应用） | 回退到某版本 |
| `migrate reset --force` | 回滚全部 migration（需 `--force`） | 测试环境重置 |
| `migrate refresh --force` | 回滚全部再重放（需 `--force`） | 测试环境验证 |
| `migrate fresh --force` | 删库内所有表再重放 migration（需 `WithAllowFresh` 授权 + `--force`） | 仅测试环境 |
| `migrate status` | 查看运行状态 | 运维排查 |
| `migrate lint` | 检查文件、registry、数据库记录漂移 | CI / 发布前检查 |
| `migrate mark-applied --force [--to <version>]` | 把 pending 迁移记为已应用而不执行（baseline 存量库，需 `--force`，`--to` 目标须已注册） | 存量库纳管 |

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

    // 关键：副作用导入迁移包，让各迁移文件的 init() 注册进 registry。
    // migrate up 以编译进 binary 的 registry 为执行源；不导入则 registry 为空、up 会直接报错（fail-closed）。
    _ "yourapp/database/migrations"
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
| `WithProjectName` | 从 `go.mod` 解析 | 生成代码里的 module import 前缀；缺省时生成命令从执行目录向上查找 `go.mod` 读取 module path，找不到则报错不落盘 |
| `WithMigrationDir` | `database/migrations` | migration 文件目录 |
| `WithModelDir` | `internal/models` | model 生成目录 |
| `WithRepositoryDir` | `internal/repository` | repository 生成目录 |
| `WithDDLDir` | `database/ddl` | DDL SQL 输出目录 |
| `WithDDLModels` | 空 | 注册可用于 `make ddl` / `make ddl diff` 的模型 |
| `WithTimeout` | `5m` | `migrate` 命令执行超时 |
| `WithLockName` | MySQL 派生 / PostgreSQL `migrate_lock` | 分布式 advisory lock 名称。MySQL 缺省按 `migrate:<数据库名>:<账本表名>` 派生（超 64 字符截断并追加哈希），同一实例上不同库、不同账本表互不阻塞；显式锁名在 MySQL 上不得超过 64 字符，超长时迁移命令直接报错 |
| `WithLockTimeout` | `10s` | 获取迁移锁的最长等待时间 |
| `WithMigrationsTable` | `migrations` | 迁移记录表名（多项目共库时各用独立表；仅字母/数字/下划线、≤63 字符，空白或非法值 `Setup` 直接报错） |
| `WithAllowFresh` | 禁用 | 显式授权 `fresh`（删库内全部表）；仅本项目独占数据库时才应开启 |
| `WithAllowUnknownApplied` | 禁用 | 显式授权 `up`/`status`/`pending` 容忍"已应用但当前 binary 未注册"的迁移记录（逐条 Warn 后继续）；用于应用回滚窗口，`mark-applied` 与回滚命令不受影响 |
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
internal/repository/user/repository.go          # Repository 结构体 + New 构造函数
internal/repository/user/repository_util.go     # Get / GetBy / All / IsExist / Paginate
```

其中：

- 所有文件都 `writeSkipIfExists`：已存在则跳过、不覆盖，可以在已有实体上重复执行补齐缺失文件
- `model.go` 提供 `BaseID`（主键）与 `BaseTimeField`（创建/更新/软删除时间），`doc.go` 是包注释
- `user.go` 是 GORM model 骨架：嵌入 `BaseID` 与 `BaseTimeField`，含 `TableName`、`GetStringID`、基于标准库 `encoding/json` 的 `MarshalBinary` / `UnmarshalBinary`
- `repository.go` 定义 `Repository` 结构体、`New(db *gorm.DB)` 构造函数与 `mdbCtx` context 注入
- `repository_util.go` 提供 `Get(ctx, id) (model, found, err)`（未找到与库故障分开表达，不吞错）、`ExistsByID`、`All(ctx) ([]model, error)`、`CreateOrUpdate(ctx, entity, primaryKey, updateColumns)`
- 生成物只依赖标准库与 `gorm`，在只含 `gorm` 依赖的新项目里可直接 `go build`；分页等业务查询按项目需要自行添加
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

# 添加字段并给出完整列定义：生成可直接执行的 ADD COLUMN，不留 TODO
myapp make migration add_email_to_users_table --type 'VARCHAR(128)' --not-null --default "''" --comment '邮箱'

# 添加字段并指定位置（MySQL AFTER）；不给 --type 时列定义以 TODO 占位待补全
myapp make migration add_email_to_users_table --after phone

# 从 WithDDLModels 注册的、表名为 users 的 GORM model 生成结构快照（不生成 model/repository 脚手架）
myapp make migration create_users_table --from-model

# 添加索引（默认名 idx_users_email，自带在线 DDL 策略）
myapp make migration add_index_email_to_users_table

# 唯一复合索引
myapp make migration add_index_email_status_to_users_table --unique --columns email,status

# 修改字段类型（up 完整生成，down 留成型骨架待补全）
myapp make migration modify_email_of_users_table --type 'VARCHAR(255)' --not-null

# 删除字段；给出原列定义即可生成真实回滚
myapp make migration drop_column_avatar_from_users_table --type 'VARCHAR(255)'

# 删除索引；给出原索引列即可生成真实回滚
myapp make migration drop_index_email_from_users_table --columns email
```

支持的命名模式：

| 模式 | 示例 | 生成行为 |
|------|------|----------|
| `create_<table>_table` | `create_users_table` | 生成建表 migration（自包含快照 struct，基础字段 + `TODO`），并自动生成 model/repository |
| `create_<table>_table --from-model` | `create_users_table --from-model` | 从 `WithDDLModels` 中表名为 `users` 的 model 反射生成快照 struct 的全部导出字段与 tag（匿名嵌入展平、`gorm:"-"` 跳过），不含 `TODO`，不生成脚手架；找不到匹配 model 时报错不落盘 |
| `update_<table>_table` | `update_users_table` | 生成 up/down 各一段 raw `ALTER TABLE` 骨架，`TODO` 待补全 |
| `add_<column>_to_<table>_table` | `add_email_to_users_table` | 生成 **raw SQL** 加列 migration，列类型/约束以 `TODO` 占位待补全 |
| `add_<column>_to_<table>_table --type <T> [--not-null] [--default <expr>] [--comment <c>]` | `add_email_to_users_table --type 'VARCHAR(128)' --not-null --default "''" --comment '邮箱'` | 生成完整可执行的 `ADD COLUMN`，不含 `TODO`；`--not-null`/`--default`/`--comment` 必须与 `--type` 同时给出，否则报错 |
| `add_<column>_to_<table>_table --after <col>` | `add_email_to_users_table --after phone` | 在 `ADD COLUMN` 后追加 `AFTER <col>` 子句（MySQL 列定位），可与 `--type` 组合 |
| `add_index_<column>_to_<table>_table [--unique] [--index-name <n>] [--columns a,b]` | `add_index_email_to_users_table` | 生成 raw `ADD INDEX` migration，默认索引名 `idx_<表>_<列...>`、默认标注 `ALGORITHM`/`LOCK`；`down` 是对应的 `DROP INDEX`（可回滚，不是不可逆） |
| `modify_<column>_of_<table>_table --type <T> [...]` | `modify_email_of_users_table --type 'VARCHAR(255)'` | 生成 raw `MODIFY COLUMN` migration，up 由参数完整生成，`down` 是同形骨架并把旧定义留为 `TODO` |
| `drop_column_<column>_from_<table>_table [--type <T> ...]` | `drop_column_email_from_users_table --type 'VARCHAR(128)'` | 生成 raw `DROP COLUMN` migration；给出原列定义时 `down` 重建该列，否则标记为不可逆 |
| `drop_index_<column>_from_<table>_table [--columns a,b] [--unique]` | `drop_index_email_from_users_table --columns email` | 生成 raw `DROP INDEX` migration，索引名默认与 `add_index` 一致；给出原索引列时 `down` 重建该索引，否则标记为不可逆 |

> **迁移模板均自包含、显式、可审查**：都不 import 业务 model、不使用 `AutoMigrate`（避免随 model 演进漂移）。带 `TODO` 占位的模板需补全（大表建议标注 `ALGORITHM`/`LOCK` 在线 DDL 策略）后才能通过 `migrate lint`。
>
> 加列、删列、加索引、删索引这些**完整生成**的语句默认带 `ALGORITHM=INPLACE, LOCK=NONE`（已在 MySQL 8.0 上逐条验证可执行），默认产出即可通过 `migrate lint` 的在线 DDL 检查。`LOCK=NONE` 的含义是：MySQL 若无法在不阻塞写入的前提下完成变更，会**直接报错**而不是悄悄锁表。
>
> `modify_*` 与 `update_*` 是待补全骨架，**不预填**策略子句：`MODIFY COLUMN` 只有少数场景（如 VARCHAR 扩容）支持 `ALGORITHM=INPLACE`，缩短长度或跨类型转换都要求 `ALGORITHM=COPY`，预填一个多数情况下会报错的值比不填更糟。策略由你按实际变更决定，`migrate lint` 的 `missing_online_ddl` 会提醒。
>
> 表名、列名、索引名统一按标识符白名单校验（字母、数字、下划线，不以数字开头，不超过 64 字符），不合法时生成期直接报错、不落盘。以数字开头的表名虽然 MySQL 允许，但无法用于 `create` 的快照 struct 名，故一并拒绝；这类表请手写迁移。
>
> 迁移名里的 `_to_` / `_from_` / `_of_` 出现**多于一次**时无法判断哪一段是表名（列名可能自带分隔符如 `reply_to_id`，表名同样可能如 `order_to_shipment`），生成器直接报错，用 `--table <表名>` 显式指定即可：
>
> ```bash
> myapp make migration add_ref_to_order_to_shipment_table --table order_to_shipment
> myapp make migration add_reply_to_id_to_messages_table --table messages
> ```
>
> `--after` 仅对 `add_*` 模式生效，通过在 raw `ALTER TABLE ... ADD COLUMN` 后追加 `AFTER <col>` 实现。注意列的物理顺序在 MySQL 中仅影响展示，不影响功能。
>
> `--from-model` 只在生成时读取 model：生成后的迁移文件是唯一事实来源，之后 model 再变化也不会回写它。model 字段若使用了业务包内的自定义类型（如枚举 `models.Status`），快照会原样 import 该包并被 `migrate lint` 判为非自包含，此时把该字段改为基础类型（如 `int8`）即可。所有生成的 `.go` 文件都经过 gofmt。

执行后会在 `database/migrations/` 下生成形如 `2026_03_17_120000_create_users_table.go` 的文件。

不同 action 使用不同的 migration 模板：

| action | up 行为 | down 行为 |
|--------|---------|-----------|
| `create` | 快照 struct `CreateTable`（带存在性检查） | `DropTable` |
| `update` | raw `ALTER TABLE` 骨架（TODO 待补全） | raw 反向 `ALTER` 骨架（TODO 待补全） |
| `add`（列） | raw `ADD COLUMN`（带存在性检查；无 `--type` 时列定义为 TODO） | raw `DROP COLUMN`（带存在性检查） |
| `add_index` | raw `ADD INDEX`（带存在性检查） | raw `DROP INDEX`（带存在性检查） |
| `modify` | raw `MODIFY COLUMN`（无 `--type` 时列定义为 TODO） | raw `MODIFY COLUMN` 骨架，旧定义为 TODO |
| `drop`（表） | `DropTable`（带存在性检查） | 标记为不可逆，需人工补全 |
| `drop_column` | raw `DROP COLUMN`（带存在性检查） | 给出 `--type` 时重建该列，否则不可逆 |
| `drop_index` | raw `DROP INDEX`（带存在性检查） | 给出 `--columns` 时重建该索引，否则不可逆 |

示例（`create`）——迁移文件**自包含表结构快照，有意不引用业务 model**：业务 model 会随需求演进，而迁移必须锁定「创建当时」的结构；引用业务 model 的迁移会被 `migrate lint` 以 `non_self_contained` 判为 error：

```go
package migrations

import (
    "time"

    "gorm.io/gorm"

    "github.com/gtkit/migrate/v2/migration"
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
        // 幂等：表已存在则跳过（MySQL 半失败后重跑 up 可自愈）。
        if db.Migrator().HasTable("users") {
            return nil
        }
        return db.Migrator().CreateTable(&userV20260317120000{})
    }

    down := func(db *gorm.DB) error {
        return db.Migrator().DropTable("users")
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

# 回滚最后一批迁移（破坏性，需 --force）
myapp migrate down --force

# 回滚最新 2 条迁移（不按批次；N 必须为正整数）
myapp migrate down --step 2 --force

# 回滚到指定版本（回滚所有比它新的迁移，需 --force）
myapp migrate down-to 2026_03_17_120000_create_users_table --force

# 回滚所有迁移（破坏性，需 --force）
myapp migrate reset --force

# 回滚所有后重新执行（破坏性，需 --force）
myapp migrate refresh --force

# 删除所有表后重新执行（⚠️ 危险，会丢失数据；需 Setup 时 WithAllowFresh 授权 + --force）
myapp migrate fresh --force
```

> `down` / `down-to` / `reset` / `refresh` / `fresh` 会回滚或删除数据，必须显式加 `--force` 才执行，缺失时直接报错拒绝，避免误触丢数据。核心生产可在组装 CLI 时干脆不注册这些回滚命令。
>
> `fresh` 有双层保护：`--force` 只防误触命令；它还会删除库内**全部**用户表（含其他项目的表），默认禁用，必须在 `Setup` 时用 `WithAllowFresh()` 显式授权（仅限本项目独占的数据库），否则直接报错拒绝。
>
> `fresh` 的清理范围包含表与视图；**PostgreSQL 仅清理 `public` schema**，其他 schema 的对象不受影响（多 schema 项目需自行清理其余 schema）。
>
> `down-to <version>` 的目标必须是真实已应用的版本；`mark-applied --to <version>` 的目标必须是已注册的迁移名；`RollbackSteps` 的步数必须为正数——否则直接报错，不会误回滚全部或标记错误范围。

各命令语义：

| 命令 | 说明 | 适合环境 |
|------|------|----------|
| `pending` | 仅列出将要执行的 migration，不实际执行；只读，不加锁、不建账本表 | 所有环境 |
| `up` | 执行尚未运行的 migration | 测试 / 预发 / 生产 |
| `down` | 回滚最后一个 batch；`--step N` 改为回滚最新 N 条 | 测试 / 谨慎用于生产 |
| `reset` | 从后往前回滚所有 migration | 测试环境 |
| `refresh` | `reset` 后重新 `up` | 测试环境 |
| `fresh` | 删除库里所有表与视图再跑 migration（需 `WithAllowFresh` 授权；PostgreSQL 仅清理 `public`） | 仅临时测试库 |
| `status` | 查看 migration 是否执行、batch 与应用时间；只读，不加锁、不建账本表 | 所有环境 |
| `lint` | 检查漂移、回滚风险、registry/file 不一致 | 所有环境，推荐 CI |

`up`、`down`、`reset`、`refresh`、`fresh` 都受 `WithTimeout(...)` 控制：这个超时覆盖**整条命令**——等锁、每一个迁移的 DDL 执行与账本写入共用同一预算（默认 5 分钟）。超时触发时驱动会关闭连接中断正在执行的 DDL，MySQL 服务端会终止该语句，但客户端拿不到确定结果，需按下文「生产注意事项」人工核对。生产环境请按最长一次迁移的实际耗时设置 `WithTimeout`（大表 ALTER 建议放到 30 分钟以上，或拆成单独的迁移批次执行）。

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
- 迁移残留未补全的 `TODO` 占位（error）,`update_*` 与 `modify_*` 的骨架默认就带 `TODO`，补全后才能执行
- 迁移使用 `AutoMigrate`（error）
- 迁移非自包含：import 了标准库、gorm、migrate 包以外的第三方/业务包（如业务 model）（error）
- raw `ALTER TABLE` 缺 `ALGORITHM`/`LOCK` 在线 DDL 策略（warning）
- raw 危险 DDL：`DROP TABLE` / `DROP DATABASE` / `TRUNCATE`（warning）

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
### 6. Makefile 集成

推荐在项目中创建 `migrate.mk`，通过 `include migrate.mk` 引入到主 `Makefile`，统一使用 `make` 命令操作：

```makefile
# Makefile
include migrate.mk
```

`migrate.mk` 示例：

```makefile
MIGRATE_ENV = $(if $(ENV),$(ENV),dev)

# ─── migrate 命令 ───────────────────────────────────────

migrate:                ## 执行迁移
	go run . migrate up -c $(MIGRATE_ENV)

migrate-down:           ## 回滚最后一批（破坏性，需 --force）
	go run . migrate down --force -c $(MIGRATE_ENV)

migrate-status:         ## 查看迁移状态
	go run . migrate status -c $(MIGRATE_ENV)

migrate-reset:          ## 回滚所有迁移（破坏性，需 --force）
	go run . migrate reset --force -c $(MIGRATE_ENV)

migrate-refresh:        ## 回滚后重放所有迁移（破坏性，需 --force）
	go run . migrate refresh --force -c $(MIGRATE_ENV)

migrate-fresh:          ## 删表后重放（⚠️ 危险，需 --force）
	go run . migrate fresh --force -c $(MIGRATE_ENV)

migrate-pending:        ## 预览待执行迁移（dry-run）
	go run . migrate pending -c $(MIGRATE_ENV)

migrate-lint:           ## 检查迁移漂移和回滚风险
	go run . migrate lint -c $(MIGRATE_ENV)

migrate-lint-strict:    ## 严格模式（warning 也视为失败）
	go run . migrate lint --strict -c $(MIGRATE_ENV)

# ─── make 命令（代码生成） ──────────────────────────────

migration:              ## 生成 migration 文件: make migration create_users_table
	go run . make migration $(filter-out $@,$(MAKECMDGOALS)) -c dev

model:                  ## 生成 model + repository: make model user
	go run . make model $(filter-out $@,$(MAKECMDGOALS)) -c dev

ddl:                    ## 生成建表 DDL: make ddl user / make ddl --all
	go run . make ddl $(filter-out $@,$(MAKECMDGOALS)) -c dev

ddl-diff:               ## 对比 DDL 漂移: make ddl-diff user / make ddl-diff --all
	go run . make ddl diff $(filter-out $@,$(MAKECMDGOALS)) -c dev

%:
	@:
```

常用命令速查：

| Makefile 命令 | 等价于 | 作用 |
|---------------|--------|------|
| `make migrate` | `migrate up` | 执行所有未运行的迁移 |
| `make migrate-down` | `migrate down --force` | 回滚最后一批迁移 |
| `make migrate-status` | `migrate status` | 查看迁移状态 |
| `make migrate-pending` | `migrate pending` | 预览待执行迁移 |
| `make migrate-lint` | `migrate lint` | 检查漂移和回滚风险 |
| `make migrate-lint-strict` | `migrate lint --strict` | 严格模式，CI 推荐 |
| `make model user` | `make model user` | 生成 model + repository |
| `make migration create_users_table` | `make migration ...` | 生成 migration 文件 |
| `make ddl user` | `make ddl user` | 生成建表 DDL SQL |
| `make ddl-diff --all` | `make ddl diff --all` | 对比 schema 漂移 |

### 7. 详细使用指南：用结构体定义表的完整流程

这一节把从零接入到生产回滚的每一步串起来。所有命令在应用模块根目录执行，`myapp` 代指你的二进制。

#### 7.1 接入：注册 model，让生成器认识你的表

结构体是表结构的事实来源。先写 GORM model，再把它交给 `WithDDLModels`，`create --from-model` 与 `make ddl` 都从这里读取：

```go
// internal/models/user.go
package models

import (
    "time"

    "gorm.io/gorm"
)

type User struct {
    ID        int64          `gorm:"column:id;primaryKey;autoIncrement;comment:主键"`
    Name      string         `gorm:"column:name;type:varchar(64);not null;default:'';comment:姓名"`
    Email     string         `gorm:"column:email;type:varchar(128);not null;default:'';uniqueIndex;comment:邮箱"`
    Status    int8           `gorm:"column:status;not null;default:0;comment:状态"`
    CreatedAt time.Time      `gorm:"column:created_at;type:datetime;not null;index;comment:创建时间"`
    UpdatedAt time.Time      `gorm:"column:updated_at;type:datetime;not null;comment:更新时间"`
    DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;type:datetime;index;comment:删除时间"`
}

func (User) TableName() string { return "users" }
```

```go
// cmd/myapp/main.go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/gtkit/migrate/v2"
    "github.com/gtkit/migrate/v2/command"
    "github.com/spf13/cobra"
    "gorm.io/driver/mysql"
    "gorm.io/gorm"

    "myproject/internal/models"
    _ "myproject/database/migrations" // 副作用导入：迁移文件在 init() 里注册进 registry
)

func main() {
    db, err := gorm.Open(mysql.Open(os.Getenv("APP_MYSQL_DSN")), &gorm.Config{})
    if err != nil {
        log.Fatal(err)
    }

    if err := migrate.Setup(db,
        migrate.WithDDLModels(&models.User{}), // create --from-model 与 make ddl 的 model 来源
        migrate.WithTimeout(30*time.Minute),    // 覆盖整条命令：等锁 + 每条迁移的 DDL + 账本写入
        migrate.WithLockTimeout(60*time.Second),
    ); err != nil {
        log.Fatal(err)
    }

    root := &cobra.Command{Use: "myapp"}
    root.AddCommand(command.Commands()...)

    // Ctrl-C / SIGTERM 经 ExecuteContext 贯通到迁移执行，正在等锁或执行的命令会被中止.
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()
    if err := root.ExecuteContext(ctx); err != nil {
        os.Exit(1)
    }
}
```

`WithProjectName` 可以省略：生成命令会从执行目录向上查找 `go.mod` 读取 module path 作为 import 前缀。项目根目录没有 `go.mod` 又没配 `WithProjectName` 时，`make model` 与 `make migration create_*` 直接报错，不会写出带错误 import 的文件。

#### 7.2 新建一张表：`create --from-model`

```bash
myapp make migration create_users_table --from-model
```

生成器在 `WithDDLModels` 里找表名为 `users` 的 model，反射它的字段生成快照 struct，写出 `database/migrations/2026_09_20_120000_create_users_table.go`：

```go
package migrations

import (
	"time"

	"gorm.io/gorm"

	"github.com/gtkit/migrate/v2/migration"
)

// UserV20260920120000 是本迁移建表时的表结构快照。
//
// 有意不引用 internal/models 下的业务 model：业务 model 会随需求不断演进，
// 而迁移必须锁定它「创建当时」的结构，才能保证任何环境、任何时间执行本迁移
// 都得到完全一致的表。后续字段/索引变更请新建 add_/update_ 迁移，切勿修改本文件。
type UserV20260920120000 struct {
	ID        int64          `gorm:"column:id;primaryKey;autoIncrement;comment:主键"`
	Name      string         `gorm:"column:name;type:varchar(64);not null;default:'';comment:姓名"`
	Email     string         `gorm:"column:email;type:varchar(128);not null;default:'';uniqueIndex;comment:邮箱"`
	Status    int8           `gorm:"column:status;not null;default:0;comment:状态"`
	CreatedAt time.Time      `gorm:"column:created_at;type:datetime;not null;index;comment:创建时间"`
	UpdatedAt time.Time      `gorm:"column:updated_at;type:datetime;not null;comment:更新时间"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;type:datetime;index;comment:删除时间"`
}

// TableName 锁定本迁移操作的表名。
func (UserV20260920120000) TableName() string {
	return "users"
}

func init() {
	up := func(db *gorm.DB) error {
		// 幂等：表已存在则跳过（MySQL DDL 已提交但记录未写的半失败状态下，重跑 up 可自愈）。
		if db.Migrator().HasTable("users") {
			return nil
		}
		return db.Migrator().CreateTable(&UserV20260920120000{})
	}

	down := func(db *gorm.DB) error {
		return db.Migrator().DropTable("users")
	}

	migration.Add("2026_09_20_120000_create_users_table", up, down)
}
```

要点：

- 快照 struct 是 model **此刻**的复制品，之后 model 再改也不会回写这个文件。表结构变更一律新建迁移。
- 反射规则：只取导出字段；匿名嵌入的 struct（如自定义 `BaseModel`、`gorm.Model`）展平成一级字段；`gorm:"-"` 跳过；tag 原样保留；`[]byte` 按惯例渲染。实现了 `driver.Valuer` 的匿名嵌入（如 `gorm.DeletedAt`）视为单列不展平。
- 迁移名里的 `<table>` 必须与 model 的实际表名一致（`TableName()` 或命名策略得出的），否则报错并列出已注册的表名，不落盘。
- model 字段若用了业务包内的自定义类型（如 `models.Status` 枚举），快照会 import 该包，`migrate lint` 会以 `non_self_contained` 报 error。把该字段改为底层基础类型（如 `int8`）即可。
- 不带 `--from-model` 时生成基础字段 + `TODO` 占位，并同时生成 model/repository 脚手架，适合从零开始一个新实体。

生成后按顺序执行：

```bash
myapp make ddl users          # 可选：落盘 database/ddl/create_users_table.sql 供审查
myapp migrate lint --strict   # 文件、registry、账本三方一致，无 TODO、无 AutoMigrate、自包含
myapp migrate pending         # 只读预览：本次 up 会执行哪些迁移
myapp migrate up
myapp migrate status
```

#### 7.3 给已有表加字段：`add --type`

```bash
myapp make migration add_phone_to_users_table \
    --type 'VARCHAR(32)' --not-null --default "''" --comment '手机号' --after email
```

生成的 up/down 是一对显式 raw SQL，不留 `TODO`：

```go
func init() {
	up := func(db *gorm.DB) error {
		// 幂等：列已存在则跳过，重复执行无副作用。
		if db.Migrator().HasColumn("users", "phone") {
			return nil
		}
		return db.Exec("ALTER TABLE `users` ADD COLUMN `phone` VARCHAR(32) NOT NULL DEFAULT '' COMMENT '手机号' AFTER `email`").Error
	}

	down := func(db *gorm.DB) error {
		if !db.Migrator().HasColumn("users", "phone") {
			return nil
		}
		return db.Exec("ALTER TABLE `users` DROP COLUMN `phone`").Error
	}

	migration.Add("2026_09_20_130000_add_phone_to_users_table", up, down)
}
```

- `--not-null`、`--default`、`--comment` 必须与 `--type` 同时给出，否则报错不落盘。`--default` 原样拼入 SQL，字符串默认值自己带引号（`"''"`、`"'active'"`），数值与表达式直接写（`0`、`CURRENT_TIMESTAMP`）。`--comment` 里的单引号会自动转义。
- 不给 `--type` 时列定义以 `/* TODO: 列定义 */` 占位，`migrate lint` 会以 `unfilled_placeholder` 拦住，补全前 `up` 不会通过 lint。
- 大表加列请在 SQL 末尾补 `, ALGORITHM=INPLACE, LOCK=NONE`（MySQL 8.0 加列多数场景可用 `ALGORITHM=INSTANT`）。`migrate lint` 对缺在线 DDL 策略的 `ALTER TABLE` 报 warning，`--strict` 下视为失败。
- 同步更新业务 model 加上 `Phone` 字段，再 `myapp make ddl users` 与 `myapp make ddl diff users` 确认 model 与落盘 DDL 一致。

#### 7.4 加索引：`add_index`

```bash
# 单列索引，索引名默认 idx_users_email
myapp make migration add_index_email_to_users_table

# 唯一索引
myapp make migration add_index_email_to_users_table --unique

# 复合索引：列名写进迁移名保持文件自解释，--columns 给出实际列
myapp make migration add_index_email_status_to_users_table --columns email,status

# 自定义索引名
myapp make migration add_index_email_to_users_table --index-name uk_users_email
```

生成的 up/down 成对且可回滚：

```go
func init() {
	up := func(db *gorm.DB) error {
		// 幂等：索引已存在则跳过，重复执行无副作用。
		if db.Migrator().HasIndex("users", "idx_users_email") {
			return nil
		}
		return db.Exec("ALTER TABLE `users` ADD INDEX `idx_users_email` (`email`), ALGORITHM=INPLACE, LOCK=NONE").Error
	}

	down := func(db *gorm.DB) error {
		if !db.Migrator().HasIndex("users", "idx_users_email") {
			return nil
		}
		return db.Exec("ALTER TABLE `users` DROP INDEX `idx_users_email`, ALGORITHM=INPLACE, LOCK=NONE").Error
	}

	migration.Add("2026_09_20_140000_add_index_email_to_users_table", up, down)
}
```

要点：

- 默认索引名 `idx_<表>_<列...>` 与 GORM 的默认命名策略一致：从 model tag 建表产出的索引名和这里对得上，`--index-name` 可覆盖。索引名超过 64 字符时生成期直接报错。
- up 与 down 都默认标注 `ALGORITHM=INPLACE, LOCK=NONE`。`LOCK=NONE` 的意义是：MySQL 若无法在不阻塞写入的前提下建这个索引，会**直接报错**而不是悄悄锁表。生产上这是更安全的默认；确实需要接受锁表时，改生成的 SQL 即可。
- 加索引天然可逆，`down` 是真正的 `DROP INDEX`，不像删索引/删表那样标记为 `Irreversible`。
- `--unique` 建唯一索引；表中已有重复值时 `up` 会失败，这是预期行为，先清理数据再执行。
- 写错形态会直接报错而不是生成错东西：`add_unique_index_*` 提示改用 `--unique`；给加列迁移传 `--unique`、给加索引迁移传 `--type` 都会报错。

#### 7.5 改列类型：`modify`

```bash
myapp make migration modify_email_of_users_table --type 'VARCHAR(255)' --not-null --comment '邮箱'
```

up 由参数完整生成，down 是同形的 `MODIFY COLUMN` 骨架，把**变更前**的列定义留成 `TODO`：

```go
	up := func(db *gorm.DB) error {
		return db.Exec("ALTER TABLE `users` MODIFY COLUMN `email` VARCHAR(255) NOT NULL COMMENT '邮箱', ALGORITHM=INPLACE, LOCK=NONE").Error
	}

	down := func(db *gorm.DB) error {
		// TODO: 填入本迁移执行前 `email` 的完整定义（MODIFY COLUMN 整体替换定义，
		// 必须写全类型、约束与注释）；确实无法还原时改用 migration.Irreversible 标记为不可逆。
		return db.Exec("ALTER TABLE `users` MODIFY COLUMN `email` /* TODO: 变更前的列定义 */, ALGORITHM=INPLACE, LOCK=NONE").Error
	}
```

down 之所以不自动生成：`MODIFY COLUMN` 是**整体替换**列定义，工具不知道变更前那一版长什么样。留成骨架加 `TODO` 意味着 `migrate lint` 会拦住未补全的迁移，等于强制你在合并前写清楚怎么退回去,这恰恰是改列最该被强制的一步。

改列的 SQL **不预填在线 DDL 策略**，这是实测后的决定。MySQL 8.0 上只有少数改列能走 `ALGORITHM=INPLACE`：

| 变更 | `ALGORITHM=INPLACE` |
|------|---------------------|
| `VARCHAR(128)` → `VARCHAR(255)` | 可用 |
| `VARCHAR(128)` → `VARCHAR(32)` | 报错 1846，要求 `COPY` |
| `VARCHAR(128)` → `TEXT` | 报错 1846，要求 `COPY` |
| `INT` → `BIGINT` | 报错 1846，要求 `COPY` |

预填 `INPLACE` 会让多数改列迁移直接执行失败，所以生成器把策略留给你：确认后在语句末尾补上 `, ALGORITHM=INPLACE, LOCK=NONE`，或对大表改走 gh-ost / pt-online-schema-change。补全前 `migrate lint` 会以 `missing_online_ddl` 提醒。

#### 7.6 删除类变更与它们的可逆性

| 需求 | 命令 | down |
|------|------|------|
| 删列 | `drop_column_avatar_from_users_table --type 'VARCHAR(255)' ...` | 给出原列定义则重建该列，否则不可逆 |
| 删索引 | `drop_index_email_from_users_table --columns email [--unique]` | 给出原索引列则重建该索引，否则不可逆 |
| 删表 | `drop_users_table` | 始终不可逆，需人工写重建逻辑 |
| 其他变更 | `update_users_table` | up/down 各一段 raw `ALTER TABLE` 骨架，`TODO` 待补全 |

要点：

- 删列的 down 重建的是**列结构，被删除的数据不会回来**，生成的文件里也写明了这一点。需要保住数据请先备份或改用 expand-contract。
- 删索引不承载数据，给出原索引定义后 down 是完全还原的。索引默认名与 `add_index` 一致，一来一回对得上。
- 删除类迁移不给定义时标记为 `Irreversible`：回滚触及它时会整体拒绝并停在它之前，不会回滚一半。
- 删列与删索引的参数就是创建时那一套（`--type` 系列 / `--columns` 与 `--unique`），照抄即可。

#### 7.7 发布到生产

1. 合并前跑 `myapp migrate lint --strict`，确认新迁移的时间戳前缀大于主干已有的最大值。
2. 对目标库做可恢复备份，在预发库按同一份代码执行 `myapp migrate up`。
3. 以**单独的迁移 Job** 执行 `myapp migrate up`，成功后再滚动业务实例。不要让每个实例启动时各跑一次：锁能挡住并发，但其余实例会在 `WithLockTimeout` 后报错退出。
4. Job 结束后 `myapp migrate status` 确认没有 Pending。`status` 与 `pending` 是只读命令，不加锁、不建账本表，可以随时对生产库执行。
5. 需要新旧版本共存的变更走 expand-contract：先加可空列或新表并发布兼容代码，回填数据，最后在后续版本删旧结构。

如果服务确实要在启动期调用 `Up`，加 `WithAllowUnknownApplied()`：应用回滚到旧版本时，旧 binary 对账本里"新版本写入、自己未注册"的记录记 Warn 后继续，不会启动失败。`mark-applied` 与所有回滚命令不受该选项影响。

#### 7.8 回滚与排障

```bash
# 回滚最后一批（同一次 up 执行的所有迁移）
myapp migrate down --force

# 只回滚最新 2 条，不按批次
myapp migrate down --step 2 --force

# 回滚到某个版本之后（不含该版本）
myapp migrate down-to 2026_09_20_120000_create_users_table --force

# 把存量库的历史表结构纳入账本而不执行 SQL
myapp migrate mark-applied --to 2026_09_20_120000_create_users_table --force
```

- 回滚前先 `status` 看清批次与应用时间。回滚遇到 `Irreversible`、registry 里找不到的记录、重复注册名时整体拒绝，不会回滚一半。
- MySQL 的 DDL 隐式提交：迁移中途失败留下"结构已变、记录未写"时，先人工核对真实表结构。生成模板的 up 都带存在性检查，单条 DDL 的迁移直接重跑 `up` 即可自愈；多条 DDL 的迁移手工补齐或回退后再重跑。
- `up` 日志的 `migrated ... duration=` 字段是大表 DDL 耗时的第一手数据，`WithTimeout` 按它来定。
- 程序化调用时用 `errors.Is` 区分 `migration.ErrLockNotAcquired`（另一实例在迁移，可重试）、`ErrRegistryDrift`（账本与 binary 不一致，需人工介入）、`ErrNoMigrations`（漏 import 迁移包）、`ErrDuplicateRegistration`（两个包注册了同名迁移）。

#### 7.9 多实例与多项目

- MySQL 默认锁名 `migrate:<数据库名>:<账本表名>`：同一实例上不同数据库、不同账本表的迁移互不阻塞。只有多个项目共用同一批表、需要串行化 DDL 时，才显式给它们配相同的 `WithLockName`。
- 多项目共库时各配独立的 `WithMigrationsTable`，账本与漂移校验互不干扰。细节见下文「多项目共用数据库」。
- 显式锁名在 MySQL 上不得超过 64 字符，超长时所有迁移命令直接报错。

#### 7.10 CI 建议

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

### 8. 命令输出示例

```bash
$ myapp migrate pending
Pending migrations (2):
  1. 2026_03_17_120000_create_users_table
  2. 2026_03_17_120100_create_orders_table

Run 'migrate up' to execute these migrations.
```

```bash
$ myapp migrate up
Running 2 migration(s)...
[INFO]  migrating                                          file=2026_03_17_120000_create_users_table batch=1
[INFO]  migrated                                           file=2026_03_17_120000_create_users_table duration=42ms
[INFO]  migrating                                          file=2026_03_17_120100_create_orders_table batch=1
[INFO]  migrated                                           file=2026_03_17_120100_create_orders_table duration=37ms
Migrations completed.
```

```bash
$ myapp migrate status
Migration Status:
--------------------------------------------------
  2026_03_17_120000_create_users_table              Ran (batch 1, 2026-03-17 12:00:03)
  2026_03_17_120100_create_orders_table              Ran (batch 1, 2026-03-17 12:00:03)
  2026_03_18_090000_add_email_to_users_table         Pending
```

```bash
$ myapp migrate lint
[WARNING] 2026_03_24_120002_drop_email_from_users_table: migration declares manual down logic is required

Summary: 0 error(s), 1 warning(s)
```

## 生产注意事项：MySQL 迁移的原子性与故障恢复

MySQL 的 `CREATE TABLE`/`ALTER TABLE` 等 DDL 会**隐式提交、无法回滚**（PostgreSQL、SQLite 支持事务型 DDL，本工具会把 DDL 与迁移记录放同一事务、失败整体回滚；MySQL 属其固有限制）。这带来两点必须知晓的行为，本工具**不会也无法**替 MySQL 消除：

- **迁移不是原子的**：一个迁移里若有多条 DDL，执行到中途失败时，前面的 DDL 已经提交、留下「半张表」；DDL 提交后、写迁移记录前若进程崩溃或超时，也会出现「结构已变更但记录未写」。
- **故障需人工恢复**：出现上述情况时，请**先人工核对真实表结构**，再决定：把该迁移手工补完（并用 `mark-applied` 将其记入），或把已生效的 DDL 手工回退后重跑。工具不会自动修复半成品。生成模板的 up 都带存在性检查（`create` 查 `HasTable`，`add` 查 `HasColumn`，`drop_*` 查目标是否存在），单条 DDL 的迁移在「DDL 已提交、记录未写」后直接重跑 `up` 即可自愈。

- **应用回滚窗口**：`up`/`status`/`pending` 默认对"已应用但当前 binary 未注册"的迁移记录 fail-closed 报错——新版本已写入账本后回滚到旧版本 binary，旧 binary 的 `up` 会失败。若服务在启动期调用 `Up`，请在 `Setup` 时加 `WithAllowUnknownApplied()`：旧 binary 对每条未知记录记 Warn 后继续执行自己已注册的迁移；`mark-applied` 与所有回滚命令不受该选项影响，仍严格拒绝。

降低风险的实践：

- **一个迁移只做一件事**，MySQL 迁移尽量写成**单条、幂等**的 DDL（生成模板已带 `HasColumn`/`HasTable` 存在性检查）。
- 大表结构变更走 gh-ost / pt-online-schema-change，或在 raw SQL 里标注 `ALGORITHM`/`LOCK`（`migrate lint` 会对缺失项告警）。
- 发布前先 `migrate lint` + `migrate pending` 人工确认；破坏性命令（`down`/`down-to`/`reset`/`refresh`/`fresh`）已强制 `--force`，核心生产建议在组装 CLI 时干脆不注册它们。

> 说明：本工具的迁移记录表沿用业界主流的极简结构（`id/migration/batch/created_at`，与 Laravel/Rails 一致），**未引入 dirty/checksum 状态**——这是有意的取舍，代价是 MySQL 半失败的恢复靠人工，如上所述。

## 集成测试

MySQL/Postgres 集成测试默认不参与普通 `go test`，需要显式指定 `integration` build tag 和数据库 DSN：

```bash
export MIGRATE_TEST_MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/migrate_test?parseTime=true'
export MIGRATE_TEST_POSTGRES_DSN='host=127.0.0.1 user=postgres password=postgres dbname=migrate_test sslmode=disable'

go test -tags=integration ./migration -run 'TestMigratorMySQL|TestMigratorPostgres'
```

MySQL 侧覆盖：端到端迁移/回滚、`GET_LOCK` 竞争与超时（`TestMigratorMySQLLockContention`）、`fresh` 删表的外键检查同连接处理（`TestMigratorMySQLForeignKeyChecksRestored`）。

为了降低误操作风险，测试默认要求数据库名包含 `test`；如果你确实要对其他库运行，请显式设置 `MIGRATE_TEST_ALLOW_ANY_DB=1`。

如果你只想跑单个数据库：

```bash
go test -tags=integration ./migration -run 'TestMigratorMySQL'
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

签名兼容 `zap.SugaredLogger` 的 key-value 风格。

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

### 静默日志（测试场景）

```go
migrate.Setup(db, migrate.WithLogger(&migration.NopLogger{}))
```

## 多项目共用数据库

当多个服务共享同一个数据库时，规则分两条：**迁移记录表必须按项目隔离**；**迁移锁按实际 DDL 资源边界选择**——项目间无共享表/跨项目外键时用独立锁名（互不阻塞），存在共享 DDL 资源时给相关项目配相同锁名（串行化，见下文）。

MySQL 的默认锁名已经按 `migrate:<数据库名>:<账本表名>` 派生：各项目只要配了独立的 `WithMigrationsTable`，默认就拿到独立的锁；同一 MySQL 实例上不同数据库的迁移也天然互不阻塞（`GET_LOCK` 的命名空间是整个实例全局的，固定锁名会让它们互相等待）。只有"存在共享 DDL 资源、需要串行化"的场景才需要显式 `WithLockName` 配同一个名字。

无共享资源的典型配置（独立锁）：

```go
// user-service
migrate.Setup(db,
    migrate.WithLockName("user_svc_migrate"),
    migrate.WithMigrationsTable("migrations_user_svc"),
)

// order-service
migrate.Setup(db,
    migrate.WithLockName("order_svc_migrate"),
    migrate.WithMigrationsTable("migrations_order_svc"),
)
```

- `WithLockName`：不同 lock name 生成不同的 advisory lock key，各项目迁移互不阻塞；仅适用于项目间无共享 DDL 资源的场景。
- `WithMigrationsTable`：各项目使用独立的迁移记录表（默认 `migrations`）。**必须配置**——若共用同一张记录表，项目 B 的漂移校验会把项目 A 的记录判为"已应用但未注册"而拒绝执行。表名仅允许字母、数字与下划线且不超过 63 字符（不支持 `schema.table`）；空白或非法表名 `Setup` 直接报错，绝不静默回退默认账本。

编程式调用使用 `migration.WithMigrationsTable(...)` MigratorOption，效果相同。

共库时的额外约束：

- **不要授权 `fresh`**：`fresh` 会删除库内**全部**用户表（包括其他项目的表和账本），默认禁用；共库数据库上绝不要配置 `WithAllowFresh`，仅本项目独占的数据库才可授权。
- **跨项目外键 / 共享表需要串行化**：独立锁名意味着两个项目可以并发执行各自的 DDL。若项目间存在跨项目外键或共享表，请给相关项目配置**相同的** `WithLockName`，用同一把锁把迁移串行化。

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

失败路径可用 `errors.Is` 判定，便于调用方区分处置：

| sentinel | 含义 |
|----------|------|
| `migration.ErrNoMigrations` | registry 为空（通常漏 import 迁移包） |
| `migration.ErrDuplicateRegistration` | 同名迁移被注册多次 |
| `migration.ErrRegistryDrift` | 账本中存在当前 binary 未注册的已应用迁移（含 `mark-applied` 与回滚遇到未注册记录） |
| `migration.ErrLockNotAcquired` | 等待时间内未获得迁移锁，另一实例可能正在迁移 |
| `migration.ErrLintFailed` | lint 存在 error 级问题 |
| `migration.ErrIrreversible` | 迁移声明不可回滚 |

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
