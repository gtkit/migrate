//go:build integration

package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	makecmd "github.com/gtkit/migrate/v2/make"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func skipIntegration(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("MIGRATE_TEST_REQUIRE_DATABASES") == "1" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// MySQL 专属路径（fresh 的 foreign_key_checks 同连接关闭/删表/复位、GET_LOCK 竞争与超时）
// SQLite 无法覆盖，由本文件的 TestMigratorMySQLForeignKeyChecksRestored 与
// TestMigratorMySQLLockContention 在真实 MySQL 上验证（需设置 MIGRATE_TEST_MYSQL_DSN）。
func TestMigratorMySQLIntegration(t *testing.T) {
	runMigratorIntegrationTest(t, "mysql", os.Getenv("MIGRATE_TEST_MYSQL_DSN"), func(dsn string) (*gorm.DB, error) {
		return gorm.Open(mysql.Open(dsn), &gorm.Config{})
	})
}

func TestMigratorPostgresIntegration(t *testing.T) {
	runMigratorIntegrationTest(t, "postgres", os.Getenv("MIGRATE_TEST_POSTGRES_DSN"), func(dsn string) (*gorm.DB, error) {
		return gorm.Open(postgres.Open(dsn), &gorm.Config{})
	})
}

func runMigratorIntegrationTest(t *testing.T, name, dsn string, open func(string) (*gorm.DB, error)) {
	t.Helper()
	if strings.TrimSpace(dsn) == "" {
		skipIntegration(t, "%s integration test skipped: DSN env var is not set", name)
	}

	db, err := open(dsn)
	if err != nil {
		t.Fatalf("open %s database: %v", name, err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get %s connection pool: %v", name, err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close %s connection pool: %v", name, err)
		}
	})

	databaseName := strings.ToLower(CurrentDatabase(db))
	if !strings.Contains(databaseName, "test") && os.Getenv("MIGRATE_TEST_ALLOW_ANY_DB") != "1" {
		skipIntegration(t, "%s integration test skipped: database %q does not look like a test database", name, databaseName)
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
		skipIntegration(t, "mysql integration test skipped: MIGRATE_TEST_MYSQL_DSN is not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	if name := strings.ToLower(CurrentDatabase(db)); !strings.Contains(name, "test") && os.Getenv("MIGRATE_TEST_ALLOW_ANY_DB") != "1" {
		skipIntegration(t, "mysql integration test skipped: database %q does not look like a test database", name)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// TestMigratorMySQLForeignKeyChecksRestored 验证 fresh 在同一 MySQL 会话恢复外键检查原值.
func TestMigratorMySQLForeignKeyChecksRestored(t *testing.T) {
	db := openMySQLTestDB(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatalf("get MySQL connection pool: %v", err)
	}
	pool.SetMaxOpenConns(2)
	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("initial cleanup: %v", err)
	}

	for _, tt := range []struct {
		name    string
		enabled bool
	}{
		{name: "initially_enabled", enabled: true},
		{name: "initially_disabled", enabled: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cleanMySQLFreshFixtures(t, db, "fk_parent", "fk_child", &integrationUser{}, &Migration{})

			// The cycle forces the cleanup session to disable checks regardless of drop order.
			for _, stmt := range []string{
				"CREATE TABLE fk_parent (id INT PRIMARY KEY, child_id INT) ENGINE=InnoDB",
				"CREATE TABLE fk_child (id INT PRIMARY KEY, parent_id INT, FOREIGN KEY (parent_id) REFERENCES fk_parent(id)) ENGINE=InnoDB",
				"ALTER TABLE fk_parent ADD FOREIGN KEY (child_id) REFERENCES fk_child(id)",
			} {
				if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
					t.Fatalf("create foreign key fixture: %v", err)
				}
			}
			before := mysqlForeignKeyCheckSessions(t, ctx, pool, &tt.enabled)

			const file = "2026_03_24_120000_create_integration_users_table"
			dir := t.TempDir()
			writeIntegrationMigrationFile(t, dir, file)
			registry := NewRegistry()
			registry.Add(file, func(tx *gorm.DB) error {
				return tx.Migrator().CreateTable(&integrationUser{})
			}, nil)
			m := NewMigrator(dir, db, WithRegistry(registry), WithAllowFresh())
			if err := m.Fresh(ctx); err != nil {
				t.Fatalf("fresh with foreign key fixtures: %v", err)
			}
			if after := mysqlForeignKeyCheckSessions(t, ctx, pool, nil); !maps.Equal(before, after) {
				t.Errorf("foreign_key_checks changed on MySQL sessions: before=%v after=%v", before, after)
			}

			migrator := db.WithContext(ctx).Migrator()
			if migrator.HasTable("fk_parent") || migrator.HasTable("fk_child") {
				t.Fatal("fresh left an old foreign key fixture table")
			}
			status, err := m.Status(ctx)
			if err != nil {
				t.Fatalf("load fresh migration status: %v", err)
			}
			if !migrator.HasTable(&integrationUser{}) || len(status) != 1 || !status[0].Ran {
				t.Fatalf("fresh did not replay the migration: status=%+v", status)
			}
		})
	}
	t.Run("drop_failure", func(t *testing.T) {
		observer, err := openMySQLTestDB(t).DB()
		if err != nil {
			t.Fatalf("get metadata lock connection pool: %v", err)
		}
		t.Run("initially_enabled", func(t *testing.T) {
			testMySQLFreshDropFailure(t, db, pool, observer, true)
		})
		t.Run("initially_disabled", func(t *testing.T) {
			testMySQLFreshDropFailure(t, db, pool, observer, false)
		})
	})
	t.Run("discard_connection", func(t *testing.T) {
		observer, err := openMySQLTestDB(t).DB()
		if err != nil {
			t.Fatalf("get test observer connection pool: %v", err)
		}
		testMySQLFreshDiscardsConnection(t, strings.TrimSpace(os.Getenv("MIGRATE_TEST_MYSQL_DSN")), observer)
	})
}

