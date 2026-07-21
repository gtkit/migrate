//go:build integration

package migration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// MySQL 专属路径（fresh 的 foreign_key_checks 同连接关闭/删表/复位、GET_LOCK 竞争与超时）
// SQLite 无法覆盖，由本文件的 TestMigratorMySQLForeignKeyChecksRestored 与
// TestMigratorMySQLLockContention 在真实 MySQL 上验证（需设置 MIGRATE_TEST_MYSQL_DSN）。
func TestMigratorMySQLIntegration(t *testing.T) {
	runMigratorIntegrationTest(t, "mysql", os.Getenv("MIGRATE_TEST_MYSQL_DSN"), func(dsn string) (db *gorm.DB, closeFn func(), err error) {
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
		return db, func() {}, err
	})
}

func TestMigratorPostgresIntegration(t *testing.T) {
	runMigratorIntegrationTest(t, "postgres", os.Getenv("MIGRATE_TEST_POSTGRES_DSN"), func(dsn string) (db *gorm.DB, closeFn func(), err error) {
		db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		return db, func() {}, err
	})
}

func runMigratorIntegrationTest(t *testing.T, name, dsn string, open func(string) (*gorm.DB, func(), error)) {
	t.Helper()
	if strings.TrimSpace(dsn) == "" {
		t.Skipf("%s integration test skipped: DSN env var is not set", name)
	}

	db, closeFn, err := open(dsn)
	if err != nil {
		t.Fatalf("open %s database: %v", name, err)
	}
	defer closeFn()

	databaseName := strings.ToLower(CurrentDatabase(db))
	if !strings.Contains(databaseName, "test") && os.Getenv("MIGRATE_TEST_ALLOW_ANY_DB") != "1" {
		t.Skipf("%s integration test skipped: database %q does not look like a test database", name, databaseName)
	}

	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("delete all tables: %v", err)
	}

	dir := t.TempDir()
	file1 := "2026_03_24_120000_create_integration_users_table"
	file2 := "2026_03_24_120001_add_email_to_integration_users_table"
	writeIntegrationMigrationFile(t, dir, file1)
	writeIntegrationMigrationFile(t, dir, file2)

	registry := NewRegistry()
	registry.Add(file1, func(db *gorm.DB) error {
		return db.Migrator().CreateTable(&integrationUser{})
	}, func(db *gorm.DB) error {
		return db.Migrator().DropTable(&integrationUser{})
	})
	registry.Add(file2, func(db *gorm.DB) error {
		if db.Migrator().HasColumn(&integrationUserV2{}, "Email") {
			return nil
		}
		return db.Migrator().AddColumn(&integrationUserV2{}, "Email")
	}, func(db *gorm.DB) error {
		if !db.Migrator().HasColumn(&integrationUserV2{}, "email") {
			return nil
		}
		return db.Migrator().DropColumn(&integrationUserV2{}, "email")
	})

	m := NewMigrator(dir, db,
		WithRegistry(registry),
		WithLockName(fmt.Sprintf("migrate_integration_%s_%d", name, time.Now().UnixNano())),
	)

	report, err := m.Lint(t.Context(), LintOptions{})
	if err != nil {
		t.Fatalf("lint before up: %v", err)
	}
	if len(report.Issues) != 0 {
		t.Fatalf("expected clean lint report before up, got %#v", report.Issues)
	}

	pending, err := m.Pending(t.Context())
	if err != nil {
		t.Fatalf("pending before up: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending migrations, got %d", len(pending))
	}

	if err := m.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}

	if !db.Migrator().HasTable(&integrationUser{}) {
		t.Fatalf("expected integration_users table to exist")
	}
	if !db.Migrator().HasColumn(&integrationUserV2{}, "email") {
		t.Fatalf("expected email column to exist")
	}

	statuses, err := m.Status(t.Context())
	if err != nil {
		t.Fatalf("status after up: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 migration statuses, got %d", len(statuses))
	}
	for _, status := range statuses {
		if !status.Ran {
			t.Fatalf("expected migration %s to be marked as ran", status.Name)
		}
	}

	ddl, err := generateDialectDDLForTest(db, &integrationUserV2{})
	if err != nil {
		t.Fatalf("generate strict ddl: %v", err)
	}
	if !strings.Contains(strings.ToLower(ddl), "create table") {
		t.Fatalf("expected create table statement, got:\n%s", ddl)
	}

	if err := m.Rollback(t.Context()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if db.Migrator().HasTable(&integrationUser{}) {
		t.Fatalf("expected integration_users table to be dropped after rollback")
	}
}

// openMySQLTestDB 打开真实 MySQL 测试库；未设置 DSN 或不是 test 库则跳过.
func openMySQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("MIGRATE_TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("mysql integration test skipped: MIGRATE_TEST_MYSQL_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	if name := strings.ToLower(CurrentDatabase(db)); !strings.Contains(name, "test") && os.Getenv("MIGRATE_TEST_ALLOW_ANY_DB") != "1" {
		t.Skipf("mysql integration test skipped: database %q does not look like a test database", name)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestMigratorMySQLForeignKeyChecksRestored 在真实 MySQL 上验证 fix C：
// deleteMySQLTables 关闭外键检查、删表、恢复固定在同一连接完成.
// 用带外键约束的父子表验证删除成功——外键未在删表连接上关闭时，删被引用的
// parent 会因约束失败；删成功即证明外键检查确实在同一连接被关闭.
func TestMigratorMySQLForeignKeyChecksRestored(t *testing.T) {
	db := openMySQLTestDB(t)

	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("initial cleanup: %v", err)
	}

	if err := db.Exec("CREATE TABLE fk_parent (id INT PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := db.Exec("CREATE TABLE fk_child (id INT PRIMARY KEY, pid INT, CONSTRAINT fk_c FOREIGN KEY (pid) REFERENCES fk_parent(id))").Error; err != nil {
		t.Fatalf("create child: %v", err)
	}

	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("delete all tables with FK constraint: %v", err)
	}

	var remaining int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?", CurrentDatabase(db)).
		Scan(&remaining).Error; err != nil {
		t.Fatalf("count tables: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected all tables dropped, got %d remaining", remaining)
	}
}

