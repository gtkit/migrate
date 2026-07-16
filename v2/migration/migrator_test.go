package migration

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type execTestUser struct {
	ID   int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Name string `gorm:"column:name;size:64"`
}

func (execTestUser) TableName() string { return "exec_test_users" }

// openExecTestDB 打开一个共享缓存的 SQLite 内存库并限制为单连接，
// 保证事务与后续查询看到同一个库.
func openExecTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestMigratorUpAndRollback(t *testing.T) {
	db := openExecTestDB(t, "migrator_up_rollback")
	dir := t.TempDir()

	file := "2026_03_24_120000_create_exec_test_users_table"
	writeMigrationFile(t, dir, file, "package migrations\n")

	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		return tx.Migrator().CreateTable(&execTestUser{})
	}, func(tx *gorm.DB) error {
		return tx.Migrator().DropTable(&execTestUser{})
	})

	m := NewMigrator(dir, db, WithRegistry(registry), WithLockTimeout(2*time.Second))

	if err := m.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table to exist after up")
	}

	var count int64
	if err := db.Model(&Migration{}).Where("migration = ?", file).Count(&count).Error; err != nil {
		t.Fatalf("count records: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 migration record, got %d", count)
	}

	if err := m.Rollback(t.Context()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table to be dropped after rollback")
	}
	if err := db.Model(&Migration{}).Where("migration = ?", file).Count(&count).Error; err != nil {
		t.Fatalf("count records after rollback: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 migration records after rollback, got %d", count)
	}
}

// TestMigratorUpRollsBackOnFailure 验证 Up 中途失败时整个迁移在事务内回滚：
// 已创建的表被撤销，且不写入迁移记录（SQLite 支持事务型 DDL）.
func TestMigratorUpRollsBackOnFailure(t *testing.T) {
	db := openExecTestDB(t, "migrator_up_failure")
	dir := t.TempDir()

	file := "2026_03_24_120000_create_exec_test_users_table"
	writeMigrationFile(t, dir, file, "package migrations\n")

	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		// 先建表，再返回错误——事务应把建表一并回滚.
		if err := tx.Migrator().CreateTable(&execTestUser{}); err != nil {
			return err
		}
		return errors.New("boom")
	}, nil)

	m := NewMigrator(dir, db, WithRegistry(registry))

	err := m.Up(t.Context())
	if err == nil {
		t.Fatalf("expected up to fail")
	}

	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table creation to be rolled back on failure")
	}

	var count int64
	if err := db.Model(&Migration{}).Where("migration = ?", file).Count(&count).Error; err != nil {
		t.Fatalf("count records: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no migration record after failed up, got %d", count)
	}
}

// TestMigratorUpRollsBackOnPanic 验证迁移函数 panic 时被转化为 error 且整体回滚.
func TestMigratorUpRollsBackOnPanic(t *testing.T) {
	db := openExecTestDB(t, "migrator_up_panic")
	dir := t.TempDir()

	file := "2026_03_24_120000_create_exec_test_users_table"
	writeMigrationFile(t, dir, file, "package migrations\n")

	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		if err := tx.Migrator().CreateTable(&execTestUser{}); err != nil {
			return err
		}
		panic("boom")
	}, nil)

	m := NewMigrator(dir, db, WithRegistry(registry))

	err := m.Up(t.Context())
	if err == nil {
		t.Fatalf("expected up to fail on panic")
	}

	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table creation to be rolled back on panic")
	}
}

