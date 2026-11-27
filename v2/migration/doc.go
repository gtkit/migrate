// Package migration 实现迁移执行引擎：迁移注册表、执行与回滚、
// 迁移记录账本、数据库 advisory lock 与一致性 lint.
//
// 迁移文件在 init 中注册到全局注册表：
//
//	func init() {
//		migration.Add("2026_01_02_150405_create_users_table", up, down)
//	}
//
// 执行入口是 Migrator（NewMigrator 构造）.所有执行方法 fail-closed：
// 空注册表、重复注册、缺 Up/Down、已应用记录未注册（漂移）、非法记录表名
// 等状态一律报错。Fresh 删除 MySQL 库内全部用户表与视图，
// 默认禁用，必须经 WithAllowFresh 显式授权（仅限本项目独占的数据库）.
//
// Registry 支持并发。Migrator 按单 goroutine 使用设计，多个实例通过 MySQL GET_LOCK 串行执行迁移。
package migration
