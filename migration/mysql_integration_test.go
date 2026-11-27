//go:build integration

package migration

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestMigratorMySQLIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("MIGRATE_TEST_MYSQL_DSN"))
	if dsn == "" {
		if os.Getenv("MIGRATE_TEST_REQUIRE_DATABASES") == "1" {
			t.Fatal("MIGRATE_TEST_MYSQL_DSN is required for MySQL integration")
		}
		t.Skip("MIGRATE_TEST_MYSQL_DSN is not set")
	}

	db, observer := openMySQLIntegrationDB(t, dsn)

	var name string
	if err := db.WithContext(t.Context()).Raw("SELECT DATABASE()").Row().Scan(&name); err != nil {
		t.Fatalf("read MySQL test database name: %v", err)
	}
	if !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("MySQL integration requires a database name containing test, got %q", name)
	}

	// Serialize separate test processes sharing a DSN before checking for fixtures.
	suiteLock := mysqlLock{db: db, lockName: "migrate_v1_integration_suite", timeout: defaultLockTimeout}
	release, err := suiteLock.Acquire(t.Context())
	if err != nil {
		t.Fatalf("lock MySQL integration suite: %v", err)
	}
	t.Cleanup(release)

	var occupied bool
	if err := db.WithContext(t.Context()).Raw(
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE())",
	).Row().Scan(&occupied); err != nil {
		t.Fatalf("check MySQL test database: %v", err)
	}
	if occupied {
		t.Fatalf("MySQL integration requires an empty test database, got %q", name)
	}

	t.Run("up_and_rollback", func(t *testing.T) {
		cleanMySQLIntegrationTables(t, db, &execTestUser{}, &Migration{})
		testMigratorUpAndRollback(t, db, "migrate_v1_integration_migration")
	})
	t.Run("custom_migrations_table", func(t *testing.T) {
		testMySQLCustomMigrationsTable(t, db)
	})
	t.Run("generated_reconciliation", func(t *testing.T) {
		testMySQLGeneratedReconciliation(t, db)
	})
	t.Run("lock_contention", func(t *testing.T) {
		testMySQLIntegrationLockContention(t, db)
	})
	t.Run("lock_release_after_timeout", func(t *testing.T) {
		cleanMySQLIntegrationTables(t, db, &Migration{})
		testMySQLIntegrationLockReleaseAfterTimeout(t, db)
	})
	t.Run("fresh_foreign_key_checks", func(t *testing.T) {
		t.Run("initially_enabled", func(t *testing.T) {
			testMySQLIntegrationFresh(t, dsn, true)
		})
		t.Run("initially_disabled", func(t *testing.T) {
			testMySQLIntegrationFresh(t, dsn, false)
		})
	})
	t.Run("fresh_drop_failure", func(t *testing.T) {
		t.Run("initially_enabled", func(t *testing.T) {
			testMySQLFreshDropFailure(t, dsn, observer, true)
		})
		t.Run("initially_disabled", func(t *testing.T) {
			testMySQLFreshDropFailure(t, dsn, observer, false)
		})
	})
	t.Run("fresh_discards_connection", func(t *testing.T) {
		testMySQLFreshDiscardsConnection(t, dsn, observer)
	})
}

func testMySQLCustomMigrationsTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	const (
		history = "integration_migration_history"
		file    = "2026_11_27_120000_create_exec_test_users_table"
	)
	cleanMySQLIntegrationTables(t, db, &execTestUser{}, &Migration{}, history)
	if err := db.Migrator().CreateTable(&Migration{}); err != nil {
		t.Fatal(err)
	}
	// A matching name in the default ledger must not hide an unapplied migration.
	original := Migration{Migration: file, Batch: 99}
	if err := db.Create(&original).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeMigrationFile(t, dir, file, "package migrations\n")
	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		return tx.Migrator().CreateTable(&execTestUser{})
	}, func(tx *gorm.DB) error {
		return tx.Migrator().DropTable(&execTestUser{})
	})
	m := NewMigrator(dir, db, WithRegistry(registry), WithLockName("migrate_v1_custom_history_test"),
		WithMigrationsTable("  "+history+"  "))
	ctx := t.Context()
	if statuses, err := m.Status(ctx); err != nil || len(statuses) != 1 || statuses[0].Ran {
		t.Fatalf("status before up: %+v, %v", statuses, err)
	}
	if !db.Migrator().HasTable(history) {
		t.Fatalf("expected %s to be created", history)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatalf("up using custom ledger: %v", err)
	}
	if statuses, err := m.Status(ctx); err != nil || len(statuses) != 1 || !statuses[0].Ran || statuses[0].Batch != 1 {
		t.Fatalf("status after up: %+v, %v", statuses, err)
	}
	if err := m.Rollback(ctx); err != nil {
		t.Fatalf("rollback using custom ledger: %v", err)
	}
	if statuses, err := m.Status(ctx); err != nil || len(statuses) != 1 || statuses[0].Ran {
		t.Fatalf("status after rollback: %+v, %v", statuses, err)
	}
	var preserved Migration
	if err := db.First(&preserved, original.ID).Error; err != nil ||
		preserved.Migration != original.Migration || preserved.Batch != original.Batch {
		t.Fatalf("the default ledger record changed: %+v, %v", preserved, err)
	}
	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatal("rollback left the migrated table in place")
	}
}

