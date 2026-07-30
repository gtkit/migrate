# Changelog

本项目所有值得记录的变更都会写入本文件。

格式遵循 [Keep a Changelog 1.1.0](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本 2.0.0](https://semver.org/lang/zh-CN/)。

## [Unreleased]

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
