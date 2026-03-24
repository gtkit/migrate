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