func cleanMySQLFreshFixtures(t *testing.T, db *gorm.DB, tables ...any) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), lockReleaseTimeout)
		defer cancel()
		if err := db.WithContext(ctx).Migrator().DropTable(tables...); err != nil {
			t.Errorf("drop fresh fixtures: %v", err)
		}
	})
}

func testMySQLFreshDropFailure(t *testing.T, db *gorm.DB, pool, observer *sql.DB, initialChecks bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	tables := [2]string{"fresh_drop_a", "fresh_drop_b"}
	cleanMySQLFreshFixtures(t, db, tables[0], tables[1], &integrationUser{}, &Migration{})

	var blockers [2]*sql.Tx
	var statements [2]string
	for i, table := range tables {
		if err := db.WithContext(ctx).Exec("CREATE TABLE " + table + " (id INT PRIMARY KEY) ENGINE=InnoDB").Error; err != nil {
			t.Fatalf("create metadata lock fixture: %v", err)
		}
		tx, err := observer.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin metadata lock transaction: %v", err)
		}
		t.Cleanup(func() {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				t.Errorf("release metadata lock on %s: %v", table, err)
			}
		})
		var count int64
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("hold metadata lock on %s: %v", table, err)
		}
		blockers[i] = tx
		statements[i] = "DROP TABLE IF EXISTS " + "`" + table + "`"
	}
	before := mysqlForeignKeyCheckSessions(t, ctx, pool, &initialChecks)

	const file = "2026_03_24_120000_create_fresh_replay_table"
	dir := t.TempDir()
	writeIntegrationMigrationFile(t, dir, file)
	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		return tx.Migrator().CreateTable(&integrationUser{})
	}, nil)
	m := NewMigrator(dir, db, WithRegistry(registry), WithAllowFresh())

	done := make(chan struct{})
	var freshErr error
	go func() {
		defer close(done)
		freshErr = m.Fresh(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Whichever table comes first, unblock only that DROP. The second returns a server-side timeout.
	_, first := waitForMySQLFreshDrop(t, ctx, observer, statements, done)
	if err := blockers[first].Rollback(); err != nil {
		t.Fatalf("release the first metadata lock: %v", err)
	}
	<-done
	blocked := 1 - first
	if freshErr == nil || !strings.Contains(freshErr.Error(), "drop table "+tables[blocked]+": Error 1205 ") {
		t.Fatalf("expected a MySQL lock wait timeout dropping %s, got %v", tables[blocked], freshErr)
	}
	if after := mysqlForeignKeyCheckSessions(t, ctx, pool, nil); !maps.Equal(before, after) {
		t.Fatalf("foreign_key_checks changed on MySQL sessions: before=%v after=%v", before, after)
	}
	if err := blockers[blocked].Rollback(); err != nil {
		t.Fatalf("release the remaining metadata lock: %v", err)
	}
	migrator := db.WithContext(ctx).Migrator()
	if migrator.HasTable(tables[first]) || !migrator.HasTable(tables[blocked]) {
		t.Fatalf("expected %s to be dropped and %s to remain", tables[first], tables[blocked])
	}
	if migrator.HasTable(&integrationUser{}) || migrator.HasTable(&Migration{}) {
		t.Fatal("fresh started replay after a failed DROP")
	}
}

func waitForMySQLFreshDrop(t *testing.T, ctx context.Context, observer *sql.DB, statements [2]string, done <-chan struct{}) (uint64, int) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var id uint64
		var statement string
		err := observer.QueryRowContext(ctx,
			"SELECT ID, INFO FROM information_schema.PROCESSLIST WHERE DB = DATABASE() AND INFO IN (?, ?) LIMIT 1",
			statements[0], statements[1],
		).Scan(&id, &statement)
		if err == nil {
			if statement == statements[0] {
				return id, 0
			}
			return id, 1
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("observe MySQL DROP: %v", err)
		}
		select {
		case <-done:
			t.Fatal("fresh finished before a fixture DROP was observed")
		case <-ctx.Done():
			t.Fatalf("wait for fixture DROP: %v", ctx.Err())
		case <-ticker.C:
		}
	}
}

