package make

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeRejectsMalformedMigrationNamesAndPosition(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	flag := CmdMakeMigration.Flags().Lookup("after")
	previous := flag.Value.String()
	t.Cleanup(func() {
		if err := flag.Value.Set(previous); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct{ name, after string }{
		{"create_table", ""},
		{"create", ""},
		{"rename_users_table", ""},
		{"rename__to_accounts_table", ""},
		{"rename_users_to__table", ""},
		{"rename_1users_to_accounts_table", ""},
		{"rename_users_to_accounts-2026_table", ""},
		{"rename_users_to_users_table", ""},
		{"rename_users_to_accounts_to_members_table", ""},
		{"rename_users_to_to_accounts_table", ""},
		{"rename_users_to_accounts_table", "name"},
		{"add_email_table", ""},
		{"add_email_to_table", ""},
		{"drop_email_from_users_table", ""},
		{"drop_column_email_from_users_table", "id"},
		{"add_email_to_users_table", "not-a-column"},
	} {
		t.Run(tc.name+"/"+tc.after, func(t *testing.T) {
			if err := flag.Value.Set(tc.after); err != nil {
				t.Fatal(err)
			}
			if err := runMakeMigration(CmdMakeMigration, []string{tc.name}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat("database"); !os.IsNotExist(err) {
				t.Fatalf("invalid migration created an output directory: %v", err)
			}
		})
	}
}

func TestMakeScaffoldKeepsEditedFilesAndFormatsNewOnes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	initial := projectName
	t.Cleanup(func() { SetProjectName(initial) })
	SetProjectName("example.com/scaffold")
	SetProjectName("")
	runMakeModel(CmdMakeModel, []string{"audit_events"})
	runMakeCMD(CmdMakeCMD, []string{"rebuild_cache"})
	for path, want := range map[string]string{
		"internal/models/model.go":                            "type BaseID struct",
		"internal/models/audit_event.go":                      "type AuditEvent struct",
		"internal/repository/audit_event/audit_event_i.go":    "type Repository struct",
		"internal/repository/audit_event/audit_event_util.go": "example.com/scaffold/internal/models",
		"cmd/rebuild_cache.go":                                "CmdRebuildCache",
	} {
		content, err := os.ReadFile(filepath.FromSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		formatted, err := format.Source(content)
		if err != nil || !bytes.Equal(content, formatted) || !strings.Contains(string(content), want) {
			t.Fatalf("unexpected generated scaffold %s: %v\n%s", path, err, content)
		}
	}

	const model = "internal/models/audit_event.go"
	edited := []byte("package models\n\n// User-maintained model.\n")
	if err := os.WriteFile(model, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	runMakeModel(CmdMakeModel, []string{"audit_events"})
	got, err := os.ReadFile(model)
	if err != nil || !bytes.Equal(got, edited) {
		t.Fatalf("scaffolding overwrote an existing model: %v\n%s", err, got)
	}
}
