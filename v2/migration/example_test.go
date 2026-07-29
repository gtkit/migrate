package migration_test

import (
	"context"
	"fmt"

	"github.com/gtkit/migrate/v2/migration"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Example 演示注册迁移、执行 Up 并查看状态的完整流程.
// 生产中迁移通常在迁移文件的 init 中调用 migration.Add 注册到全局注册表；
// 这里为保证示例可独立运行，使用自定义 Registry.
func Example() {
	db, err := gorm.Open(sqlite.Open("file:example?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		fmt.Println("open:", err)
		return
	}

	registry := migration.NewRegistry()
	registry.Add("2026_01_02_150405_create_users_table",
		func(tx *gorm.DB) error {
			return tx.Exec("CREATE TABLE users (id integer primary key)").Error
		},
		func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE users").Error
		},
	)

	m := migration.NewMigrator("database/migrations", db,
		migration.WithRegistry(registry),
		migration.WithLogger(&migration.NopLogger{}),
	)

	if err := m.Up(context.Background()); err != nil {
		fmt.Println("up:", err)
		return
	}

	statuses, err := m.Status(context.Background())
	if err != nil {
		fmt.Println("status:", err)
		return
	}
	for _, s := range statuses {
		fmt.Printf("%s ran=%v batch=%d\n", s.Name, s.Ran, s.Batch)
	}

	// Output:
	// 2026_01_02_150405_create_users_table ran=true batch=1
}

// ExampleValidateMigrationsTable 演示迁移记录表名的合法性校验.
func ExampleValidateMigrationsTable() {
	fmt.Println(migration.ValidateMigrationsTable("migrations_user_svc"))
	fmt.Println(migration.ValidateMigrationsTable("ledger AS l") != nil)

	// Output:
	// <nil>
	// true
}

// ExampleWithAllowFresh 演示 Fresh 的显式授权：默认禁用直接拒绝，
// 授权后删除库内全部表并重放迁移.仅当本项目独占该数据库时才应授权.
func ExampleWithAllowFresh() {
	db, err := gorm.Open(sqlite.Open("file:example_allow_fresh?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		fmt.Println("open:", err)
		return
	}

	registry := migration.NewRegistry()
	registry.Add("2026_01_02_150405_create_users_table",
		func(tx *gorm.DB) error {
			return tx.Exec("CREATE TABLE users (id integer primary key)").Error
		},
		func(tx *gorm.DB) error {
			return tx.Exec("DROP TABLE users").Error
		},
	)

	// 未授权：Fresh 默认禁用.
	m := migration.NewMigrator("database/migrations", db,
		migration.WithRegistry(registry),
		migration.WithLogger(&migration.NopLogger{}),
	)
	fmt.Println("rejected without allow:", m.Fresh(context.Background()) != nil)

	// 显式授权后可执行.
	m = migration.NewMigrator("database/migrations", db,
		migration.WithRegistry(registry),
		migration.WithLogger(&migration.NopLogger{}),
		migration.WithAllowFresh(),
	)
	fmt.Println("fresh with allow:", m.Fresh(context.Background()))

	// Output:
	// rejected without allow: true
	// fresh with allow: <nil>
}