func mysqlForeignKeyCheckSessions(t *testing.T, ctx context.Context, pool *sql.DB, initial *bool) map[uint64]bool {
	t.Helper()
	checksBySession := make(map[uint64]bool, 2)
	// Hold both sessions until the snapshot is complete so the pool cannot reuse the first one.
	for range 2 {
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatalf("reserve MySQL session: %v", err)
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("return MySQL session: %v", err)
			}
		}()

		if initial != nil {
			if _, err := conn.ExecContext(ctx, "SET SESSION foreign_key_checks = ?, lock_wait_timeout = 3", *initial); err != nil {
				t.Fatalf("set initial MySQL session variables: %v", err)
			}
		}
		var id uint64
		var checks bool
		if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID(), @@SESSION.foreign_key_checks").Scan(&id, &checks); err != nil {
			t.Fatalf("read MySQL session and foreign_key_checks: %v", err)
		}
		if initial != nil && checks != *initial {
			t.Fatalf("session %d has foreign_key_checks=%t, want %t", id, checks, *initial)
		}
		checksBySession[id] = checks
	}
	return checksBySession
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
func (l *integrationDDLLogger) Info(context.Context, string, ...any)             {}
func (l *integrationDDLLogger) Warn(context.Context, string, ...any)             {}
func (l *integrationDDLLogger) Error(context.Context, string, ...any)            {}
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

// TestMigratorPostgresLockTimeoutDoesNotPoisonPool 验证等锁超时失败后，
// 归还连接池的连接不残留 statement_timeout（否则复用该连接的业务查询会被莫名取消）.
func TestMigratorPostgresLockTimeoutDoesNotPoisonPool(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MIGRATE_TEST_POSTGRES_DSN"))
	if dsn == "" {
		skipIntegration(t, "postgres integration test skipped: MIGRATE_TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if name := strings.ToLower(CurrentDatabase(db)); !strings.Contains(name, "test") && os.Getenv("MIGRATE_TEST_ALLOW_ANY_DB") != "1" {
		skipIntegration(t, "postgres integration test skipped: database %q does not look like a test database", name)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })

	key := hashLockName("poison_pool_test")
	holder := &postgresLock{db: db, lockKey: key, timeout: 5 * time.Second}
	release, err := holder.Acquire(context.Background())
	if err != nil {
		t.Fatalf("holder acquire: %v", err)
	}

	// waiter 以 1 秒超时等同一把锁，必然失败（statement_timeout=1000ms 生效在其专属连接上）.
	waiter := &postgresLock{db: db, lockKey: key, timeout: time.Second}
	if _, err := waiter.Acquire(context.Background()); err == nil {
		release()
		t.Fatalf("waiter should time out while holder keeps the lock")
	}
	release()

	// 连续两次覆盖池内两条连接：若失败路径残留 statement_timeout=1s，pg_sleep(1.5) 会被取消.
	for i := range 2 {
		if err := db.Exec("SELECT pg_sleep(1.5)").Error; err != nil {
			t.Fatalf("pooled connection %d appears poisoned by leftover statement_timeout: %v", i, err)
		}
	}
}

