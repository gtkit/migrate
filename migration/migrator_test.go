package migration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	testMigratorUpAndRollback(t, openExecTestDB(t, "migrator_up_rollback"), "")
}

func testMigratorUpAndRollback(t *testing.T, db *gorm.DB, lockName string) {
	t.Helper()
	dir := t.TempDir()

	file := "2026_03_24_120000_create_exec_test_users_table"
	writeMigrationFile(t, dir, file, "package migrations\n")

	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		return tx.Migrator().CreateTable(&execTestUser{})
	}, func(tx *gorm.DB) error {
		return tx.Migrator().DropTable(&execTestUser{})
	})

	m := NewMigrator(dir, db, WithRegistry(registry), WithLockName(lockName), WithLockTimeout(2*time.Second))

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

func TestDestructiveReplayPreflight(t *testing.T) {
	const applied = "2026_03_24_120000_create_exec_test_users_table"
	const pending = "2026_03_24_120001_pending"
	for _, command := range []string{"fresh", "refresh"} {
		for _, condition := range []string{"missing-directory", "empty-directory", "unregistered-file", "nil-up", "valid"} {
			t.Run(command+"/"+condition, func(t *testing.T) {
				db := openExecTestDB(t, "preflight_"+command+condition)
				dir := t.TempDir()
				calls := 0
				registry := NewRegistry()
				registry.Add(applied, func(tx *gorm.DB) error {
					return tx.Migrator().CreateTable(&execTestUser{})
				}, func(tx *gorm.DB) error {
					calls++
					return tx.Migrator().DropTable(&execTestUser{})
				})
				writeMigrationFile(t, dir, applied, "package migrations\n")
				m := NewMigrator(dir, db, WithRegistry(registry))
				if err := m.Up(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := db.Create(&execTestUser{Name: "keep"}).Error; err != nil {
					t.Fatal(err)
				}
				switch condition {
				case "missing-directory":
					m.Folder = filepath.Join(dir, "missing")
				case "empty-directory":
					m.Folder = t.TempDir()
				case "unregistered-file":
					writeMigrationFile(t, dir, pending, "package migrations\n")
				case "nil-up":
					writeMigrationFile(t, dir, pending, "package migrations\n")
					registry.Add(pending, nil, nil)
				}
				run := m.Fresh
				if command == "refresh" {
					run = m.Refresh
				}
				err := run(t.Context())
				if (err == nil) != (condition == "valid") {
					t.Fatalf("replay: %v", err)
				}
				var rows int64
				if err := db.Model(&execTestUser{}).Count(&rows).Error; err != nil {
					t.Fatal(err)
				}
				if condition == "valid" && rows != 0 {
					t.Fatalf("expected rebuilt table to be empty, found %d rows", rows)
				}
				if condition != "valid" && (rows != 1 || calls != 0) {
					t.Fatalf("invalid replay changed business rows or called Down: rows=%d calls=%d", rows, calls)
				}
				if err := db.Model(&Migration{}).Count(&rows).Error; err != nil || rows != 1 {
					t.Fatalf("expected one ledger row: count=%d err=%v", rows, err)
				}
			})
		}
	}
}

func TestRollbackPreflightsWholeSelection(t *testing.T) {
	db := openExecTestDB(t, "rollback_preflight")
	dir := t.TempDir()
	registry := NewRegistry()
	calls := 0
	for i, name := range []string{"2026_03_24_120000_old", "2026_03_24_120001_new"} {
		writeMigrationFile(t, dir, name, "package migrations\n")
		var down MigrateFunc
		if i == 1 {
			down = func(*gorm.DB) error { calls++; return nil }
		}
		registry.Add(name, func(*gorm.DB) error { return nil }, down)
	}
	m := NewMigrator(dir, db, WithRegistry(registry))
	if err := m.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.RollbackSteps(t.Context(), -1); err == nil {
		t.Fatal("expected negative step error")
	}
	if err := m.RollbackSteps(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(t.Context()); err == nil {
		t.Fatal("expected missing Down error")
	}
	var rows int64
	if err := db.Model(&Migration{}).Count(&rows).Error; err != nil || rows != 2 || calls != 0 {
		t.Fatalf("rollback changed ledger or invoked Down: count=%d calls=%d err=%v", rows, calls, err)
	}
}

type lifecycleLog struct {
	NopLogger
	success []string
	failed  []string
}

func (l *lifecycleLog) Info(msg string, fields ...any) {
	if msg == "migrated" || msg == "rolled back" {
		l.success = append(l.success, msg+" "+formatKV(fields))
	}
}

func (l *lifecycleLog) Error(msg string, fields ...any) {
	l.failed = append(l.failed, msg+" "+formatKV(fields))
}

func TestMigratorLogsTransactionOutcome(t *testing.T) {
	db := openExecTestDB(t, "lifecycle_log")
	dir := t.TempDir()
	const name = "2026_03_24_120000_log_outcome"
	writeMigrationFile(t, dir, name, "package migrations\n")
	registry := NewRegistry()
	downErr := errors.New("failed to revert")
	registry.Add(name, func(*gorm.DB) error { return nil }, func(*gorm.DB) error { return downErr })
	logger := &lifecycleLog{}
	m := NewMigrator(dir, db, WithRegistry(registry), WithLogger(logger))
	if err := m.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(t.Context()); !errors.Is(err, downErr) {
		t.Fatalf("expected callback failure, got %v", err)
	}
	if len(logger.success) != 1 || !strings.HasPrefix(logger.success[0], "migrated file="+name+" batch=1 duration=") {
		t.Fatalf("unexpected success events: %v", logger.success)
	}
	if len(logger.failed) != 1 || !strings.HasPrefix(logger.failed[0], "rollback failed file="+name+" batch=1 duration=") ||
		!strings.Contains(logger.failed[0], "failed to revert") {
		t.Fatalf("unexpected failure events: %v", logger.failed)
	}
	downErr = nil
	if err := m.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(logger.success) != 2 || !strings.HasPrefix(logger.success[1], "rolled back file="+name+" batch=1 duration=") {
		t.Fatalf("unexpected rollback success events: %v", logger.success)
	}
}