func openMySQLIntegrationDB(t *testing.T, dsn string) (*gorm.DB, *sql.DB) {
	t.Helper()
	pool, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("invalid MIGRATE_TEST_MYSQL_DSN")
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close MySQL test connection pool: %v", err)
		}
	})
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open MySQL test database: %v", err)
	}
	return db, pool
}

func cleanMySQLIntegrationTables(t *testing.T, db *gorm.DB, tables ...any) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), defaultLockTimeout)
		defer cancel()
		if err := db.WithContext(ctx).Migrator().DropTable(tables...); err != nil {
			t.Errorf("drop MySQL integration fixtures: %v", err)
		}
	})
}

func testMySQLIntegrationLockContention(t *testing.T, db *gorm.DB) {
	t.Helper()
	const lockName = "migrate_v1_integration_contention"
	const timeout = time.Second
	holder := NewMigrator(t.TempDir(), db, WithLockName(lockName))
	waiter := NewMigrator(t.TempDir(), db, WithLockName(lockName), WithLockTimeout(timeout))

	if t.Run("while_held", func(t *testing.T) {
		release, err := holder.acquireLock(t.Context())
		if err != nil {
			t.Fatalf("acquire holding lock: %v", err)
		}
		defer release()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		start := time.Now()
		if releaseWaiter, err := waiter.acquireLock(ctx); err == nil {
			releaseWaiter()
			t.Fatal("waiter acquired the lock while the holder still held it")
		}
		if waited := time.Since(start); waited < timeout/2 || ctx.Err() != nil {
			t.Fatalf("waited %s for a lock with configured timeout %s", waited, timeout)
		}
	}) {
		requireMySQLIntegrationLockAvailable(t, waiter)
	}
}

func testMySQLIntegrationLockReleaseAfterTimeout(t *testing.T, db *gorm.DB) {
	t.Helper()
	const file = "2026_03_24_120000_wait_for_deadline"
	const lockName = "migrate_v1_integration_deadline"

	dir := t.TempDir()
	writeMigrationFile(t, dir, file, "package migrations\n")
	registry := NewRegistry()
	m := NewMigrator(dir, db, WithRegistry(registry), WithLockName(lockName))
	if err := m.Setup(t.Context()); err != nil {
		t.Fatalf("set up deadline test: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	entered := false
	registry.Add(file, func(tx *gorm.DB) error {
		entered = true
		<-ctx.Done()
		return tx.Statement.Context.Err()
	}, nil)

	if err := m.Up(ctx); !entered || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected Up to time out in its callback, entered=%t, err=%v", entered, err)
	}
	waiter := NewMigrator(dir, db, WithLockName(lockName), WithLockTimeout(time.Second))
	requireMySQLIntegrationLockAvailable(t, waiter)
}

func requireMySQLIntegrationLockAvailable(t *testing.T, m *Migrator) {
	t.Helper()
	var free bool
	if err := m.DB.WithContext(t.Context()).Raw("SELECT IS_FREE_LOCK(?)", m.lockName).Row().Scan(&free); err != nil {
		t.Fatalf("query MySQL advisory lock: %v", err)
	}
	if !free {
		t.Fatalf("MySQL advisory lock %q is still held", m.lockName)
	}
	release, err := m.acquireLock(t.Context())
	if err != nil {
		t.Fatalf("reacquire MySQL advisory lock: %v", err)
	}
	release()
}

func testMySQLIntegrationFresh(t *testing.T, dsn string, initialChecks bool) {
	t.Helper()
	// Fresh keeps one session locked and uses the other to drop and replay.
	db, pool := openMySQLIntegrationDB(t, dsn)
	pool.SetMaxOpenConns(2)
	cleanMySQLIntegrationTables(t, db, "fresh_fk_a", "fresh_fk_b", &execTestUser{}, &Migration{})
	for _, stmt := range []string{
		"CREATE TABLE fresh_fk_a (id INT PRIMARY KEY, b_id INT) ENGINE=InnoDB",
		"CREATE TABLE fresh_fk_b (id INT PRIMARY KEY, a_id INT, FOREIGN KEY (a_id) REFERENCES fresh_fk_a(id)) ENGINE=InnoDB",
		"ALTER TABLE fresh_fk_a ADD FOREIGN KEY (b_id) REFERENCES fresh_fk_b(id)",
	} {
		if err := db.WithContext(t.Context()).Exec(stmt).Error; err != nil {
			t.Fatalf("create foreign key fixture: %v", err)
		}
	}
	before := mysqlIntegrationSessionChecks(t, pool, &initialChecks)

	const file = "2026_03_24_120000_create_exec_test_users_table"
	dir := t.TempDir()
	writeMigrationFile(t, dir, file, "package migrations\n")
	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		return tx.Migrator().CreateTable(&execTestUser{})
	}, nil)
	m := NewMigrator(dir, db, WithRegistry(registry), WithLockName("migrate_v1_integration_fresh"))
	if err := m.Fresh(t.Context()); err != nil {
		t.Fatalf("fresh with foreign key fixtures: %v", err)
	}
	if after := mysqlIntegrationSessionChecks(t, pool, nil); !maps.Equal(before, after) {
		t.Fatalf("foreign_key_checks per session changed after fresh, before=%v, after=%v", before, after)
	}

	migrator := db.WithContext(t.Context()).Migrator()
	if migrator.HasTable("fresh_fk_a") || migrator.HasTable("fresh_fk_b") {
		t.Fatal("fresh left an old foreign key fixture table")
	}
	var count int64
	if err := db.WithContext(t.Context()).Model(&Migration{}).Where("migration = ?", file).Count(&count).Error; err != nil {
		t.Fatalf("read replayed migration: %v", err)
	}
	if !migrator.HasTable(&execTestUser{}) || count != 1 {
		t.Fatalf("fresh did not replay the migration, ledger count=%d", count)
	}
}

