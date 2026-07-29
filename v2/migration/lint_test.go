package migration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// newMySQLLintMigrator 构造 dbType 视为 MySQL 的 Migrator，
// 用于测试 MySQL 专属的在线 DDL lint 规则（SQLite 测试库下该规则默认跳过）.
func newMySQLLintMigrator(dir string, db *gorm.DB, registry *Registry) *Migrator {
	m := NewMigrator(dir, db, WithRegistry(registry))
	m.dbType = DBTypeMySQL
	return m
}

func TestMigratorLintDetectsDiskRegistryAndDBDrift(t *testing.T) {
	db := openExecTestDB(t, t.Name())

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
	db := openExecTestDB(t, t.Name())

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

func TestMigratorLintDetectsContentIssues(t *testing.T) {
	db := openExecTestDB(t, t.Name())

	dir := t.TempDir()
	todoFile := "2026_03_24_120000_add_email_to_users_table"
	autoFile := "2026_03_24_120001_update_users_table"
	importFile := "2026_03_24_120002_create_orders_table"

	writeMigrationFile(t, dir, todoFile,
		"package migrations\n\nfunc up() { _ = \"ALTER TABLE `users` ADD COLUMN `email` /* TODO: 列定义 */\" }\n")
	writeMigrationFile(t, dir, autoFile,
		"package migrations\n\nfunc up() { db.AutoMigrate(&X{}) }\n")
	writeMigrationFile(t, dir, importFile,
		"package migrations\n\nimport \"example.com/app/internal/models\"\n")

	registry := NewRegistry()
	noop := func(*gorm.DB) error { return nil }
	registry.Add(todoFile, noop, noop)
	registry.Add(autoFile, noop, noop)
	registry.Add(importFile, noop, noop)

	report, err := newMySQLLintMigrator(dir, db, registry).Lint(t.Context(), LintOptions{SkipDatabase: true})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	assertLintHasIssue(t, report.Issues, "unfilled_placeholder", todoFile)
	assertLintHasIssue(t, report.Issues, "missing_online_ddl", todoFile)
	assertLintHasIssue(t, report.Issues, "automigrate_used", autoFile)
	assertLintHasIssue(t, report.Issues, "non_self_contained", importFile)
}

func TestMigratorLintDestructiveAndCleanMigration(t *testing.T) {
	db := openExecTestDB(t, t.Name())

	dir := t.TempDir()
	dropFile := "2026_03_24_120000_drop_users_table"
	cleanFile := "2026_03_24_120001_add_phone_to_users_table"

	writeMigrationFile(t, dir, dropFile,
		"package migrations\n\nfunc up() { _ = \"DROP TABLE `users`\" }\n")
	// 完整、自包含、带在线 DDL 策略、无 TODO 的迁移不应产生任何归属它的问题。
	writeMigrationFile(t, dir, cleanFile,
		"package migrations\n\nimport (\n\t\"gorm.io/gorm\"\n\n\t\"github.com/gtkit/migrate/v2/migration\"\n)\n\nfunc up(db *gorm.DB) error {\n\treturn db.Exec(\"ALTER TABLE `users` ADD COLUMN `phone` VARCHAR(32) NOT NULL DEFAULT '', ALGORITHM=INPLACE, LOCK=NONE\").Error\n}\n\nvar _ = migration.Add\n")

	registry := NewRegistry()
	noop := func(*gorm.DB) error { return nil }
	registry.Add(dropFile, noop, noop)
	registry.Add(cleanFile, noop, noop)

	report, err := NewMigrator(dir, db, WithRegistry(registry)).Lint(t.Context(), LintOptions{SkipDatabase: true})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	assertLintHasIssue(t, report.Issues, "destructive_migration", dropFile)

	for _, issue := range report.Issues {
		if issue.Name == cleanFile {
			t.Fatalf("complete self-contained migration should have no issues, got %s/%s: %s", issue.Code, issue.Name, issue.Message)
		}
	}
}

// TestMigratorLintOnlineDDLDetection 验证在线 DDL 判定：Go/SQL 注释里的
// ALGORITHM/LOCK 关键词不算已标注策略，只有真实 SQL 子句才算.
func TestMigratorLintOnlineDDLDetection(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		wantFlagged bool
	}{
		{
			// Go 注释里有关键词，真实 SQL 字符串没有 → 仍应报.
			name:        "go_comment_only",
			content:     "package migrations\n\nfunc up() {\n\t// 建议标注 ALGORITHM=INPLACE, LOCK=NONE\n\t_ = \"ALTER TABLE `users` ADD COLUMN `a` INT\"\n}\n",
			wantFlagged: true,
		},
		{
			// 在线 DDL 策略写在 SQL 块注释里 → 仍应报.
			name:        "sql_comment_only",
			content:     "package migrations\n\nfunc up() { _ = \"ALTER TABLE users ADD COLUMN x INT /* ALGORITHM=INPLACE, LOCK=NONE */\" }\n",
			wantFlagged: true,
		},
		{
			// SQL 字符串里是真实的 ALGORITHM 与 LOCK 子句 → 不应报.
			name:        "real_clause",
			content:     "package migrations\n\nfunc up() { _ = \"ALTER TABLE `users` ADD COLUMN `b` INT, ALGORITHM=INPLACE, LOCK=NONE\" }\n",
			wantFlagged: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openExecTestDB(t, t.Name())
			dir := t.TempDir()
			file := "2026_03_24_120000_add_a_to_users_table"
			writeMigrationFile(t, dir, file, tc.content)

			registry := NewRegistry()
			noop := func(*gorm.DB) error { return nil }
			registry.Add(file, noop, noop)

			report, err := newMySQLLintMigrator(dir, db, registry).Lint(t.Context(), LintOptions{SkipDatabase: true})
			if err != nil {
				t.Fatalf("lint: %v", err)
			}

			flagged := false
			for _, issue := range report.Issues {
				if issue.Name == file && issue.Code == "missing_online_ddl" {
					flagged = true
				}
			}
			if flagged != tc.wantFlagged {
				t.Fatalf("missing_online_ddl flagged = %v, want %v; issues: %#v", flagged, tc.wantFlagged, report.Issues)
			}
		})
	}
}

