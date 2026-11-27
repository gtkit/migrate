package make

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMakeDDLDiffDetectsDrift(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}

	SetConfig(Config{
		ProjectName: "example.com/testapp",
		DB:          db,
		DDLModels:   []any{&ddlUser{}},
	})

	if err := os.MkdirAll(filepath.Join(tmpDir, "database/ddl"), 0o755); err != nil {
		t.Fatalf("mkdir ddl dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "database/ddl/create_users_table.sql"), []byte("CREATE TABLE users (id integer);\n"), 0o644); err != nil {
		t.Fatalf("write stale ddl: %v", err)
	}

	err = executeMakeCommandWithError("ddl", "diff", "users")
	if !errors.Is(err, ErrDDLDiffFound) {
		t.Fatalf("expected ErrDDLDiffFound, got %v", err)
	}
}

func TestMakeDDLDiffPassesWhenCurrent(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}

	SetConfig(Config{
		ProjectName: "example.com/testapp",
		DB:          db,
		DDLModels:   []any{&ddlUser{}},
	})

	expected, err := GenerateCreateTableDDL(db, &ddlUser{})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, "database/ddl"), 0o755); err != nil {
		t.Fatalf("mkdir ddl dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "database/ddl/create_users_table.sql"), []byte(expected), 0o644); err != nil {
		t.Fatalf("write ddl: %v", err)
	}

	if err := executeMakeCommandWithError("ddl", "diff", "users"); err != nil {
		t.Fatalf("expected no diff, got %v", err)
	}
}

func TestRenderDDLTextDiff(t *testing.T) {
	diff := renderDDLTextDiff("CREATE TABLE users (\n  id integer\n);\n", "CREATE TABLE users (\n  id integer,\n  email text\n);\n")
	if !strings.Contains(diff, "--- existing") {
		t.Fatalf("diff should include existing header, got:\n%s", diff)
	}
	if !strings.Contains(diff, "+++ generated") {
		t.Fatalf("diff should include generated header, got:\n%s", diff)
	}
	if !strings.Contains(diff, "+  email text") {
		t.Fatalf("diff should include inserted line, got:\n%s", diff)
	}
}

func TestMakeDDLRejectsLinkedSnapshot(t *testing.T) {
	resetMakeTestState(t)
	dir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	SetConfig(Config{DB: db, DDLModels: []any{&ddlUser{}}, DDLDir: dir})

	snapshot := "external snapshot fixture"
	target := filepath.Join(t.TempDir(), "external.sql")
	if err := os.WriteFile(target, []byte(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "create_users_table.sql")); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"ddl", "diff", "users"}, {"ddl", "users", "--force"}} {
		if err := executeMakeCommandWithError(args...); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("%v accepted a symlink: %v", args, err)
		}
	}
	if got := readFile(t, target); got != snapshot {
		t.Fatalf("linked snapshot was modified: %q", got)
	}
}

type ddlPathLikeTable struct{ ID uint }

func (ddlPathLikeTable) TableName() string { return "subdir/../../outside" }

func TestMakeDDLRejectsPathLikeTableNames(t *testing.T) {
	resetMakeTestState(t)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ddl")
	SetConfig(Config{DB: db, DDLModels: []any{&ddlPathLikeTable{}}, DDLDir: dir})
	for _, args := range [][]string{{"ddl", "diff", "--all"}, {"ddl", "--all"}} {
		if err := executeMakeCommandWithError(args...); err == nil || !strings.Contains(err.Error(), "DDL snapshot filename") {
			t.Fatalf("%v accepted table name with path separators: %v", args, err)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid table name created a DDL directory: %v", err)
	}
}

func executeMakeCommandWithError(args ...string) error {
	CmdMake.SilenceErrors = true
	CmdMake.SilenceUsage = true
	CmdMake.SetArgs(args)
	return CmdMake.Execute()
}