// TestMigratorMySQLFreshDropsViews 在真实 MySQL 上验证库中存在视图时 fresh 清理成功：
// 视图必须用 DROP VIEW 删除，混入 DROP TABLE 会直接报错.
func TestMigratorMySQLFreshDropsViews(t *testing.T) {
	db := openMySQLTestDB(t)

	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("initial cleanup: %v", err)
	}
	if err := db.Exec("CREATE TABLE vt_base (id INT PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := db.Exec("CREATE VIEW vt_view AS SELECT id FROM vt_base").Error; err != nil {
		t.Fatalf("create view: %v", err)
	}

	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("delete all tables with a view present: %v", err)
	}

	var remaining int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?", CurrentDatabase(db)).
		Scan(&remaining).Error; err != nil {
		t.Fatalf("count objects: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected all tables and views dropped, got %d remaining", remaining)
	}
}

// TestMigratorPostgresFreshScopedToPublic 在真实 PostgreSQL 上验证 fresh 只清 public：
// public 的表与视图被删除，其他 schema 的同名表不受影响（DROP 显式限定 schema，不依赖 search_path）.
func TestMigratorPostgresFreshScopedToPublic(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MIGRATE_TEST_POSTGRES_DSN"))
	if dsn == "" {
		skipIntegration(t, "postgres integration test skipped: MIGRATE_TEST_POSTGRES_DSN is not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if name := strings.ToLower(CurrentDatabase(db)); !strings.Contains(name, "test") && os.Getenv("MIGRATE_TEST_ALLOW_ANY_DB") != "1" {
		skipIntegration(t, "postgres integration test skipped: database %q does not look like a test database", name)
	}
	t.Cleanup(func() {
		_ = db.Exec("DROP SCHEMA IF EXISTS mig_other CASCADE").Error
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("initial cleanup: %v", err)
	}
	if err := db.Exec("DROP SCHEMA IF EXISTS mig_other CASCADE").Error; err != nil {
		t.Fatalf("drop other schema: %v", err)
	}
	if err := db.Exec("CREATE SCHEMA mig_other").Error; err != nil {
		t.Fatalf("create other schema: %v", err)
	}
	if err := db.Exec("CREATE TABLE pv_base (id INT PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create public table: %v", err)
	}
	if err := db.Exec("CREATE VIEW pv_view AS SELECT id FROM pv_base").Error; err != nil {
		t.Fatalf("create public view: %v", err)
	}
	if err := db.Exec("CREATE TABLE mig_other.pv_base (id INT PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create other-schema table: %v", err)
	}

	exec := func(t *testing.T, sql string) {
		t.Helper()
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, dependency := range []struct {
		name, setup, verify, remove string
	}{
		{
			"view", "CREATE VIEW mig_other.dependent_view AS SELECT id FROM public.pv_view",
			"SELECT to_regclass('mig_other.dependent_view') IS NOT NULL", "DROP VIEW mig_other.dependent_view",
		},
		{
			"foreign key", "ALTER TABLE mig_other.pv_base ADD CONSTRAINT pv_external_fk FOREIGN KEY (id) REFERENCES public.pv_base(id)",
			"SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'pv_external_fk' AND conrelid = 'mig_other.pv_base'::regclass)",
			"ALTER TABLE mig_other.pv_base DROP CONSTRAINT pv_external_fk",
		},
		{
			"partition", "CREATE TABLE mig_other.pv_partition PARTITION OF public.pv_parent FOR VALUES FROM (0) TO (10)",
			"SELECT to_regclass('mig_other.pv_partition') IS NOT NULL", "DROP TABLE mig_other.pv_partition",
		},
		{
			"external partition parent", "CREATE TABLE public.pv_child PARTITION OF mig_other.pv_parent FOR VALUES FROM (0) TO (10)",
			"SELECT to_regclass('public.pv_child') IS NOT NULL AND to_regclass('mig_other.pv_parent') IS NOT NULL", "DROP TABLE public.pv_child",
		},
	} {
		t.Run(dependency.name, func(t *testing.T) {
			switch dependency.name {
			case "partition":
				exec(t, "CREATE TABLE public.pv_parent (id INT) PARTITION BY RANGE (id)")
			case "external partition parent":
				exec(t, "CREATE TABLE mig_other.pv_parent (id INT) PARTITION BY RANGE (id)")
			}
			exec(t, dependency.setup)
			t.Cleanup(func() {
				if err := db.Exec(dependency.remove).Error; err != nil {
					t.Error(err)
				}
			})
			if err := DeleteAllTables(db); err == nil {
				t.Fatal("cleanup unexpectedly accepted an external dependency")
			}
			var exists bool
			if err := db.Raw(dependency.verify).Scan(&exists).Error; err != nil || !exists {
				t.Fatalf("external dependency must survive: exists=%t err=%v", exists, err)
			}
			if err := db.Raw("SELECT to_regclass('public.pv_view') IS NOT NULL AND to_regclass('public.pv_base') IS NOT NULL").
				Scan(&exists).Error; err != nil || !exists {
				t.Fatalf("public cleanup must roll back: exists=%t err=%v", exists, err)
			}
		})
	}
	exec(t, "CREATE TABLE public.pv_ref (id INT PRIMARY KEY REFERENCES public.pv_base(id))")
	exec(t, "ALTER TABLE public.pv_base ADD FOREIGN KEY (id) REFERENCES public.pv_ref(id)")

	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("delete all tables: %v", err)
	}

	var n int64
	if err := db.Raw("SELECT COUNT(*) FROM pg_tables WHERE schemaname = 'public'").Scan(&n).Error; err != nil || n != 0 {
		t.Fatalf("public tables should be gone, count=%d err=%v", n, err)
	}
	if err := db.Raw("SELECT COUNT(*) FROM pg_views WHERE schemaname = 'public'").Scan(&n).Error; err != nil || n != 0 {
		t.Fatalf("public views should be gone, count=%d err=%v", n, err)
	}
	if err := db.Raw("SELECT COUNT(*) FROM pg_tables WHERE schemaname = 'mig_other' AND tablename = 'pv_base'").
		Scan(&n).Error; err != nil || n != 1 {
		t.Fatalf("same-named table in another schema must survive, count=%d err=%v", n, err)
	}
}

