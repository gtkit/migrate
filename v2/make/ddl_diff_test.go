package make

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func executeMakeCommandWithError(args ...string) error {
	CmdMake.SilenceErrors = true
	CmdMake.SilenceUsage = true
	CmdMake.SetArgs(args)
	return CmdMake.Execute()
}

type ddlUserDiff struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Email     string    `gorm:"column:email;size:128;not null;uniqueIndex"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (ddlUserDiff) TableName() string {
	return "users"
}
