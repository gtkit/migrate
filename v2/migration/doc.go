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
// 空注册表、重复注册、缺 Up/Down、已应用记录未注册（漂移）等状态一律报错.
//
// 并发安全：Registry 线程安全；Migrator 的执行方法通过数据库 advisory lock
// 串行化多实例并发（SQLite 例外，无锁）；Migrator 本身按单 goroutine 使用设计.
package migration
