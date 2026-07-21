# Changelog

本项目所有值得记录的变更都会写入本文件。

格式遵循 [Keep a Changelog 1.1.0](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本 2.0.0](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Added

- `migrate lint` 新增内容级门禁：残留 `TODO` 占位、使用 `AutoMigrate`、非自包含 import（业务/第三方包）判为 error；raw `ALTER TABLE` 缺 `ALGORITHM`/`LOCK` 在线 DDL 策略、raw 危险 DDL（`DROP TABLE`/`DROP DATABASE`/`TRUNCATE`）判为 warning（`--strict` 下升为失败）。
- `migrate lint` 新增 `duplicate_registration`（error）：同名迁移被注册多次时报告（运行时仍先注册者赢），使"两个包注册同名迁移"可被发现。
- 回滚（`down`/`down-to`/`reset`/`refresh`）在回滚任一迁移之前做全量预检：任一待回滚记录不在 registry 或缺 `Down` 则整体拒绝，杜绝"回滚一半才失败"的部分回滚。

### Changed

- **⚠ 破坏性变更** 迁移执行与破坏性命令（`up`/`fresh`/`refresh`）在动手前统一校验 registry：为空、存在重复注册名、或任一迁移缺 `Up` 时直接报错——`fresh`/`refresh` 在删表/回滚**之前**拦下，绝不删光数据却不重建；重复注册名运行时也不再静默用第一个实现。
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

### Removed

- 删除死模板 `migration.stub`（旧的 `AutoMigrate` + 业务 model 范例，已无引用）与 `migration_add_raw.stub`（逻辑合并进 `migration_add`，`--after` 改为注入 `AFTER` 子句）。

### Fixed

- `migrate up`：注册表为空（通常是漏 import 迁移包），或数据库中存在「已应用但当前 binary 未注册」的迁移（结构可能已漂移）时，`up` 与 `IsUpToDate` 改为 fail-closed 返回错误，不再静默通过。
- `migrate fresh`：MySQL 删表时的 `SET foreign_key_checks=0` → 删表 → 恢复 `=1` 改为固定在同一数据库连接上执行，并保证在连接归还连接池前恢复；此前经连接池分发可能使关闭态落不到删表连接，或将关闭态残留污染被业务复用的池内连接。
- `migrate fresh`：修复上述同连接删表在真实 MySQL（非空库）上因 `db.Connection` 内调用 `Migrator().DropTable` 返回 `invalid db` 而失败的问题（`db.Connection` 提供的是 `*sql.Conn`，Migrator 需 `*sql.DB`），改为在该连接上直接执行 raw `DROP TABLE`。由新增的真实 MySQL 集成测试发现并覆盖。
- `migrate lint`：修复在线 DDL 检查可被 SQL 注释绕过的问题——`ALTER TABLE ... /* ALGORITHM=INPLACE, LOCK=NONE */` 此前因注释含关键词被判为合规；现改为判定前先剥离 SQL 注释（`/* */` 与 `--`）。
- `migrate lint`：进一步修复在线 DDL 误判——改为匹配真实子句 `ALGORITHM\s*=`/`LOCK\s*=`（不再把列名 `algorithm`/`lock`、字符串值或 `#` 注释里的关键词当作已标注策略），并剥离 MySQL `#` 行注释。
- `migration.NewMigrator` 传入 nil db 不再直接 panic；`Setup` 等路径返回可处理错误。
- 迁移锁改用专属 `*sql.Conn` 获取/释放，并用独立超时上下文执行释放：业务上下文取消/超时后仍能可靠释放；释放不确定时物理关闭连接以结束会话，避免会话级命名锁残留在连接池（MySQL `GET_LOCK`、PostgreSQL `pg_advisory_lock`）。
- `migrate lint`：在线 DDL 判定前屏蔽 SQL 字符串字面量，修复 `DEFAULT 'algorithm=... lock=...'` 等把引号内关键词误当真实子句的绕过。

### Migration Notes

- 使用 `down` / `down-to` / `reset` / `refresh` / `fresh` 的脚本需补 `--force`。
- 依赖「迁移目录为空即视为已最新」的旧行为会开始报错，属预期修正——请确保迁移包已被 import 进入 binary（如 `_ "yourapp/database/migrations"`）。
- 新生成的 `add`/`update`/`drop` 迁移形态改为 raw SQL，需按提示补全 `TODO`；`migrate lint` 会拦截未补全、`AutoMigrate` 与非自包含 import。
- `RollbackSteps` 需传正数；`down-to`/`RollbackTo` 的目标必须是已应用版本，否则报错——不会再误回滚全部。
- 手写迁移必须有 `Up`；可回滚迁移必须有 `Down`，"不可回滚"请显式用 `migration.Irreversible(...)`（nil `Down` 会在回滚时报错、不删记录）。
- 空 registry（漏 import 迁移包）下 `fresh`/`refresh`/`reset`/`down`/`pending`/`status` 等一律 fail-closed 报错，不再静默"成功/无待执行"。