// TestMigratorMySQLLegacyGuardedCreateSelfHeals 在真实 MySQL 上验证旧版 create 模板的 HasTable 守卫：
// MySQL DDL 隐式提交，Up 在 CreateTable 之后失败会留下"表已建、记录未写"；
// 带守卫的 up 重跑时跳过建表、正常写记录，无守卫则报 Table already exists.
func TestMigratorMySQLLegacyGuardedCreateSelfHeals(t *testing.T) {
	db := openMySQLTestDB(t)
	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("initial cleanup: %v", err)
	}

	type healSnapshot struct {
		ID int64 `gorm:"column:id;primaryKey;autoIncrement"`
	}
	const table = "heal_users"
	calls := 0
	registry := NewRegistry()
	registry.Add("2026_03_24_120000_create_heal_users_table", func(tx *gorm.DB) error {
		calls++
		if tx.Migrator().HasTable(table) {
			return nil
		}
		if err := tx.Table(table).Migrator().CreateTable(&healSnapshot{}); err != nil {
			return err
		}
		if calls == 1 {
			return fmt.Errorf("simulated crash after DDL commit")
		}
		return nil
	}, func(tx *gorm.DB) error {
		return tx.Migrator().DropTable(table)
	})

	m := NewMigrator(t.TempDir(), db, WithRegistry(registry),
		WithLockName(fmt.Sprintf("migrate_heal_%d", time.Now().UnixNano())), WithLogger(&NopLogger{}))

	if err := m.Up(t.Context()); err == nil {
		t.Fatalf("first Up should fail after the DDL was committed")
	}
	if !db.Migrator().HasTable(table) {
		t.Fatalf("MySQL should have committed the CREATE TABLE despite the failure")
	}
	statuses, err := m.Status(t.Context())
	if err != nil || len(statuses) != 1 || statuses[0].Ran {
		t.Fatalf("migration must not be recorded after failure, got %+v err=%v", statuses, err)
	}

	if err := m.Up(t.Context()); err != nil {
		t.Fatalf("second Up should self-heal through the HasTable guard, got %v", err)
	}
	upToDate, err := m.IsUpToDate(t.Context())
	if err != nil || !upToDate {
		t.Fatalf("IsUpToDate after self-heal = (%v, %v), want (true, nil)", upToDate, err)
	}
	if calls != 2 {
		t.Fatalf("up should have run exactly twice, got %d", calls)
	}
}

