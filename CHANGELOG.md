# Changelog

本项目所有值得记录的变更都会写入本文件。

格式遵循 [Keep a Changelog 1.1.0](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本 2.0.0](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [2.4.0] - 2026-09-20

### Added

- 新增迁移形态 `make migration modify_<列>_of_<表>_table`：up 由 `--type`/`--not-null`/`--default`/`--comment` 生成完整的 `MODIFY COLUMN`，down 生成同形骨架并把变更前的列定义留为 `TODO`（`MODIFY COLUMN` 整体替换定义，工具无从得知旧定义；由 lint 强制补全）。
- `drop_column_*` 给出 `--type` 等列定义参数时，down 生成重建该列的 `ADD COLUMN`，并在文件中注明只重建结构、数据不会恢复；`drop_index_*` 给出 `--columns`（可配 `--unique`）时，down 生成重建该索引的 `ADD INDEX`。参数不给时仍标记为 `Irreversible`，行为不变。此前删列/删索引恒为不可逆，同批次里只要有一条就会让整批 `down` 被预检拒绝。
- 新增迁移形态 `make migration add_index_<列>_to_<表>_table`：生成带 `HasIndex` 幂等守卫的 `ADD INDEX`/`DROP INDEX` 成对实现，默认索引名 `idx_<表>_<列...>`（与 GORM 默认命名策略一致），up 与 down 都默认标注 `ALGORITHM=INPLACE, LOCK=NONE`；加索引天然可逆，`down` 是真正的 `DROP INDEX` 而非 `Irreversible`。新增 flag `--unique`（唯一索引）、`--index-name`（覆盖默认名，超过 MySQL 64 字符上限时生成期报错）、`--columns`（复合索引列表，覆盖从迁移名推出的单列）。

### Changed

- `drop_index_*` 的默认索引名改为与 `add_index_*` 一致的 `idx_<表>_<列>`（此前是裸列名并留 `TODO` 待人工确认），支持 `--index-name` 覆盖；生成物不再含 `TODO`。
- 加列、删列、加索引、删索引这些完整生成的语句统一带 `ALGORITHM=INPLACE, LOCK=NONE`（此前只有加索引带），生成物默认即可通过 `migrate lint` 的 `missing_online_ddl` 检查。`modify_*` 与 `update_*` 这类待补全骨架**不预填**策略：实测 MySQL 8.0 上 `MODIFY COLUMN` 仅少数场景（如 VARCHAR 扩容）支持 `ALGORITHM=INPLACE`，缩短长度与跨类型转换都会报 1846 要求 `COPY`，预填会让多数改列迁移直接执行失败；策略交由使用者决定，由 lint 提醒。
- `update_*` 与 `modify_*` 模板注释里的 `migration.Irreversible(...)` 改为不带括号的表述，消除 lint 对注释文本的 `irreversible_migration` 误报。
- **⚠ 解析行为修正** 表名、列名、索引名统一按标识符白名单校验（字母、数字、下划线，不以数字开头，长度不超过 64），非法时生成期报错且不落盘。此前 `add_ev il_to_users_table`、`create_users; DROP TABLE x_table` 这类输入会静默生成编译不过或执行必错的文件。以数字开头的表名 MySQL 本身允许，但无法用于 `create` 的快照 struct 名，一并拒绝。
- **⚠ 解析行为修正** 迁移名里的 `_to_` / `_from_` / `_of_` 出现多于一次时改为报错，并指引用新增的 `--table <表名>` 显式消歧。此前按首次出现切分，`add_reply_to_id_to_messages_table` 会被静默切成「给 `id_to_messages` 表加 `reply` 列」；而按最后一次切分同样会让 `add_ref_to_order_to_shipment_table` 被切成「给 `shipment` 表加 `ref_to_order` 列」——列名与表名都可能自带分隔符，任何一种猜法都会在另一种场景下静默出错，因此改为 fail-closed。
- 新增 `--table` flag：迁移名分隔符歧义时显式指定表名，生成器按后缀剥离得到目标名，不再依赖猜测。
- **⚠ 解析行为修正** `add_index_*` 不再被解析为"加一个名叫 `index_xxx` 的列"。此前 `add_index_email_to_users_table` 会静默生成添加 `index_email` 列的迁移且不报错，与本项目 fail-closed 的原则冲突。真需要添加名字以 `index_` 开头的列时，请改用其他列名或直接写 `update_*` 迁移。
- **⚠ 解析行为修正** `add_unique_index_*` 直接报错并指引改用 `add_index_* --unique`（此前同样被静默当作列名）；`add` 形态解析出的列名为空时报错。
- flag 与迁移形态不匹配时报错而非静默忽略：加列迁移传 `--unique`/`--index-name`/`--columns`，或加索引迁移传 `--type`/`--not-null`/`--default`/`--comment`/`--after`，都会被拒绝且不生成文件。

## [2.3.0] - 2026-09-20

### Added

- `make migration create_<table>_table --from-model`：从 `WithDDLModels` 中表名匹配的 GORM model 反射生成快照 struct 的全部导出字段、tag 与 import（匿名嵌入展平、`gorm:"-"` 跳过、`[]byte` 按惯例渲染），不含 `TODO`，不生成 model/repository 脚手架；找不到匹配 model 或表名不一致时报错且不落盘。
- `make migration add_<col>_to_<table>_table` 新增 `--type` / `--not-null` / `--default` / `--comment`：给出 `--type` 时生成完整可执行的 `ADD COLUMN`（可与 `--after` 组合），不含 `TODO`；三个修饰参数在无 `--type` 时报错。
- CLI `migrate down --step N`：回滚最新 N 条而非最后一批，N 必须为正整数。
- `migrate status` 显示已应用迁移的应用时间；`MigrationStatus` 新增 `AppliedAt` 字段。
- `up` / 回滚日志的 `migrated` / `rolled back` 带每条迁移的 `duration`。
- 新增 sentinel 错误 `migration.ErrNoMigrations`、`ErrDuplicateRegistration`、`ErrRegistryDrift`、`ErrLockNotAcquired`，对应失败路径以 `%w` 包装，可 `errors.Is` 判定。
- 生成的 `.go` 文件统一经 gofmt 格式化，格式化失败视为生成失败。
- 回归测试：默认锁名派生（单测 + 真实 MySQL 集成）、MySQL 显式锁名长度 fail-closed（离线方言构造，不连库）、只读诊断不建表、耗时日志字段、四个 sentinel 的 `errors.Is`、CLI `down --step` 与 `status` 时间输出、`--from-model` 反射规则与不落盘、`add` 列定义参数与校验、`go.mod` 项目名解析与缺失 fail-closed。
- README 新增「详细使用指南」：从注册 model、`create --from-model`、`add --type`、其他结构变更、发布到生产、回滚与排障、多实例与多项目的完整流程与生成物示例。

### Changed

- **⚠ 破坏性变更（默认值）** MySQL 默认迁移锁名由固定的 `migrate_lock` 改为按 `migrate:<数据库名>:<账本表名>` 派生（超 64 字符截断并追加 FNV-64a 哈希后缀）。`GET_LOCK` 的命名空间是整个 MySQL 实例全局的，固定锁名会让同实例不同数据库、不同账本表的迁移互相等锁并在默认 10 秒后报错。迁移说明：滚动升级窗口内新旧 binary 锁名不同、互斥失效，需要跨版本互斥的用户请在升级前后显式配置相同的 `WithLockName`。PostgreSQL 默认锁名不变。
- **⚠ 破坏性变更** MySQL 上显式 `WithLockName` 超过 64 字符时所有迁移入口 fail-closed 报错（此前直到 `GET_LOCK` 执行才由 MySQL 报错）。
- **⚠ 破坏性变更（默认值）** `WithProjectName` 缺省值由字面量 `project_name` 改为"从执行目录向上查找 `go.mod` 解析 module path"；找不到 `go.mod` 时 `make model` 与 `make migration create_*`（非 `--from-model`）报错且不落盘，不再生成带错误 import 的脚手架。
- `Status` / `Pending` / `IsUpToDate` 改为只读：不获取锁、不创建账本表，账本表不存在时视为无已应用记录（可在只读账号或新库上运行）。`up` 等写命令仍按原样创建账本表。
- **⚠ 脚手架形态变更** `make model`（及 `make migration create_*` 附带的脚手架）生成物只依赖标准库与 `gorm`：去掉对 `<module>/internal/pkg/paginator` 与 `github.com/gtkit/json` 的 import（此前在新项目里编译不过），JSON 改用 `encoding/json`；仓储方法改为 `Get(ctx, id) (model, found, err)`、`ExistsByID`、`All(ctx) ([]model, error)`、`CreateOrUpdate`，删除吞错误的 `Get`/`All` 旧签名与按调用方传入字段名拼接 SQL 的 `GetBy`/`IsExist`，以及依赖 paginator 的 `ListPaging`/`Paginate`；`GetStringID` 改用 `strconv.FormatInt`。已存在的文件不会被覆盖。
- **⚠ 脚手架形态变更** `make cmd` 模板改为最小可编译形态：`RunE` 签名、无演示输出、不再在 `init` 里假设存在 `rootCmd` 自动注册，生成后由用户显式 `rootCmd.AddCommand`。
- `make` 包精简：删除无模板引用的 `Model` 字段与对应替换项、只为去重而存在的辅助函数、不可达分支与单调用点的包装函数；`make ddl` 与 `--from-model` 共用同一套 model 解析（`db` 为 nil 时用 GORM 默认命名策略）。行为不变。

### Fixed

- 修复 model 包与 migrations 包 `doc.go` 模板的包注释位置（此前写在 `package` 子句之后，不是有效的包文档）。

### Migration Notes

- MySQL 用户升级后默认锁名改变：滚动升级窗口内新旧版本互斥失效。需要跨版本互斥的，在升级前后给新旧 binary 显式配置相同的 `WithLockName`；无此需求的无需改动，各库各账本表自动获得独立锁。
- 依赖 `WithProjectName` 默认值 `project_name` 的生成流程会开始报错，属预期修正：在项目根目录（含 `go.mod`）执行生成命令，或显式传 `WithProjectName`。
- 已生成的 model / repository / cmd 文件不会被覆盖，升级不影响存量代码；新生成的脚手架方法集与旧版不同，混用时按新签名调用。

## [2.2.2] - 2026-09-20

### Added

- 回归测试：`WithAllowUnknownApplied` 的作用边界（migration 包单测 + CLI 端到端，覆盖默认 fail-closed、授权后 `pending`/`up`/`status`/`IsUpToDate` 放行并记 Warn、未知记录原样保留、`mark-applied` 与回滚仍拒绝）；`create` 模板的 `HasTable` 守卫（`make` 生成内容断言 + 真实 MySQL 集成测试验证 DDL 已提交但记录未写时重跑 `up` 自愈）。

## [2.2.1] - 2026-09-20

### Fixed

- README 方言说明补充使用约束：每个迁移的 Up/Down 与账本记录在同一事务内执行，迁移函数里只能使用允许在事务块内运行的语句（PostgreSQL 的 `CREATE INDEX CONCURRENTLY` 等会因此报错）。

## [2.2.0] - 2026-09-18

### Added

- 新增 `WithAllowUnknownApplied`（顶层 Option 与 `migration.WithAllowUnknownApplied` MigratorOption）：显式授权 `up`/`IsUpToDate`/`status`/`pending` 容忍"已应用但当前 binary 未注册"的迁移记录——逐条记 Warn 后继续，只处理已注册且未应用的迁移。用于应用回滚窗口（新版本已写账本后回滚到旧 binary，启动期调用 `Up` 不再失败）。默认关闭、行为不变；`mark-applied` 与所有回滚入口不受该选项影响，始终 fail-closed。

### Changed

- `make migration create_*` 模板的 up 增加 `HasTable` 存在性检查，与 `add`/`drop_*` 模板一致：MySQL 上 DDL 已提交但账本记录未写的半失败状态，重跑 `up` 直接自愈，不再报 `Table already exists`。仅影响新生成的迁移文件。

### Fixed

- README：`create` 示例改为与生成模板一致的自包含快照形态（原示例 import 业务 model，会被 `migrate lint` 以 `non_self_contained` 判为 error）；`make model` 生成文件名更正为 `repository.go` / `repository_util.go`；补充 `WithTimeout` 覆盖整条命令含 DDL 执行的后果与生产建议；补充应用回滚窗口的处理方式。
- `make ddl diff` 的 LCS 循环改用 `slices.Backward`（`go fix` 建议，行为不变）。

## [2.1.0] - 2026-07-30

### Added

- `migrate lint` 新增内容级门禁：残留 `TODO` 占位、使用 `AutoMigrate`、非自包含 import（业务/第三方包）判为 error；raw `ALTER TABLE` 缺 `ALGORITHM`/`LOCK` 在线 DDL 策略、raw 危险 DDL（`DROP TABLE`/`DROP DATABASE`/`TRUNCATE`）判为 warning（`--strict` 下升为失败）。
- `migrate lint` 新增 `duplicate_registration`（error）：同名迁移被注册多次时报告（运行时仍先注册者赢），使"两个包注册同名迁移"可被发现。
- 回滚（`down`/`down-to`/`reset`/`refresh`）在回滚任一迁移之前做全量预检：任一待回滚记录不在 registry、缺 `Down`、或存在重复注册名（会静默使用先注册者的 `Down`）则整体拒绝，杜绝"回滚一半才失败"的部分回滚，与 `up` 的执行校验对称。
- 新增 `WithMigrationsTable`（顶层 Option 与 `migration.WithMigrationsTable` MigratorOption）：迁移记录表名可配置。多项目共用同一数据库时各用独立记录表（锁名按 DDL 资源边界选择：无共享表/外键用独立锁名，存在共享资源配相同锁名串行化），迁移账本与漂移校验互不干扰——此前文档宣称仅配 `WithLockName` 即可共库，实际第二个项目会把第一个项目的记录判为漂移而拒绝执行。
- 新增 `migration.ValidateMigrationsTable`：迁移记录表名合法性校验（仅允许字母、数字、下划线，长度不超过 63——MySQL 上限 64、PostgreSQL 63 字节且超长静默截断，取跨方言交集；不支持 schema 限定名）。
- 新增 `WithAllowFresh`（顶层 Option 与 `migration.WithAllowFresh` MigratorOption）：显式授权 `fresh` 执行，仅当本项目独占该数据库时才应开启。

### Changed

- **⚠ 破坏性变更** `fresh`（`Migrator.Fresh`）默认禁用：必须在 `Setup`/构造时经 `WithAllowFresh` 显式授权（CLI 运行时仍需 `--force`），未授权直接报错且不删任何表——fresh 会删除库内全部用户表（含其他项目的表与迁移账本），此前仅有文档警告没有代码拦截；数据库所有权不从记录表名推断（共库项目可能用默认表名、独占库也可能用自定义表名）。
- **⚠ 破坏性变更** `Migrator` 公开字段 `DB`/`Folder` 改为构造期快照：构造后写入不再影响迁移执行、记录读写与加锁——v2.0.5 中构造后改写 `DB` 会把迁移跑到新库而锁仍在旧库（"旧库持锁、新库跑迁移"的分裂），依赖该行为的调用方请改为重新构造 Migrator。
- **⚠ 破坏性变更** 迁移执行与破坏性命令（`up`/`fresh`/`refresh`）在动手前统一校验 registry：为空、存在重复注册名、任一迁移缺 `Up`、或迁移名不符合时间戳命名格式（`YYYY_MM_DD_HHMMSS_描述`，执行顺序依赖该前缀，此前只有 lint 检查）时直接报错——`fresh`/`refresh` 在删表/回滚**之前**拦下，绝不删光数据却不重建；重复注册名运行时也不再静默用第一个实现。回滚路径不查名称格式：历史误入账本的坏名迁移仍可通过 `down`/`reset` 清理。
- **⚠ 破坏性变更** `Pending`/`Status`/`MarkApplied` 改用完整一致性校验（含"数据库已应用但当前 binary 未注册"的漂移），漂移时报错而非静默返回成功。
- **⚠ 破坏性变更** 所有获取迁移锁的命令（`up`/`down`/`down-to`/`reset`/`refresh`/`fresh`/`mark-applied`）在取锁前校验连接池容量；`MaxOpenConns=1`（非 SQLite）时直接报错而非阻塞至超时。
- **⚠ 破坏性变更** `migrate up` 与 `IsUpToDate` 改以编译期注册表（registry）为迁移集合的唯一真实来源，不再依赖运行时的 `.go` 源文件目录。此前在部署环境缺少源目录时，`up` 会读到空目录并静默报告「已最新」、漏执行整批迁移；现在以已 import 编译进 binary 的迁移为准。
- **⚠ 破坏性变更** `migrate reset` / `refresh` / `fresh` 现在必须显式加 `--force` 才执行，缺失时直接返回错误并拒绝执行，防止误触导致数据丢失。
- **⚠ 破坏性变更** `make migration` 的 `add`/`update`/`drop`/`drop_column`/`drop_index` 模板改为自包含、显式的 raw SQL：不再 import 业务 model、`update` 不再使用 `AutoMigrate`。新生成的迁移带 `TODO` 占位，需补全（大表建议标注在线 DDL 策略）后才能通过 `migrate lint`。
- **⚠ 破坏性变更** `RollbackSteps(steps)` 要求 `steps > 0`，否则直接返回错误（此前负数经 GORM `Limit(-1)` 会取消行数限制而回滚全部）。
- **⚠ 破坏性变更** `RollbackTo(target)` 在 `target` 非空时要求它是真实已应用的版本，否则返回错误（此前传 `0` 或早于首个版本的字符串会误回滚全部）。
- **⚠ 破坏性变更** CLI `down`、`down-to` 现在必须显式加 `--force`，与 `reset`/`refresh`/`fresh` 一致。
- **⚠ 破坏性变更** `Fresh`/`Refresh` 在删表/回滚之前先校验 registry 非空；空 registry（通常是漏 import 迁移包）时直接报错、绝不删光数据却不重建。
- **⚠ 破坏性变更** `Pending`/`Status`/`MarkApplied`/`upWithoutLock` 在空 registry 时 fail-closed 报错，不再返回"空列表/静默"的误导性结果。
- **⚠ 破坏性变更** 迁移 `Up` 为 nil 时执行直接报错（不再静默记为已执行）；`Down` 为 nil 时回滚直接报错且不删除迁移记录（"不可回滚"请显式用 `Irreversible`）。
- **⚠ 破坏性变更** `mark-applied --to <version>`（`MarkApplied`）的目标非空时必须是已注册的迁移名，否则报错且不标记任何迁移（此前拼写错误的目标会静默标记错误范围）。
- `migrate up` 预检输出待执行数量（`Running N migration(s)...`），无待执行时提示 `Database is up to date.` 后直接返回。
- `migrate lint` 的 `missing_online_ddl`（`ALGORITHM`/`LOCK` 为 MySQL 专属语法）仅在数据库类型为 MySQL 时上报，PostgreSQL/SQLite 项目不再误报；危险 DDL 检查保持全方言生效。
- `make.SetProjectName` 与 `file.FileNameWithoutExtension` 标记为 Deprecated（保留兼容，推荐分别改用 `Setup`+`WithProjectName` 与标准库）。

### Removed

- 删除死模板 `migration.stub`（旧的 `AutoMigrate` + 业务 model 范例，已无引用）与 `migration_add_raw.stub`（逻辑合并进 `migration_add`，`--after` 改为注入 `AFTER` 子句）。

### Fixed

- `migrate up`：注册表为空（通常是漏 import 迁移包），或数据库中存在「已应用但当前 binary 未注册」的迁移（结构可能已漂移）时，`up` 与 `IsUpToDate` 改为 fail-closed 返回错误，不再静默通过。
- `migrate fresh`：MySQL 删表时的 `SET foreign_key_checks=0` → 删表 → 恢复 `=1` 改为固定在同一专属连接上执行；恢复使用独立超时上下文（业务上下文取消后仍尝试复位），恢复失败、以及关闭外键检查本身因网络错误/取消而执行结果未知时，都将该连接标记坏连接并物理关闭结束会话——任何情况下都不会把外键检查状态不确定的连接归还连接池污染业务查询。此前经连接池分发可能使关闭态落不到删表连接，或将关闭态残留污染被业务复用的池内连接。
- `migrate fresh`：清理范围补齐视图并限定 schema——MySQL 区分 BASE TABLE 与 VIEW 分别用 `DROP TABLE`/`DROP VIEW`（此前库中存在视图会因对视图执行 `DROP TABLE` 而失败）；PostgreSQL 的查询与 DROP 都显式限定 `public`，不再依赖 `search_path`，其他 schema 的同名表绝不受影响；SQLite 一并删除视图。残留视图导致重放 `CREATE VIEW` 冲突的问题一并消除。
- `migration.CurrentDatabase` 与 `migration.DetectDBType` 传入 nil、零值或未初始化的 `*gorm.DB` 时返回空值，不再 panic（零值 `gorm.DB` 上访问经嵌入 `Config` 提升的 `Dialector` 字段此前会 nil 解引用）。
- 空迁移账本不再触发 GORM `record not found` 误报日志：`Rollback` 与批次号查询改用 `Limit(1).Find` 替代 `First`——空账本是正常状态（如新库首次操作），`First` 产生的 `ErrRecordNotFound` 会被 GORM 默认 logger 打成错误日志、污染生产监控。
- `migrate fresh`：修复上述同连接删表在真实 MySQL（非空库）上因 `db.Connection` 内调用 `Migrator().DropTable` 返回 `invalid db` 而失败的问题（`db.Connection` 提供的是 `*sql.Conn`，Migrator 需 `*sql.DB`），改为在该连接上直接执行 raw `DROP TABLE`。由新增的真实 MySQL 集成测试发现并覆盖。
- `migrate lint`：修复在线 DDL 检查可被 SQL 注释绕过的问题——`ALTER TABLE ... /* ALGORITHM=INPLACE, LOCK=NONE */` 此前因注释含关键词被判为合规；现改为判定前先剥离 SQL 注释（`/* */` 与 `--`）。
- `migrate lint`：进一步修复在线 DDL 误判——改为匹配真实子句 `ALGORITHM\s*=`/`LOCK\s*=`（不再把列名 `algorithm`/`lock`、字符串值或 `#` 注释里的关键词当作已标注策略），并剥离 MySQL `#` 行注释。
- `migration.NewMigrator` 传入 nil db 不再直接 panic；`Setup` 等路径返回可处理错误。
- 迁移锁改用专属 `*sql.Conn` 获取/释放，并用独立超时上下文执行释放：业务上下文取消/超时后仍能可靠释放；释放不确定时物理关闭连接以结束会话，避免会话级命名锁残留在连接池（MySQL `GET_LOCK`、PostgreSQL `pg_advisory_lock`）。
- `migrate lint`：在线 DDL 判定前屏蔽 SQL 字符串字面量，修复 `DEFAULT 'algorithm=... lock=...'` 等把引号内关键词误当真实子句的绕过。
- `console.Error`（含 `console.Exit` 的消息）改输出到 stderr，重定向 stdout 时错误消息不再混入正常输出；`Success`/`Warning` 仍走 stdout。
- `migrate fresh`：PostgreSQL 删表时对表名做双引号标识符转义，与 MySQL 分支的反引号转义一致。
- 未调用 `migrate.Setup` 就执行迁移命令时返回错误而不再 panic；全局配置改为原子指针存取，消除并发场景下的数据竞争。
- PostgreSQL 迁移锁：获取锁失败（含等锁超时）后先复位会话级 `statement_timeout` 再归还连接，复位失败则物理关闭连接——此前失败路径会把带超时设置的连接归还连接池，复用该连接的业务查询会被莫名取消；锁释放路径的复位失败同样接入坏连接兜底。
- CLI 迁移命令的执行上下文改为从 `cmd.Context()` 派生，经 `ExecuteContext` 传入的取消信号（如 Ctrl-C）能中止迁移执行；`fresh` 的删表操作同样受超时/取消约束（此前完全脱离上下文控制）。
- 迁移记录表名 fail-closed 校验：非法表名（空格、SQL 片段、反引号、schema 限定名、纯空白、超过 63 字符等）在 `Setup`、`Lint` 或首个执行入口直接报错，绝不静默回退默认表名（多项目共库下静默回退会写错账本）；首尾空白自动规整。
- advisory lock 获取查询出错（网络错误、上下文取消等，服务端可能已授锁）时，专属连接改为标记坏连接后物理关闭、结束会话——此前直接归还连接池，可能残留持锁会话导致其他实例长时间等锁。

### Migration Notes

- 使用 `down` / `down-to` / `reset` / `refresh` / `fresh` 的脚本需补 `--force`。
- 继续使用 `fresh` 还需在 `Setup`（或 `NewMigrator`）时加 `WithAllowFresh()` 显式授权，且仅限本项目独占的数据库；共库环境请勿授权。
- 依赖「迁移目录为空即视为已最新」的旧行为会开始报错，属预期修正——请确保迁移包已被 import 进入 binary（如 `_ "yourapp/database/migrations"`）。
- 新生成的 `add`/`update`/`drop` 迁移形态改为 raw SQL，需按提示补全 `TODO`；`migrate lint` 会拦截未补全、`AutoMigrate` 与非自包含 import。
- `RollbackSteps` 需传正数；`down-to`/`RollbackTo` 的目标必须是已应用版本，否则报错——不会再误回滚全部。
- 手写迁移必须有 `Up`；可回滚迁移必须有 `Down`，"不可回滚"请显式用 `migration.Irreversible(...)`（nil `Down` 会在回滚时报错、不删记录）。
- 空 registry（漏 import 迁移包）下 `fresh`/`refresh`/`reset`/`down`/`pending`/`status` 等一律 fail-closed 报错，不再静默"成功/无待执行"。