// TestMigratorMarkApplied 验证 mark-applied 只写入迁移记录、不执行 Up.
func TestMigratorMarkApplied(t *testing.T) {
	db := openExecTestDB(t, "migrator_mark_applied")
	dir := t.TempDir()

	f1 := "2026_03_24_120000_create_a_table"
	f2 := "2026_03_24_120001_create_b_table"
	writeMigrationFile(t, dir, f1, "package migrations\n")
	writeMigrationFile(t, dir, f2, "package migrations\n")

	registry := NewRegistry()
	// Up 若被执行会建表；mark-applied 不应执行它们.
	registry.Add(f1, func(tx *gorm.DB) error { return tx.Migrator().CreateTable(&execTestUser{}) }, nil)
	registry.Add(f2, func(tx *gorm.DB) error { return tx.Migrator().CreateTable(&execTestUser{}) }, nil)

	m := NewMigrator(dir, db, WithRegistry(registry))

	marked, err := m.MarkApplied(t.Context(), "")
	if err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	if len(marked) != 2 {
		t.Fatalf("expected 2 marked, got %d: %v", len(marked), marked)
	}
	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("mark-applied must not run Up (table should not exist)")
	}

	upToDate, err := m.IsUpToDate(t.Context())
	if err != nil {
		t.Fatalf("is up to date: %v", err)
	}
	if !upToDate {
		t.Fatalf("expected up-to-date after mark-applied")
	}

	again, err := m.MarkApplied(t.Context(), "")
	if err != nil {
		t.Fatalf("mark applied again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected nothing to mark on second run, got %v", again)
	}
}

// TestMigratorMarkAppliedTo 验证 --to 只标记不超过指定版本的 pending 迁移.
func TestMigratorMarkAppliedTo(t *testing.T) {
	db := openExecTestDB(t, "migrator_mark_applied_to")
	dir := t.TempDir()

	f1 := "2026_03_24_120000_create_a_table"
	f2 := "2026_03_24_120001_create_b_table"
	f3 := "2026_03_24_120002_create_c_table"
	noop := func(*gorm.DB) error { return nil }
	registry := NewRegistry()
	for _, f := range []string{f1, f2, f3} {
		writeMigrationFile(t, dir, f, "package migrations\n")
		registry.Add(f, noop, noop)
	}

	m := NewMigrator(dir, db, WithRegistry(registry))

	marked, err := m.MarkApplied(t.Context(), f2)
	if err != nil {
		t.Fatalf("mark applied to: %v", err)
	}
	if len(marked) != 2 || marked[0] != f1 || marked[1] != f2 {
		t.Fatalf("expected [%s %s], got %v", f1, f2, marked)
	}

	pending, err := m.Pending(t.Context())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0] != f3 {
		t.Fatalf("expected [%s] pending, got %v", f3, pending)
	}
}

// TestMigratorRollbackTo 验证 down-to 只回滚版本高于目标（不含目标）的迁移.
func TestMigratorRollbackTo(t *testing.T) {
	db := openExecTestDB(t, "migrator_rollback_to")
	dir := t.TempDir()

	f1 := "2026_03_24_120000_create_a_table"
	f2 := "2026_03_24_120001_create_b_table"
	f3 := "2026_03_24_120002_create_c_table"
	tables := map[string]string{f1: "rt_a", f2: "rt_b", f3: "rt_c"}

	registry := NewRegistry()
	for _, f := range []string{f1, f2, f3} {
		tbl := tables[f]
		writeMigrationFile(t, dir, f, "package migrations\n")
		registry.Add(f,
			func(tx *gorm.DB) error { return tx.Exec("CREATE TABLE " + tbl + " (id integer primary key)").Error },
			func(tx *gorm.DB) error { return tx.Exec("DROP TABLE " + tbl).Error },
		)
	}

	m := NewMigrator(dir, db, WithRegistry(registry))
	if err := m.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}

	// 回滚到 f1（不含）：应回滚 f2、f3.
	if err := m.RollbackTo(t.Context(), f1); err != nil {
		t.Fatalf("rollback-to: %v", err)
	}

	if !db.Migrator().HasTable("rt_a") {
		t.Fatalf("rt_a should remain (f1 not rolled back)")
	}
	if db.Migrator().HasTable("rt_b") || db.Migrator().HasTable("rt_c") {
		t.Fatalf("rt_b/rt_c should be dropped after rollback-to f1")
	}

	pending, err := m.Pending(t.Context())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending after rollback-to, got %d: %v", len(pending), pending)
	}
}
