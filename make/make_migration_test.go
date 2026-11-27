package make

import (
	"bytes"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedMigrationTemplatesCompile(t *testing.T) {
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)

	flag := CmdMakeMigration.Flags().Lookup("after")
	previous := flag.Value.String()
	t.Cleanup(func() {
		if err := flag.Value.Set(previous); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct {
		name, after string
		want        []string
	}{
		{"create_audit_log_table", "", []string{`MySQLTableMatches(db, "audit_log", &snapshot{})`, `db.Table("audit_log").Migrator().CreateTable(&snapshot{})`}},
		{"update_audit_log_table", "", []string{`fill in the schema change for audit_log`}},
		{"rename_users_to_accounts_table", "", []string{
			`RenameTable("users", "accounts")`,
			`RenameTable("accounts", "users")`,
			`, up, down)`,
		}},
		{"rename_audit_logs_to_event_log_table", "", []string{
			`RenameTable("audit_logs", "event_log")`,
			`RenameTable("event_log", "audit_logs")`,
		}},
		{"add_email_to_audit_log_table", "", []string{`MySQLColumnMatches(db, table.Name, column.Name, definition, "")`, `ADD COLUMN ? ?", table, column, gorm.Expr(definition)`}},
		{"add_region_to_audit_log_table", "email", []string{`after := clause.Column{Name: "email"}`, `MySQLColumnMatches(db, table.Name, column.Name, definition, after.Name)`}},
		{"add_index_email_to_audit_log_table", "", []string{`MySQLIndexMatches(db, "audit_log", "idx_audit_log_email", false, "email")`, "ADD INDEX `idx_audit_log_email`"}},
		{"drop_column_email_from_audit_log_table", "", []string{`DROP COLUMN ?`}},
		{"drop_index_idx_audit_email_from_audit_log_table", "", []string{`DropIndex("audit_log", "idx_audit_email")`}},
		{"drop_audit_log_table", "", []string{`DropTable("audit_log")`}},
	} {
		if err := flag.Value.Set(tc.after); err != nil {
			t.Fatal(err)
		}
		if err := runMakeMigration(CmdMakeMigration, []string{tc.name}); err != nil {
			t.Fatal(err)
		}
		files, err := filepath.Glob(filepath.Join(dir, "database", "migrations", "*_"+tc.name+".go"))
		if err != nil || len(files) != 1 {
			t.Fatalf("expected one generated migration for %s: %v (%v)", tc.name, files, err)
		}
		source, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		formatted, err := format.Source(source)
		if err != nil || !bytes.Equal(source, formatted) {
			t.Fatalf("%s is not gofmt-clean: %v", tc.name, err)
		}
		content := string(source)
		if strings.Contains(content, "{{") || strings.Contains(content, "/internal/models") {
			t.Fatalf("unexpected generated source for %s:\n%s", tc.name, source)
		}
		for _, want := range tc.want {
			if !strings.Contains(content, want) {
				t.Fatalf("generated migration %s lacks %q:\n%s", tc.name, want, source)
			}
		}
		if tc.name == "create_audit_log_table" {
			baseModel, err := os.ReadFile(filepath.Join(dir, "internal", "models", "model.go"))
			if err != nil {
				t.Fatal(err)
			}
			for _, column := range []string{"created_at", "updated_at", "deleted_at"} {
				tag := []byte(`gorm:"column:` + column + `;type:datetime;`)
				if !bytes.Contains(baseModel, tag) {
					t.Errorf("generated base model lacks explicit datetime type for %s", column)
				}
				if !bytes.Contains(source, tag) {
					t.Errorf("generated create-table snapshot lacks explicit datetime type for %s", column)
				}
			}
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "database", "migrations", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "go", append([]string{"test"}, files...)...)
	cmd.Dir = packageDir
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile generated migrations: %v\n%s", err, output)
	}
}
