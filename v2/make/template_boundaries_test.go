package make

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTemplateRenderingErrorsLeaveNoFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stub, want string
		model            Model
	}{
		{"unknown-stub", "missing_template", "read stub", Model{}},
		{"invalid-go-name", "cmd", "format generated", Model{StructName: "not-valid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "new-dir", "command.go")
			err := createFileFromStub(output, tc.stub, tc.model, writeFailIfExists, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
			if _, err := os.Stat(filepath.Dir(output)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed rendering created an output directory: %v", err)
			}
		})
	}
}

func TestGeneratedFileWriteModesPreserveExistingContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "migration.sql")
	if err := writeGeneratedFile(path, []byte("user changes"), writeFailIfExists); err != nil {
		t.Fatal(err)
	}
	assertContent := func(want string) {
		t.Helper()
		content, err := os.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatalf("content=%q, err=%v, want %q", content, err, want)
		}
	}
	updated := []byte("new generation")
	if err := writeGeneratedFile(path, updated, writeFailIfExists); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected collision error, got %v", err)
	}
	if err := writeGeneratedFile(path, updated, writeSkipIfExists); err != nil {
		t.Fatal(err)
	}
	assertContent("user changes")
	if err := writeGeneratedFile(path, updated, writeOverwrite); err != nil {
		t.Fatal(err)
	}
	assertContent("new generation")

	broken := filepath.Join(filepath.Dir(path), "broken.sql")
	if err := os.Symlink(filepath.Join(filepath.Dir(path), "missing", "migration.sql"), broken); err != nil {
		t.Fatal(err)
	}
	if err := writeGeneratedFile(broken, updated, writeFailIfExists); err == nil || !strings.Contains(err.Error(), "write ") {
		t.Fatalf("expected dangling target write error, got %v", err)
	}
}

func TestInvalidMigrationFlagsLeaveNoScaffold(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"drop column without definition", []string{"drop_column_email_from_users_table", "--not-null"}, "does not apply"},
		{"modify with column position", []string{"modify_email_of_users_table", "--after", "id"}, "does not apply"},
		{"modify without type", []string{"modify_email_of_users_table", "--default", "0"}, "require --type"},
		{"rename with after", []string{"rename_users_to_accounts_table", "--after", "id"}, "--after does not apply"},
		{"rename with table override", []string{"rename_users_to_accounts_table", "--table", "accounts"}, "--table does not apply"},
		{"rename from model", []string{"rename_users_to_accounts_table", "--from-model"}, "--from-model does not apply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetMakeTestState(t)
			t.Chdir(t.TempDir())
			CmdMake.SetArgs(append([]string{"migration"}, tc.args...))
			if err := CmdMake.Execute(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
			if _, err := os.Stat("database"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid migration created output: %v", err)
			}
		})
	}
}
