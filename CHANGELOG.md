# Changelog

本项目所有值得记录的变更都会写入本文件。

格式遵循 [Keep a Changelog 1.1.0](https://keepachangelog.com/zh-CN/1.1.0/)，版本号遵循 [语义化版本 2.0.0](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Added

- `migrate lint` 新增内容级门禁：残留 `TODO` 占位、使用 `AutoMigrate`、非自包含 import（业务/第三方包）判为 error；raw `ALTER TABLE` 缺 `ALGORITHM`/`LOCK` 在线 DDL 策略、raw 危险 DDL（`DROP TABLE`/`DROP DATABASE`/`TRUNCATE`）判为 warning（`--strict` 下升为失败）。

### Changed

- **⚠ 破坏性变更** `migrate up` 与 `IsUpToDate` 改以编译期注册表（registry）为迁移集合的唯一真实来源，不再依赖运行时的 `.go` 源文件目录。此前在部署环境缺少源目录时，`up` 会读到空目录并静默报告「已最新」、漏执行整批迁移；现在以已 import 编译进 binary 的迁移为准。
- **⚠ 破坏性变更** `migrate reset` / `refresh` / `fresh` 现在必须显式加 `--force` 才执行，缺失时直接返回错误并拒绝执行，防止误触导致数据丢失。
- **⚠ 破坏性变更** `make migration` 的 `add`/`update`/`drop`/`drop_column`/`drop_index` 模板改为自包含、显式的 raw SQL：不再 import 业务 model、`update` 不再使用 `AutoMigrate`。新生成的迁移带 `TODO` 占位，需补全（大表建议标注在线 DDL 策略）后才能通过 `migrate lint`。

### Removed

- 删除死模板 `migration.stub`（旧的 `AutoMigrate` + 业务 model 范例，已无引用）与 `migration_add_raw.stub`（逻辑合并进 `migration_add`，`--after` 改为注入 `AFTER` 子句）。

### Fixed

- `migrate up`：注册表为空（通常是漏 import 迁移包），或数据库中存在「已应用但当前 binary 未注册」的迁移（结构可能已漂移）时，`up` 与 `IsUpToDate` 改为 fail-closed 返回错误，不再静默通过。
- `migrate fresh`：MySQL 删表时的 `SET foreign_key_checks=0` → 删表 → 恢复 `=1` 改为固定在同一数据库连接上执行，并保证在连接归还连接池前恢复；此前经连接池分发可能使关闭态落不到删表连接，或将关闭态残留污染被业务复用的池内连接。

### Migration Notes

- 使用 `reset` / `refresh` / `fresh` 的脚本需补 `--force`。
- 依赖「迁移目录为空即视为已最新」的旧行为会开始报错，属预期修正——请确保迁移包已被 import 进入 binary。
- 新生成的 `add`/`update`/`drop` 迁移形态改为 raw SQL，需按提示补全 `TODO`；`migrate lint` 会拦截未补全、`AutoMigrate` 与非自包含 import。
