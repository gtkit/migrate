//go:build integration

package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	freshFaultTable = "fresh_discard_fixture"
	freshFaultLock  = "migrate_v1_fresh_fault_integration"
)

func testMySQLFreshDiscardsConnection(t *testing.T, dsn string, observer *sql.DB) {
	t.Helper()
	t.Run("disconnect_during_drop", func(t *testing.T) {
		testMySQLFreshConnectionFault(t, dsn, observer, false)
	})
	t.Run("restore_failure", func(t *testing.T) {
		testMySQLFreshConnectionFault(t, dsn, observer, true)
	})
}

func testMySQLFreshConnectionFault(t *testing.T, dsn string, observer *sql.DB, restoreFail bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	var lockID uint64
	var beforeRestore func(context.Context) error
	if restoreFail {
		beforeRestore = func(ctx context.Context) error {
			return observer.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", freshFaultLock).Scan(&lockID)
		}
	}
	db, pool := openMySQLFreshFaultDB(t, dsn, observer, beforeRestore)
	cleanMySQLIntegrationTables(t, db, freshFaultTable, &Migration{})
	if err := db.WithContext(ctx).Exec("CREATE TABLE " + freshFaultTable + " (id INT PRIMARY KEY) ENGINE=InnoDB").Error; err != nil {
		t.Fatalf("create fresh fixture: %v", err)
	}
	initialChecks := true
	before := mysqlIntegrationSessionChecks(t, pool, &initialChecks)

	const file = "2026_03_24_120000_fresh_connection_fault"
	dir := t.TempDir()
	writeMigrationFile(t, dir, file, "package migrations\n")
	registry := NewRegistry()
	replayed := false
	registry.Add(file, func(*gorm.DB) error {
		replayed = true
		return nil
	}, nil)
	m := NewMigrator(dir, db, WithRegistry(registry), WithLockName(freshFaultLock))

	var failedID uint64
	if restoreFail {
		err := m.Fresh(ctx)
		if err == nil || !strings.Contains(err.Error(), "restore foreign key checks: Error 1231 ") {
			t.Fatalf("expected MySQL to reject restoration, got %v", err)
		}
		if _, ok := before[lockID]; !ok {
			t.Fatalf("unexpected fresh lock session %d, original sessions %v", lockID, before)
		}
		// With the lock session reserved, the other original session runs table cleanup.
		for id := range before {
			if id != lockID {
				failedID = id
			}
		}
	} else {
		failedID = disconnectMySQLFreshDrop(t, ctx, observer, m, before)
	}
	after := mysqlIntegrationSessionChecks(t, pool, nil)
	requireMySQLFreshSessionDiscarded(t, ctx, observer, before, after, failedID)

	migrator := db.WithContext(ctx).Migrator()
	if exists := migrator.HasTable(freshFaultTable); exists != !restoreFail {
		t.Fatalf("fixture table exists=%t, want %t", exists, !restoreFail)
	}
	if replayed || migrator.HasTable(&Migration{}) {
		t.Fatal("fresh started replay after the cleanup failed")
	}
}

func disconnectMySQLFreshDrop(t *testing.T, ctx context.Context, observer *sql.DB, m *Migrator, sessions map[uint64]bool) uint64 {
	t.Helper()
	tx, err := observer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin metadata lock transaction: %v", err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("release metadata lock: %v", err)
		}
	})
	var count int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+freshFaultTable).Scan(&count); err != nil {
		t.Fatalf("hold fixture metadata lock: %v", err)
	}

	freshCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	var freshErr error
	go func() {
		defer close(done)
		freshErr = m.Fresh(freshCtx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	statement := "DROP TABLE IF EXISTS `" + freshFaultTable + "`"
	id, _ := waitForMySQLFreshDrop(t, ctx, observer, [2]string{statement, statement}, done)
	if _, ok := sessions[id]; !ok {
		t.Fatalf("fixture DROP is running in unexpected session %d", id)
	}
	if _, err := observer.ExecContext(ctx, fmt.Sprintf("KILL CONNECTION %d", id)); err != nil {
		t.Fatalf("disconnect fixture DROP session: %v", err)
	}
	<-done
	if freshErr == nil || !strings.Contains(freshErr.Error(), "drop table "+freshFaultTable+":") {
		t.Fatalf("expected DROP error after disconnect, got %v", freshErr)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("release metadata lock: %v", err)
	}
	return id
}

func requireMySQLFreshSessionDiscarded(t *testing.T, ctx context.Context, observer *sql.DB, before, after map[uint64]bool, id uint64) {
	t.Helper()
	_, wasPresent := before[id]
	_, remains := after[id]
	if !wasPresent || remains || len(before) != 2 || len(after) != 2 {
		t.Fatalf("cleanup session %d was not replaced: before=%v after=%v", id, before, after)
	}
	for otherID, original := range before {
		if otherID != id && after[otherID] != original {
			t.Fatalf("unaffected session %d changed: before=%v after=%v", otherID, before, after)
		}
	}

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var alive bool
		if err := observer.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.PROCESSLIST WHERE ID = ?)", id).Scan(&alive); err != nil {
			t.Fatalf("check cleanup session %d: %v", id, err)
		}
		if !alive {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("cleanup session %d remained open: %v", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

func openMySQLFreshFaultDB(t *testing.T, dsn string, observer *sql.DB, beforeRestore func(context.Context) error) (*gorm.DB, *sql.DB) {
	t.Helper()
	mysqlDriver, ok := observer.Driver().(driver.DriverContext)
	if !ok {
		t.Fatal("MySQL driver does not expose a connector")
	}
	connector, err := mysqlDriver.OpenConnector(dsn)
	if err != nil {
		t.Fatal("invalid MIGRATE_TEST_MYSQL_DSN")
	}
	if beforeRestore != nil {
		connector = freshRestoreFaultConnector{Connector: connector, beforeRestore: beforeRestore}
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(2)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close fresh test pool: %v", err)
		}
	})
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open fresh test database: %v", err)
	}
	return db, pool
}

type freshRestoreFaultConnector struct {
	driver.Connector
	beforeRestore func(context.Context) error
}

func (c freshRestoreFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	mysqlConn, ok := conn.(freshTestDriverConn)
	if !ok {
		return nil, errors.Join(errors.New("MySQL connection does not implement context query and exec"), conn.Close())
	}
	return freshRestoreFaultConn{freshTestDriverConn: mysqlConn, beforeRestore: c.beforeRestore}, nil
}

type freshTestDriverConn interface {
	driver.Conn
	driver.QueryerContext
	driver.ExecerContext
}

type freshRestoreFaultConn struct {
	freshTestDriverConn
	beforeRestore func(context.Context) error
}

func (c freshRestoreFaultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == "SET SESSION foreign_key_checks = ?" {
		if err := c.beforeRestore(ctx); err != nil {
			return nil, err
		}
		// MySQL rejects this value on the live session.
		query, args = "SET SESSION foreign_key_checks = 2", nil
	}
	return c.freshTestDriverConn.ExecContext(ctx, query, args)
}