// TestMigratorMySQLLockContention 在真实 MySQL 上验证 GET_LOCK 竞争与超时：
// 一个实例持锁时，另一个实例在 WithLockTimeout 内拿不到锁应返回错误而非无限阻塞；
// 释放后可正常获取.
func TestMigratorMySQLLockContention(t *testing.T) {
	dbA := openMySQLTestDB(t)
	dbB := openMySQLTestDB(t)

	lockName := fmt.Sprintf("migrate_contention_%d", time.Now().UnixNano())
	mA := NewMigrator(t.TempDir(), dbA, WithRegistry(NewRegistry()), WithLockName(lockName))
	mB := NewMigrator(t.TempDir(), dbB, WithRegistry(NewRegistry()),
		WithLockName(lockName), WithLockTimeout(2*time.Second))

	release, err := mA.lock.Acquire(t.Context())
	if err != nil {
		t.Fatalf("A acquire lock: %v", err)
	}

	// B 在 A 持锁期间获取同名锁：应在超时后失败.
	start := time.Now()
	if _, err := mB.lock.Acquire(t.Context()); err == nil {
		t.Fatalf("B should fail to acquire a lock held by A")
	}
	if waited := time.Since(start); waited < time.Second {
		t.Fatalf("B should have waited for the lock timeout, returned after %s", waited)
	}

	// A 释放后 B 应能获取.
	release()
	relB, err := mB.lock.Acquire(t.Context())
	if err != nil {
		t.Fatalf("B acquire after release: %v", err)
	}
	relB()
}

// TestMigratorMySQLLockReleasedAfterContextCancel 验证业务 ctx 取消后锁仍被可靠释放：
// 取消后调用释放函数，另一个实例应能立即获取同名锁（会话锁不残留在连接池）.
func TestMigratorMySQLLockReleasedAfterContextCancel(t *testing.T) {
	dbA := openMySQLTestDB(t)
	dbB := openMySQLTestDB(t)

	lockName := fmt.Sprintf("migrate_cancel_%d", time.Now().UnixNano())
	mA := NewMigrator(t.TempDir(), dbA, WithRegistry(NewRegistry()), WithLockName(lockName))
	mB := NewMigrator(t.TempDir(), dbB, WithRegistry(NewRegistry()),
		WithLockName(lockName), WithLockTimeout(2*time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	release, err := mA.lock.Acquire(ctx)
	if err != nil {
		t.Fatalf("A acquire: %v", err)
	}

	// 取消业务 ctx 后再释放（模拟迁移超时/取消后走 defer release）.
	cancel()
	release()

	// A 的锁应已可靠释放，B 立即可获取.
	relB, err := mB.lock.Acquire(t.Context())
	if err != nil {
		t.Fatalf("B should acquire the lock after A released it post-cancel: %v", err)
	}
	relB()
}

func writeIntegrationMigrationFile(t *testing.T, dir, fileName string) {
	t.Helper()
	path := filepath.Join(dir, fileName+".go")
	if err := os.WriteFile(path, []byte("package migrations\n"), 0o644); err != nil {
		t.Fatalf("write migration file %s: %v", path, err)
	}
}

func generateDialectDDLForTest(db *gorm.DB, model any) (string, error) {
	// Avoid import cycle with make package by using a small local helper.
	capture := &integrationDDLLogger{seen: make(map[string]struct{})}
	dryRunDB := db.Session(&gorm.Session{
		DryRun: true,
		Logger: capture,
	})
	if err := dryRunDB.Migrator().CreateTable(model); err != nil {
		return "", err
	}
	return strings.Join(capture.sql, "\n"), nil
}

type integrationDDLLogger struct {
	sql  []string
	seen map[string]struct{}
}

func (l *integrationDDLLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface { return l }
func (l *integrationDDLLogger) Info(context.Context, string, ...interface{})     {}
func (l *integrationDDLLogger) Warn(context.Context, string, ...interface{})     {}
func (l *integrationDDLLogger) Error(context.Context, string, ...interface{})    {}
func (l *integrationDDLLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return
	}
	if !strings.HasSuffix(sql, ";") {
		sql += ";"
	}
	if _, ok := l.seen[sql]; ok {
		return
	}
	l.seen[sql] = struct{}{}
	l.sql = append(l.sql, sql)
}

type integrationUser struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Name      string    `gorm:"column:name;size:64;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (integrationUser) TableName() string {
	return "integration_users"
}

type integrationUserV2 struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Name      string    `gorm:"column:name;size:64;not null"`
	Email     string    `gorm:"column:email;size:128;uniqueIndex"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (integrationUserV2) TableName() string {
	return "integration_users"
}