// TestMigratorMySQLDerivedLockName 在真实 MySQL 上验证默认锁名派生：
// 锁名为 migrate:<库名>:<账本表名> 且确实被持有；不同账本表拿到不同锁互不阻塞；
// 同账本表等待超时返回 ErrLockNotAcquired；释放后锁消失.
func TestMigratorMySQLDerivedLockName(t *testing.T) {
	db := openMySQLTestDB(t)
	expected := deriveMySQLLockName(CurrentDatabase(db), defaultTableName)

	m := NewMigrator(t.TempDir(), db, WithRegistry(NewRegistry()), WithLogger(&NopLogger{}))
	release, err := m.lock.Acquire(t.Context())
	if err != nil {
		t.Fatalf("acquire derived lock: %v", err)
	}
	var holder *int64
	if err := db.Raw("SELECT IS_USED_LOCK(?)", expected).Scan(&holder).Error; err != nil {
		t.Fatalf("query IS_USED_LOCK: %v", err)
	}
	if holder == nil {
		t.Fatalf("derived lock %q should be held", expected)
	}

	other := NewMigrator(t.TempDir(), db, WithRegistry(NewRegistry()), WithLogger(&NopLogger{}),
		WithMigrationsTable("svc2_migrations"), WithLockTimeout(2*time.Second))
	releaseOther, err := other.lock.Acquire(t.Context())
	if err != nil {
		t.Fatalf("a different ledger table must derive a different lock and not block: %v", err)
	}
	releaseOther()

	same := NewMigrator(t.TempDir(), db, WithRegistry(NewRegistry()), WithLogger(&NopLogger{}), WithLockTimeout(time.Second))
	if _, err := same.lock.Acquire(t.Context()); !errors.Is(err, ErrLockNotAcquired) {
		t.Fatalf("same ledger table must wait and fail with ErrLockNotAcquired, got %v", err)
	}

	release()
	if err := db.Raw("SELECT IS_USED_LOCK(?)", expected).Scan(&holder).Error; err != nil {
		t.Fatalf("query IS_USED_LOCK after release: %v", err)
	}
	if holder != nil {
		t.Fatalf("derived lock %q should be released", expected)
	}
}

func TestMigratorMySQLTwoConnectionsWithLegacyDropTable(t *testing.T) {
	db := openMySQLTestDB(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(2)
	registry := NewRegistry()
	name := "2026_03_24_120000_create_migrate_pool_regression"
	table := "migrate_pool_regression"
	if err := db.Exec("DROP TABLE IF EXISTS " + quoteMySQLIdent(table)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Exec("DROP TABLE IF EXISTS " + quoteMySQLIdent(table)).Error; err != nil {
			t.Error(err)
		}
		if err := db.Exec("DROP TABLE IF EXISTS migrate_pool_ledger").Error; err != nil {
			t.Error(err)
		}
	})
	registry.Add(name, func(tx *gorm.DB) error {
		return tx.Exec("CREATE TABLE " + quoteMySQLIdent(table) + " (id INT PRIMARY KEY)").Error
	}, func(tx *gorm.DB) error {
		return tx.Migrator().DropTable(table)
	})
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry), WithMigrationsTable("migrate_pool_ledger"))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil || len(status) != 1 || !status[0].Ran || status[0].Batch != 1 || !db.Migrator().HasTable("migrate_pool_ledger") {
		t.Fatalf("expected the custom ledger to record the migration: %+v, %v", status, err)
	}
	if err := m.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable(table) {
		t.Fatal("legacy Down did not drop the table")
	}
	records, err := m.readLedger(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("expected empty ledger, got %v: %v", records, err)
	}
	if status, err := m.Status(ctx); err != nil || len(status) != 1 || status[0].Ran {
		t.Fatalf("expected pending status after rollback in the custom ledger: %+v, %v", status, err)
	}

	registry.Add("2026_03_24_120001_drop_migrate_pool_regression", func(tx *gorm.DB) error {
		return tx.Migrator().DropTable(table)
	}, nil)
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if db.Migrator().HasTable(table) {
		t.Fatal("legacy Up did not drop the table")
	}
}