func TestMigratorLintDetectsDuplicateRegistration(t *testing.T) {
	db := openExecTestDB(t, t.Name())

	dir := t.TempDir()
	name := "2026_03_24_120000_create_users_table"
	writeMigrationFile(t, dir, name, "package migrations\n")

	registry := NewRegistry()
	noop := func(*gorm.DB) error { return nil }
	registry.Add(name, noop, noop)
	registry.Add(name, noop, noop) // 重复注册

	report, err := NewMigrator(dir, db, WithRegistry(registry)).Lint(t.Context(), LintOptions{SkipDatabase: true})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}
	assertLintHasIssue(t, report.Issues, "duplicate_registration", name)
}

func TestMigratorLintOnlineDDLMatchesRealClauses(t *testing.T) {
	db := openExecTestDB(t, t.Name())
	dir := t.TempDir()

	// 这些都缺真实在线 DDL 子句，应报 missing_online_ddl.
	bad := map[string]string{
		"2026_03_24_120000_col_name":   "ALTER TABLE users ADD COLUMN algorithm INT, ADD COLUMN lock INT",                     // algorithm/lock 只是列名
		"2026_03_24_120001_str_val":    "ALTER TABLE users ADD COLUMN note VARCHAR(64) DEFAULT 'algorithm lock'",              // 出现在字符串值里
		"2026_03_24_120002_hash":       "ALTER TABLE users ADD COLUMN x INT # ALGORITHM=INPLACE, LOCK=NONE",                   // 写在 # 注释里
		"2026_03_24_120004_str_clause": "ALTER TABLE users ADD COLUMN note VARCHAR(64) DEFAULT 'algorithm=INPLACE lock=NONE'", // 写在字符串值里
	}
	registry := NewRegistry()
	noop := func(*gorm.DB) error { return nil }
	for name, sql := range bad {
		writeMigrationFile(t, dir, name, "package migrations\n\nfunc up() { _ = \""+sql+"\" }\n")
		registry.Add(name, noop, noop)
	}
	// 真实子句，不应报.
	realName := "2026_03_24_120003_real"
	writeMigrationFile(t, dir, realName, "package migrations\n\nfunc up() { _ = \"ALTER TABLE users ADD COLUMN y INT, ALGORITHM=INPLACE, LOCK=NONE\" }\n")
	registry.Add(realName, noop, noop)

	report, err := newMySQLLintMigrator(dir, db, registry).Lint(t.Context(), LintOptions{SkipDatabase: true})
	if err != nil {
		t.Fatalf("lint: %v", err)
	}

	for name := range bad {
		assertLintHasIssue(t, report.Issues, "missing_online_ddl", name)
	}
	for _, issue := range report.Issues {
		if issue.Name == realName && issue.Code == "missing_online_ddl" {
			t.Fatalf("real ALGORITHM+LOCK clause must not be flagged, got: %s", issue.Message)
		}
	}
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
