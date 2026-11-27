package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

type migrationSessionConnector struct {
	query func(string, []driver.NamedValue) (driver.Rows, error)
	exec  func(string, []driver.NamedValue) (driver.Result, error)
	count int
}

func (c *migrationSessionConnector) Connect(context.Context) (driver.Conn, error) {
	c.count++
	return migrationSessionConn{c}, nil
}
func (*migrationSessionConnector) Driver() driver.Driver { return nil }

type migrationSessionConn struct{ *migrationSessionConnector }

func (migrationSessionConn) Close() error { return nil }
func (migrationSessionConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}

func (migrationSessionConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected begin") }

func (c migrationSessionConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.query(query, args)
}

func (c migrationSessionConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.exec(query, args)
}

type migrationSessionRow struct {
	value driver.Value
	read  bool
}

func (*migrationSessionRow) Columns() []string { return []string{"value"} }
func (*migrationSessionRow) Close() error      { return nil }
func (r *migrationSessionRow) Next(values []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	values[0] = r.value
	return nil
}

func openMigrationSessionTest(t *testing.T, connector *migrationSessionConnector) *gorm.DB {
	t.Helper()
	pool := sql.OpenDB(connector)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	return &gorm.DB{Config: &gorm.Config{ConnPool: pool}}
}

func TestMySQLLockReleaseSurvivesBusinessCancellation(t *testing.T) {
	var locked bool
	connector := &migrationSessionConnector{query: func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(query, "GET_LOCK") && !locked:
			locked = true
		case strings.Contains(query, "RELEASE_LOCK") && locked:
			locked = false
		default:
			return nil, fmt.Errorf("unexpected lock query: %s", query)
		}
		return &migrationSessionRow{value: int64(1)}, nil
	}}
	lock := &mysqlLock{db: openMigrationSessionTest(t, connector), lockName: "test_lock", timeout: time.Second}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release, err := lock.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	release()
	if locked || connector.count != 1 {
		t.Fatalf("lock still held or cleanup changed connection: held=%t connections=%d", locked, connector.count)
	}
}

func TestMySQLCleanupRestoresOriginalForeignKeyChecks(t *testing.T) {
	dropError := errors.New("drop failed")
	for _, original := range []bool{true, false} {
		for _, fail := range []bool{true, false} {
			t.Run(fmt.Sprintf("original=%t/fail=%t", original, fail), func(t *testing.T) {
				current := original
				connector := &migrationSessionConnector{
					query: func(query string, _ []driver.NamedValue) (driver.Rows, error) {
						if strings.Contains(query, "information_schema") {
							return &migrationSessionRow{value: "test_table"}, nil
						}
						return &migrationSessionRow{value: current}, nil
					},
					exec: func(query string, args []driver.NamedValue) (driver.Result, error) {
						switch query {
						case "SET SESSION foreign_key_checks = 0":
							current = false
						case "DROP TABLE IF EXISTS `test_table`":
							if current {
								return nil, errors.New("foreign key checks still enabled")
							}
							if fail {
								return nil, dropError
							}
						case "SET SESSION foreign_key_checks = ?":
							current = args[0].Value == true
						default:
							return nil, fmt.Errorf("unexpected cleanup statement: %s", query)
						}
						return driver.RowsAffected(0), nil
					},
				}
				err := deleteMySQLTables(openMigrationSessionTest(t, connector))
				if (fail && !errors.Is(err, dropError)) || (!fail && err != nil) || current != original || connector.count != 1 {
					t.Fatalf("cleanup err=%v foreign_key_checks=%t connections=%d", err, current, connector.count)
				}
			})
		}
	}
}

func TestPostgresTransactionLockReleaseSurvivesBusinessCancellation(t *testing.T) {
	var statements []string
	connector := &migrationSessionConnector{exec: func(query string, _ []driver.NamedValue) (driver.Result, error) {
		statements = append(statements, query)
		return driver.RowsAffected(0), nil
	}}
	lock := &postgresLock{db: openMigrationSessionTest(t, connector), lockKey: 12, timeout: time.Second}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release, err := lock.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	release()
	want := []string{"BEGIN", "SET LOCAL statement_timeout = 1000", "SELECT pg_advisory_xact_lock($1)", "ROLLBACK"}
	if !slices.Equal(statements, want) || connector.count != 1 {
		t.Fatalf("unexpected lock transaction lifecycle: %v connections=%d", statements, connector.count)
	}
}
