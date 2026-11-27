package migration

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestLedgerWriteFailuresRollbackCallbacksAndLogFailure(t *testing.T) {
	t.Parallel()
	db := openExecTestDB(t, "ledger_failure_outcome")
	registry := NewRegistry()
	const name = "2026_11_26_120000_create_ledger_guard"
	registry.Add(name, func(tx *gorm.DB) error {
		return tx.Exec("CREATE TABLE ledger_guard (value INTEGER)").Error
	}, func(tx *gorm.DB) error {
		return tx.Exec("DELETE FROM ledger_guard").Error
	})
	logger := &captureLogger{}
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry), WithLogger(logger), WithMigrationsTable("project_ledger"))
	ctx := t.Context()
	if err := m.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	runSQL := func(statement string) {
		t.Helper()
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	checkStatus := func(ran bool) {
		t.Helper()
		status, err := m.Status(ctx)
		if err != nil || len(status) != 1 || status[0].Ran != ran {
			t.Fatalf("unexpected ledger: %+v, %v", status, err)
		}
	}

	runSQL("CREATE TRIGGER block_insert BEFORE INSERT ON project_ledger " +
		"BEGIN SELECT RAISE(ABORT, 'fixture blocked insert'); END")
	if err := m.Up(ctx); err == nil || !strings.Contains(err.Error(), "record migration "+name) {
		t.Fatalf("expected ledger insert error, got %v", err)
	}
	if db.Migrator().HasTable("ledger_guard") {
		t.Fatal("failed ledger insert should undo Up")
	}
	checkStatus(false)
	runSQL("DROP TRIGGER block_insert")

	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	runSQL("INSERT INTO ledger_guard VALUES (42)")
	runSQL("CREATE TRIGGER block_delete BEFORE DELETE ON project_ledger " +
		"BEGIN SELECT RAISE(ABORT, 'fixture blocked delete'); END")
	if err := m.RollbackSteps(ctx, 1); err == nil || !strings.Contains(err.Error(), "delete migration record "+name) {
		t.Fatalf("expected ledger delete error, got %v", err)
	}
	checkStatus(true)
	var data int
	if err := db.Raw("SELECT value FROM ledger_guard").Scan(&data).Error; err != nil || data != 42 {
		t.Fatalf("failed ledger delete should undo Down: value=%d, err=%v", data, err)
	}
	if len(logger.errors) != 2 {
		t.Fatalf("expected one log per failure: %q", logger.errors)
	}
	for i, message := range []string{"migration failed", "rollback failed"} {
		if !strings.HasPrefix(logger.errors[i], message+" file="+name+" batch=1 duration=") {
			t.Fatalf("failure log lacks migration context: %s", logger.errors[i])
		}
	}
	successes := 0
	for _, line := range logger.infos {
		if strings.HasPrefix(line, "migrated ") || strings.HasPrefix(line, "rolled back ") {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("only the committed Up should log success: %q", logger.infos)
	}
	runSQL("DROP TRIGGER block_delete")
	if err := m.RollbackSteps(ctx, 1); err != nil {
		t.Fatal(err)
	}
	checkStatus(false)
}

func TestMarkAppliedRollsBackWholeBatchOnLedgerError(t *testing.T) {
	t.Parallel()
	db := openExecTestDB(t, "mark_applied_insert_failure")
	registry := NewRegistry()
	first, second := "2026_11_26_120000_first", "2026_11_26_120001_second"
	calls := 0
	for _, name := range []string{first, second} {
		registry.Add(name, func(*gorm.DB) error {
			calls++
			return nil
		}, nil)
	}
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry))
	ctx := t.Context()
	if err := m.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_second BEFORE INSERT ON migrations WHEN NEW.migration = '" + second +
		"' BEGIN SELECT RAISE(ABORT, 'fixture blocked second insert'); END").Error; err != nil {
		t.Fatal(err)
	}
	if marked, err := m.MarkApplied(ctx, ""); err == nil || !strings.Contains(err.Error(), second) || len(marked) != 0 {
		t.Fatalf("expected failed baseline: %v, %v", marked, err)
	}
	status, err := m.Status(ctx)
	if err != nil || len(status) != 2 || status[0].Ran || status[1].Ran || calls != 0 {
		t.Fatalf("partial baseline or callback execution: %+v, calls=%d, err=%v", status, calls, err)
	}
}

func TestRollbackEntriesRejectUnreadableLedger(t *testing.T) {
	t.Parallel()
	db := openExecTestDB(t, "unreadable_rollback_order")
	const name = "2026_11_26_120000_guard"
	if err := db.Exec("CREATE TABLE migrations (migration TEXT PRIMARY KEY, batch INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO migrations (migration, batch) VALUES (?, 1)", name).Error; err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	downCalls := 0
	registry.Add(name, func(*gorm.DB) error { return nil }, func(*gorm.DB) error {
		downCalls++
		return nil
	})
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry))
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		run  func() error
		want string
	}{
		{"last-batch", func() error { return m.Rollback(ctx) }, "get last migration"},
		{"steps", func() error { return m.RollbackSteps(ctx, 1) }, "get migrations to rollback"},
		{"target", func() error { return m.RollbackTo(ctx, name) }, "get migrations to rollback"},
		{"reset", func() error { return m.Reset(ctx) }, "get all migrations"},
		{"refresh", func() error { return m.Refresh(ctx) }, "get all migrations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
	var count int
	if err := db.Raw("SELECT count(*) FROM migrations").Scan(&count).Error; err != nil || count != 1 || downCalls != 0 {
		t.Fatalf("unreadable ledger must preserve the entry and forbid Down: count=%d calls=%d err=%v", count, downCalls, err)
	}
}

func TestFreshRejectsSingleConnectionMySQLBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()
	db := openOfflineMySQLDialect(t)
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	registry := NewRegistry()
	registry.Add("2026_11_26_120000_guard", func(*gorm.DB) error {
		t.Error("Up executed even though migration pool is too small")
		return nil
	}, nil)
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry), WithAllowFresh())
	if err := m.Fresh(t.Context()); err == nil || !strings.Contains(err.Error(), "at least 2 connections") {
		t.Fatalf("expected pool capacity error, got %v", err)
	}
	if got := pool.Stats().OpenConnections; got != 0 {
		t.Fatalf("pool rejection should not open any connections, got %d", got)
	}
}
