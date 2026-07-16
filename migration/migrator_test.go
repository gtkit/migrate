package migration

import (
	"errors"
	"os"
	"path/filepath"
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

func writeMigrationFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name+".go")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
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

	if err := m.Up(t.Context()); err == nil {
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

	if err := m.Up(t.Context()); err == nil {
		t.Fatalf("expected up to fail on panic")
	}

	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table creation to be rolled back on panic")
	}
}
