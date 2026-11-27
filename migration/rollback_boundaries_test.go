package migration

import (
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestRollbackStepsUsesApplicationOrderAcrossBatches(t *testing.T) {
	t.Parallel()
	db := openExecTestDB(t, "rollback_step_order")
	dir := t.TempDir()
	registry := NewRegistry()
	var downs []string
	add := func(name string) {
		t.Helper()
		writeMigrationFile(t, dir, name, "package migrations\n")
		registry.Add(name, nil, func(*gorm.DB) error {
			downs = append(downs, name)
			return nil
		})
	}
	const (
		first = "2026_11_26_120000_first"
		late  = "2026_11_26_120001_late"
		last  = "2026_11_26_120002_last"
	)
	add(last)
	add(first)
	m := NewMigrator(dir, db, WithRegistry(registry), WithLockName("rollback_order"),
		WithLogger(&NopLogger{}), WithMigrationsTable("step_history"))
	ctx := t.Context()
	if latest, err := m.IsUpToDate(ctx); err != nil || latest {
		t.Fatalf("unapplied database reported up to date: %t, %v", latest, err)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if latest, err := m.IsUpToDate(ctx); err != nil || !latest {
		t.Fatalf("applied database reported pending: %t, %v", latest, err)
	}
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	add(late)
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.RollbackSteps(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(downs, []string{late}) {
		t.Fatalf("rollback step selected %v", downs)
	}
	if pending, err := m.Pending(ctx); err != nil || !slices.Equal(pending, []string{late}) {
		t.Fatalf("unexpected pending migrations: %v, %v", pending, err)
	}
	status, err := m.Status(ctx)
	if err != nil || len(status) != 3 || !status[0].Ran || status[1].Ran || !status[2].Ran ||
		status[0].Batch != 1 || status[2].Batch != 1 {
		t.Fatalf("unexpected status: %+v, %v", status, err)
	}
	if err := m.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(downs, []string{late, last, first}) {
		t.Fatalf("reset did not reverse the applied order: %v", downs)
	}
	if err := m.Rollback(ctx); err != nil {
		t.Fatalf("empty ledger rollback: %v", err)
	}
	if !db.Migrator().HasTable("step_history") || db.Migrator().HasTable("migrations") {
		t.Fatal("step rollback did not use only the configured table")
	}
}

func TestInvalidMigrationsTablePreventsFresh(t *testing.T) {
	t.Parallel()
	db := openExecTestDB(t, "invalid_history")
	if err := db.Migrator().CreateTable(&execTestUser{}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	const file = "2026_11_26_120000_create_exec_test_users_table"
	writeMigrationFile(t, dir, file, "package migrations\n")
	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error { return tx.Migrator().CreateTable(&execTestUser{}) }, nil)
	m := NewMigrator(dir, db, WithRegistry(registry), WithMigrationsTable("history AS h"))
	for _, err := range []error{m.Setup(t.Context()), m.Fresh(t.Context())} {
		if err == nil || !strings.Contains(err.Error(), "invalid migrations table name") {
			t.Fatalf("expected table name validation error, got %v", err)
		}
	}
	if !db.Migrator().HasTable(&execTestUser{}) || db.Migrator().HasTable("migrations") {
		t.Fatal("invalid table name changed database tables")
	}
}

func TestRollbackLedgerDeleteFailureKeepsDataAndLogsFailure(t *testing.T) {
	t.Parallel()
	db := openExecTestDB(t, "rollback_delete_failure")
	dir := t.TempDir()
	const name = "2026_11_26_120000_create_rollback_guard"
	writeMigrationFile(t, dir, name, "package migrations\n")
	registry := NewRegistry()
	registry.Add(name, func(tx *gorm.DB) error {
		return tx.Exec("CREATE TABLE rollback_guard (value INTEGER)").Error
	}, func(tx *gorm.DB) error {
		return tx.Exec("DELETE FROM rollback_guard").Error
	})
	logger := &lifecycleLog{}
	m := NewMigrator(dir, db, WithRegistry(registry), WithLogger(logger))
	ctx := t.Context()
	if err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO rollback_guard VALUES (42)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_rollback BEFORE DELETE ON migrations " +
		"BEGIN SELECT RAISE(ABORT, 'fixture refuses ledger delete'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(ctx); err == nil || !strings.Contains(err.Error(), "delete migration record") {
		t.Fatalf("expected ledger deletion error, got %v", err)
	}
	var data int
	if err := db.Raw("SELECT value FROM rollback_guard").Scan(&data).Error; err != nil || data != 42 {
		t.Fatalf("failed rollback changed data: %d, %v", data, err)
	}
	status, err := m.Status(ctx)
	if err != nil || len(status) != 1 || !status[0].Ran {
		t.Fatalf("failed rollback changed its ledger entry: %+v, %v", status, err)
	}
	if len(logger.success) != 1 || len(logger.failed) != 1 || !strings.Contains(logger.failed[0], "fixture refuses ledger delete") {
		t.Fatalf("unexpected results in logs: success=%v failed=%v", logger.success, logger.failed)
	}
	if err := db.Exec("DROP TRIGGER reject_rollback").Error; err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(ctx); err != nil {
		t.Fatalf("retry after fixing the ledger: %v", err)
	}
}
