package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMigratorLintDetectsDiskRegistryAndDBDrift(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	dir := t.TempDir()
	writeMigrationFile(t, dir, "2026_03_24_120000_create_users_table", "package migrations\n")
	writeMigrationFile(t, dir, "2026_03_24_120001_update_users_table", "package migrations\n")
	writeMigrationFile(t, dir, "2026_03_24_120002_drop_email_from_users_table", "package migrations\nfunc x() error { return Irreversible(\"manual down required\") }\n")

	registry := NewRegistry()
	registry.Add("2026_03_24_120000_create_users_table", func(*gorm.DB) error { return nil }, nil)
	registry.Add("2026_03_24_120003_create_orders_table", func(*gorm.DB) error { return nil }, func(*gorm.DB) error { return nil })

	m := NewMigrator(dir, db, WithRegistry(registry))

	if err := db.AutoMigrate(&Migration{}); err != nil {
		t.Fatalf("auto migrate migration table: %v", err)
	}
	if err := db.Create(&Migration{Migration: "2026_03_24_120004_create_legacy_table", Batch: 1}).Error; err != nil {
		t.Fatalf("insert migration record: %v", err)
	}

	report, err := m.Lint(t.Context(), LintOptions{})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	if !report.HasErrors() {
		t.Fatalf("expected lint errors, got %#v", report.Issues)
	}
	if !report.HasWarnings() {
		t.Fatalf("expected lint warnings, got %#v", report.Issues)
	}

	assertLintHasIssue(t, report.Issues, "unregistered_file", "2026_03_24_120001_update_users_table")
	assertLintHasIssue(t, report.Issues, "missing_file", "2026_03_24_120003_create_orders_table")
	assertLintHasIssue(t, report.Issues, "applied_missing_file", "2026_03_24_120004_create_legacy_table")
	assertLintHasIssue(t, report.Issues, "applied_unregistered", "2026_03_24_120004_create_legacy_table")
	assertLintHasIssue(t, report.Issues, "missing_down", "2026_03_24_120000_create_users_table")
	assertLintHasIssue(t, report.Issues, "irreversible_migration", "2026_03_24_120002_drop_email_from_users_table")
}

func TestMigratorLintDetectsDuplicateTimestamps(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	dir := t.TempDir()
	writeMigrationFile(t, dir, "2026_03_24_120000_create_users_table", "package migrations\n")
	writeMigrationFile(t, dir, "2026_03_24_120000_create_orders_table", "package migrations\n")

	registry := NewRegistry()
	registry.Add("2026_03_24_120000_create_users_table", func(*gorm.DB) error { return nil }, func(*gorm.DB) error { return nil })
	registry.Add("2026_03_24_120000_create_orders_table", func(*gorm.DB) error { return nil }, func(*gorm.DB) error { return nil })

	report, err := NewMigrator(dir, db, WithRegistry(registry)).Lint(t.Context(), LintOptions{SkipDatabase: true})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	assertLintHasIssue(t, report.Issues, "duplicate_timestamp", "2026_03_24_120000_create_orders_table, 2026_03_24_120000_create_users_table")
}

func writeMigrationFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name+".go")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertLintHasIssue(t *testing.T, issues []LintIssue, code, name string) {
	t.Helper()
	for _, issue := range issues {
		if issue.Code == code && issue.Name == name {
			return
		}
	}

	var builder strings.Builder
	for _, issue := range issues {
		builder.WriteString(string(issue.Severity))
		builder.WriteString(":")
		builder.WriteString(issue.Code)
		builder.WriteString(":")
		builder.WriteString(issue.Name)
		builder.WriteString("\n")
	}

	t.Fatalf("expected lint issue %s/%s, got:\n%s", code, name, builder.String())
}
