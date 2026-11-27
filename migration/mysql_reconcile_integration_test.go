//go:build integration

package migration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtkit/migrate/internal/snapshot"
	makecmd "github.com/gtkit/migrate/make"
	"gorm.io/gorm"
)

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
	runner, err := os.ReadFile(filepath.Join(sourceDir, "testdata", "template_reconciliation_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	t.Cleanup(snapshot.Reset)
	snapshot.Register(&reconciledSnapshotModel{})
	tables := []any{"reconcile_created", "reconcile_snapshot", "reconcile_columns", "reconcile_indexes", "reconcile_created_history", "reconcile_adopted_history"}
	for i := range 4 {
		tables = append(tables, fmt.Sprintf("reconcile_conflict_%d", i))
	}
	cleanMySQLIntegrationTables(t, db, tables...)
	for _, args := range [][]string{
		{"create_reconcile_created_table"},
		{"create_reconcile_snapshot_table", "--from-model"},
		{"add_label_to_reconcile_columns_table", "--after", "id"},
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
	for _, file := range files {
		if !strings.HasSuffix(file, "_add_label_to_reconcile_columns_table.go") {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		content := strings.Replace(string(source), `const definition = ""`,
			`const definition = "VARCHAR(32) NOT NULL DEFAULT 'guest' COMMENT 'label comment'"`, 1)
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
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
