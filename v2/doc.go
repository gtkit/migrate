// Package migrate 提供基于 GORM + Cobra 的数据库迁移工具，
// 以 Laravel 风格的命令组织迁移的创建、执行、回滚与检查，
// 支持 MySQL、PostgreSQL、SQLite（代码生成模板当前为 MySQL 语法）.
//
// 使用前必须调用 Setup 注入数据库连接与配置，然后把 CmdMigrate、
// make.CmdMake 挂载到应用的 Cobra 根命令：
//
//	if err := migrate.Setup(db,
//		migrate.WithProjectName("yourapp"),
//		migrate.WithLockName("yourapp_migrate"),
//	); err != nil {
//		log.Fatal(err)
//	}
//	rootCmd.AddCommand(command.Commands()...)
//
// 迁移集合以编译期注册表为唯一真实来源：迁移文件在 init 中调用
// migration.Add 注册，必须被 import 编译进二进制（通常通过
// `_ "yourapp/database/migrations"` 空导入）.
//
// 破坏性命令双层保护：down/reset/refresh/fresh 运行时必须加 --force；
// fresh 会删除库内全部用户表，额外要求 Setup 时经 WithAllowFresh 显式授权
// （仅限本项目独占的数据库），否则直接拒绝执行.
//
// 并发安全：多实例同时执行迁移时通过数据库 advisory lock 串行化
// （MySQL GET_LOCK / PostgreSQL pg_advisory_lock；SQLite 无锁，仅限单进程）.
package migrate