func testMySQLFreshDropFailure(t *testing.T, dsn string, observer *sql.DB, initialChecks bool) {
	t.Helper()
	db, pool := openMySQLIntegrationDB(t, dsn)
	pool.SetMaxOpenConns(2)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	tables := [2]string{"fresh_drop_a", "fresh_drop_b"}
	cleanMySQLIntegrationTables(t, db, tables[0], tables[1], &execTestUser{}, &Migration{})

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
	before := mysqlIntegrationSessionChecks(t, pool, &initialChecks)

	const file = "2026_03_24_120000_create_fresh_replay_table"
	dir := t.TempDir()
	writeMigrationFile(t, dir, file, "package migrations\n")
	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		return tx.Migrator().CreateTable(&execTestUser{})
	}, nil)
	m := NewMigrator(dir, db, WithRegistry(registry), WithLockName("migrate_v1_integration_fresh"))

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
	if after := mysqlIntegrationSessionChecks(t, pool, nil); !maps.Equal(before, after) {
		t.Fatalf("foreign_key_checks changed on MySQL sessions: before=%v after=%v", before, after)
	}
	if err := blockers[blocked].Rollback(); err != nil {
		t.Fatalf("release the remaining metadata lock: %v", err)
	}
	migrator := db.WithContext(ctx).Migrator()
	if migrator.HasTable(tables[first]) || !migrator.HasTable(tables[blocked]) {
		t.Fatalf("expected %s to be dropped and %s to remain", tables[first], tables[blocked])
	}
	if migrator.HasTable(&execTestUser{}) || migrator.HasTable(&Migration{}) {
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

func mysqlIntegrationSessionChecks(t *testing.T, pool *sql.DB, initial *bool) map[uint64]bool {
	t.Helper()
	checksBySession := make(map[uint64]bool, 2)
	for range 2 {
		conn, err := pool.Conn(t.Context())
		if err != nil {
			t.Fatalf("reserve MySQL session: %v", err)
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Errorf("return MySQL session: %v", err)
			}
		}()

		if initial != nil {
			if _, err := conn.ExecContext(t.Context(), "SET SESSION foreign_key_checks = ?, lock_wait_timeout = 3", *initial); err != nil {
				t.Fatalf("set initial MySQL session variables: %v", err)
			}
		}
		var id uint64
		var checks bool
		if err := conn.QueryRowContext(t.Context(), "SELECT CONNECTION_ID(), @@SESSION.foreign_key_checks").Scan(&id, &checks); err != nil {
			t.Fatalf("read MySQL session and foreign_key_checks: %v", err)
		}
		if initial != nil && checks != *initial {
			t.Fatalf("session %d has foreign_key_checks=%t, want %t", id, checks, *initial)
		}
		checksBySession[id] = checks
	}
	return checksBySession
}