func TestMySQLGeneratedIndexDefinitionGuard(t *testing.T) {
	db := openMySQLTestDB(t)
	const table = "migrate_index_guard"
	exec := func(sql string) {
		t.Helper()
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { exec("DROP TABLE IF EXISTS " + table) })
	exec("CREATE TABLE " + table + " (email VARCHAR(64), code VARCHAR(64), KEY idx_guard (email, code), KEY idx_prefix (code(10))) ENGINE=InnoDB")
	if match, err := MySQLIndexMatches(db, table, "idx_guard", false, "email", "code"); err != nil || !match {
		t.Fatalf("matching index: match=%t err=%v", match, err)
	}
	if match, err := MySQLIndexMatches(db, table, "idx_missing", false, "email"); err != nil || match {
		t.Fatalf("absent index: match=%t err=%v", match, err)
	}
	for _, test := range []struct {
		index   string
		unique  bool
		columns []string
	}{
		{"idx_guard", true, []string{"email", "code"}},
		{"idx_guard", false, []string{"code", "email"}},
		{"idx_prefix", false, []string{"code"}},
	} {
		if match, err := MySQLIndexMatches(db, table, test.index, test.unique, test.columns...); err == nil || match {
			t.Errorf("different definition was accepted: %+v", test)
		}
	}
}

func TestMigratorMySQLGeneratedTemplatesReconcileObjects(t *testing.T) {
	testMySQLGeneratedReconciliation(t, openMySQLTestDB(t))
}

type reconciledSnapshotModel struct {
	ID    uint64 `gorm:"primaryKey;autoIncrement"`
	Email string `gorm:"type:varchar(80);not null;default:guest;uniqueIndex:idx_reconcile_snapshot_email"`
	Score int32  `gorm:"type:int;not null;default:0;check:reconcile_snapshot_score,score >= 0"`
}

func (reconciledSnapshotModel) TableName() string { return "reconcile_snapshot" }

func testMySQLGeneratedReconciliation(t *testing.T, db *gorm.DB) {
	t.Helper()
	sourceDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := os.ReadFile(filepath.Join(sourceDir, "testdata", "template_conflict_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	previous := makecmd.CurrentConfig()
	t.Cleanup(func() { makecmd.SetConfig(previous) })
	makecmd.SetConfig(makecmd.Config{
		ProjectName: "example.com/template-reconciliation",
		DDLModels:   []any{&reconciledSnapshotModel{}},
	})
	tables := []any{"reconcile_created", "reconcile_snapshot", "reconcile_columns", "reconcile_indexes", "reconcile_created_history", "reconcile_adopted_history"}
	for i := range 4 {
		tables = append(tables, fmt.Sprintf("reconcile_conflict_%d", i))
	}
	cleanMySQLFreshFixtures(t, db, tables...)
	for _, args := range [][]string{
		{"create_reconcile_created_table"},
		{"create_reconcile_snapshot_table", "--from-model"},
		{"add_label_to_reconcile_columns_table", "--after", "id", "--type", "VARCHAR(32)", "--not-null", "--default", "'guest'", "--comment", "label comment"},
		{"add_index_email_to_reconcile_indexes_table", "--unique"},
	} {
		t.Run("generate_"+args[0], func(t *testing.T) {
			for _, name := range []string{"after", "type", "not-null", "default", "comment", "unique", "from-model"} {
				flag := makecmd.CmdMakeMigration.Flags().Lookup(name)
				if flag == nil {
					continue
				}
				value, changed := flag.Value.String(), flag.Changed
				t.Cleanup(func() {
					if err := flag.Value.Set(value); err != nil {
						t.Error(err)
					}
					flag.Changed = changed
				})
			}
			makecmd.CmdMake.SetArgs(append([]string{"migration"}, args...))
			t.Cleanup(func() { makecmd.CmdMake.SetArgs(nil) })
			if err := makecmd.CmdMake.Execute(); err != nil {
				t.Fatal(err)
			}
		})
	}
	if t.Failed() {
		return
	}
	migrationDir := filepath.Join(dir, "database", "migrations")
	files, err := filepath.Glob(filepath.Join(migrationDir, "2*.go"))
	if err != nil || len(files) != 4 {
		t.Fatalf("expected four generated migrations: %v, %v", files, err)
	}
	testFile := filepath.Join(migrationDir, "template_reconciliation_test.go")
	if err := os.WriteFile(testFile, runner, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "go", append([]string{"test", "-race", "-count=1"}, append(files, testFile)...)...)
	cmd.Dir = sourceDir
	cmd.Env = append(cmd.Environ(), "GOWORK=off", "MIGRATE_RECONCILE_DIR="+migrationDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile and run generated MySQL migrations: %v\n%s", err, output)
	}
}
