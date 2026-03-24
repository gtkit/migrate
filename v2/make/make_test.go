package make

import (
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMakeModelGeneratesReferenceLayoutByDefault(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	SetConfig(Config{
		ProjectName: "example.com/testapp",
	})

	executeMakeCommand(t, "model", "user")

	expectedFiles := []string{
		filepath.Join(tmpDir, "internal/models/model.go"),
		filepath.Join(tmpDir, "internal/models/doc.go"),
		filepath.Join(tmpDir, "internal/models/user.go"),
		filepath.Join(tmpDir, "internal/repository/user/repository.go"),
		filepath.Join(tmpDir, "internal/repository/user/repository_util.go"),
	}

	for _, filePath := range expectedFiles {
		assertFileExists(t, filePath)
		assertGoFileParses(t, filePath)
	}

	modelContent := readFile(t, filepath.Join(tmpDir, "internal/models/user.go"))
	if !strings.Contains(modelContent, "package models") {
		t.Fatalf("generated model should use models package, got:\n%s", modelContent)
	}
	if !strings.Contains(modelContent, `return "users"`) {
		t.Fatalf("generated model should declare users table, got:\n%s", modelContent)
	}

	repositoryContent := readFile(t, filepath.Join(tmpDir, "internal/repository/user/repository_util.go"))
	if !strings.Contains(repositoryContent, `"example.com/testapp/internal/models"`) {
		t.Fatalf("repository should import configured models path, got:\n%s", repositoryContent)
	}
}

func TestMakeMigrationSupportsCustomDirectories(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	SetConfig(Config{
		ProjectName: "example.com/customapp",
	})

	executeMakeCommand(t,
		"--model-dir", "internal/entities",
		"--repository-dir", "internal/data/repositories",
		"--migration-dir", "db/migrations",
		"migration", "create_users_table",
	)

	assertFileExists(t, filepath.Join(tmpDir, "internal/entities/model.go"))
	assertFileExists(t, filepath.Join(tmpDir, "internal/entities/user.go"))
	assertFileExists(t, filepath.Join(tmpDir, "internal/data/repositories/user/repository.go"))
	assertFileExists(t, filepath.Join(tmpDir, "db/migrations/doc.go"))

	migrations, err := filepath.Glob(filepath.Join(tmpDir, "db/migrations/*_create_users_table.go"))
	if err != nil {
		t.Fatalf("glob migration file: %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("expected exactly one generated migration, got %d", len(migrations))
	}
	assertGoFileParses(t, migrations[0])

	modelContent := readFile(t, filepath.Join(tmpDir, "internal/entities/user.go"))
	if !strings.Contains(modelContent, "package entities") {
		t.Fatalf("custom model directory should drive package name, got:\n%s", modelContent)
	}

	migrationContent := readFile(t, migrations[0])
	if !strings.Contains(migrationContent, `"example.com/customapp/internal/entities"`) {
		t.Fatalf("migration should import custom models path, got:\n%s", migrationContent)
	}
}

func TestMakeMigrationDropColumnMarksIrreversibleDown(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	SetConfig(Config{
		ProjectName: "example.com/testapp",
	})

	executeMakeCommand(t, "migration", "drop_column_email_from_users_table")

	migrations, err := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_drop_column_email_from_users_table.go"))
	if err != nil {
		t.Fatalf("glob migration file: %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("expected exactly one generated migration, got %d", len(migrations))
	}

	content := readFile(t, migrations[0])
	if !strings.Contains(content, `migration.Irreversible("fill in the column recreation`) {
		t.Fatalf("drop column migration should require manual down logic, got:\n%s", content)
	}
}

func TestMakeDDLGeneratesSQLFile(t *testing.T) {
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

	executeMakeCommand(t, "ddl", "users")

	ddlPath := filepath.Join(tmpDir, "database/ddl/create_users_table.sql")
	assertFileExists(t, ddlPath)

	content := readFile(t, ddlPath)
	upper := strings.ToUpper(content)
	if !strings.Contains(upper, "CREATE TABLE") {
		t.Fatalf("DDL should contain CREATE TABLE statement, got:\n%s", content)
	}
	if !strings.Contains(content, "users") {
		t.Fatalf("DDL should reference users table, got:\n%s", content)
	}
	if !strings.Contains(upper, "CREATE UNIQUE INDEX") && !strings.Contains(upper, "UNIQUE") {
		t.Fatalf("DDL should include unique constraint or index for email, got:\n%s", content)
	}
}

func resetMakeTestState(t *testing.T) {
	t.Helper()
	SetConfig(defaultConfig())
	resetCommandTree(CmdMake)
}

func resetCommandTree(cmd *cobra.Command) {
	cmd.SetArgs(nil)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	resetFlagSet(cmd.Flags())
	resetFlagSet(cmd.PersistentFlags())
	for _, child := range cmd.Commands() {
		resetCommandTree(child)
	}
}

func resetFlagSet(fs *pflag.FlagSet) {
	if fs == nil {
		return
	}
	fs.VisitAll(func(flag *pflag.Flag) {
		_ = flag.Value.Set(flag.DefValue)
		flag.Changed = false
	})
}

func executeMakeCommand(t *testing.T, args ...string) {
	t.Helper()
	CmdMake.SilenceErrors = true
	CmdMake.SilenceUsage = true
	CmdMake.SetArgs(args)
	if err := CmdMake.Execute(); err != nil {
		t.Fatalf("execute make command %q: %v", strings.Join(args, " "), err)
	}
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
}

func assertFileExists(t *testing.T, filePath string) {
	t.Helper()
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat %s: %v", filePath, err)
	}
	if info.IsDir() {
		t.Fatalf("%s is a directory, expected file", filePath)
	}
}

func assertGoFileParses(t *testing.T, filePath string) {
	t.Helper()
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, filePath, nil, parser.ParseComments); err != nil {
		t.Fatalf("parse generated file %s: %v", filePath, err)
	}
}

func readFile(t *testing.T, filePath string) string {
	t.Helper()
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read %s: %v", filePath, err)
	}
	return string(data)
}

type ddlUser struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Email     string    `gorm:"column:email;size:128;not null;uniqueIndex"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
}

func (ddlUser) TableName() string {
	return "users"
}
