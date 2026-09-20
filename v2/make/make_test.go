package make

import (
	"errors"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
	// 脚手架只依赖标准库与 gorm：不得 import 旧项目私有包或额外 JSON 库.
	for _, filePath := range expectedFiles {
		content := readFile(t, filePath)
		for _, forbidden := range []string{"internal/pkg/paginator", "github.com/gtkit/json"} {
			if strings.Contains(content, forbidden) {
				t.Fatalf("%s must not import %s:\n%s", filePath, forbidden, content)
			}
		}
	}
	for _, method := range []string{
		"func (r *Repository) Get(ctx context.Context, id int64) (user models.User, found bool, err error)",
		"func (r *Repository) ExistsByID(ctx context.Context, id int64) (bool, error)",
		"func (r *Repository) All(ctx context.Context) (users []models.User, err error)",
		"func (r *Repository) CreateOrUpdate(",
		"gorm.ErrRecordNotFound",
	} {
		if !strings.Contains(repositoryContent, method) {
			t.Fatalf("repository should provide %q, got:\n%s", method, repositoryContent)
		}
	}
	for _, removed := range []string{"GetBy(", "IsExist(", "Paginate(", "ListPaging", "fmt.Sprintf(\"%s = ?\""} {
		if strings.Contains(repositoryContent, removed) {
			t.Fatalf("repository must not keep %q (swallowed errors / dynamic column names), got:\n%s", removed, repositoryContent)
		}
	}
	if !strings.Contains(modelContent, "strconv.FormatInt(user.ID, 10)") || !strings.Contains(modelContent, `"encoding/json"`) {
		t.Fatalf("model should use FormatInt and encoding/json, got:\n%s", modelContent)
	}
	docContent := readFile(t, filepath.Join(tmpDir, "internal/models/doc.go"))
	if !strings.HasPrefix(docContent, "// Package models ") {
		t.Fatalf("doc.go package comment must precede the package clause, got:\n%s", docContent)
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
	// create 迁移使用自包含的结构快照，不得引用业务 model 的 import 路径（避免 schema 随 model 演进漂移）。
	if strings.Contains(migrationContent, "example.com/customapp/internal/entities") {
		t.Fatalf("create migration must NOT import business model path, got:\n%s", migrationContent)
	}
	if !strings.Contains(migrationContent, `return "users"`) {
		t.Fatalf("create migration should lock table name via TableName, got:\n%s", migrationContent)
	}
	if !strings.Contains(migrationContent, "CreateTable(") {
		t.Fatalf("create migration should call CreateTable on the snapshot struct, got:\n%s", migrationContent)
	}
	// 幂等守卫必须在 CreateTable 之前：MySQL DDL 已提交但记录未写时重跑 up 才能自愈.
	guard := strings.Index(migrationContent, `HasTable("users")`)
	if guard < 0 || guard > strings.Index(migrationContent, "CreateTable(") {
		t.Fatalf("create migration should check HasTable before CreateTable, got:\n%s", migrationContent)
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

func TestMakeMigrationAddWithAfterGeneratesRawSQL(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	SetConfig(Config{ProjectName: "example.com/testapp"})

	executeMakeCommand(t, "migration", "--after", "pay_channel", "add_pay_mch_id_to_orders_table")

	migrations, err := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_add_pay_mch_id_to_orders_table.go"))
	if err != nil {
		t.Fatalf("glob migration file: %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("expected exactly one generated migration, got %d", len(migrations))
	}
	assertGoFileParses(t, migrations[0])

	content := readFile(t, migrations[0])
	if !strings.Contains(content, "ALTER TABLE `orders` ADD COLUMN `pay_mch_id`") {
		t.Fatalf("expected raw SQL ADD COLUMN, got:\n%s", content)
	}
	if !strings.Contains(content, "AFTER `pay_channel`") {
		t.Fatalf("expected AFTER clause, got:\n%s", content)
	}
	if strings.Contains(content, "AddColumn(") {
		t.Fatalf("raw SQL stub should not use Migrator().AddColumn, got:\n%s", content)
	}
}

func TestMakeMigrationAddWithoutAfterGeneratesRawSQL(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	SetConfig(Config{ProjectName: "example.com/testapp"})

	executeMakeCommand(t, "migration", "add_email_to_users_table")

	migrations, err := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_add_email_to_users_table.go"))
	if err != nil {
		t.Fatalf("glob migration file: %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("expected exactly one generated migration, got %d", len(migrations))
	}
	assertGoFileParses(t, migrations[0])

	content := readFile(t, migrations[0])
	if !strings.Contains(content, "ALTER TABLE `users` ADD COLUMN `email`") {
		t.Fatalf("default add (no --after) should generate raw SQL ADD COLUMN, got:\n%s", content)
	}
	if strings.Contains(content, "AddColumn(") {
		t.Fatalf("raw SQL stub should not use Migrator().AddColumn, got:\n%s", content)
	}
	if strings.Contains(content, "AFTER `") {
		t.Fatalf("add without --after should not contain an AFTER clause, got:\n%s", content)
	}
}

func TestMakeCmdGeneratesCommandFile(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	SetConfig(Config{ProjectName: "example.com/testapp"})

	executeMakeCommand(t, "cmd", "backup_database")

	files, err := filepath.Glob(filepath.Join(tmpDir, "cmd", "*.go"))
	if err != nil {
		t.Fatalf("glob command file: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly one generated command file, got %d", len(files))
	}
	assertGoFileParses(t, files[0])
	content := readFile(t, files[0])
	if !strings.Contains(content, "var CmdBackupDatabase = &cobra.Command{") || !strings.Contains(content, "RunE:  runBackupDatabase") {
		t.Fatalf("command stub should declare an exported RunE command, got:\n%s", content)
	}
	// 不自动注册（不假设调用方存在 rootCmd）、不依赖 console.
	for _, forbidden := range []string{"rootCmd", "console.", "func init()"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("command stub must not contain %q, got:\n%s", forbidden, content)
		}
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

// TestMakeMigrationTemplatesAreSelfContained 验证 add/update/drop/dropcolumn/dropindex
// 生成的迁移不 import 业务 model、update 不用 AutoMigrate，且可被 go/parser 解析。
func TestMakeMigrationTemplatesAreSelfContained(t *testing.T) {
	cases := []struct {
		arg  string
		glob string
	}{
		{"add_email_to_users_table", "*_add_email_to_users_table.go"},
		{"update_users_table", "*_update_users_table.go"},
		{"drop_users_table", "*_drop_users_table.go"},
		{"drop_column_email_from_users_table", "*_drop_column_email_from_users_table.go"},
		{"drop_index_email_from_users_table", "*_drop_index_email_from_users_table.go"},
	}

	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			resetMakeTestState(t)
			tmpDir := t.TempDir()
			chdirForTest(t, tmpDir)

			SetConfig(Config{ProjectName: "example.com/testapp"})

			executeMakeCommand(t, "migration", tc.arg)

			migrations, err := filepath.Glob(filepath.Join(tmpDir, "database/migrations", tc.glob))
			if err != nil {
				t.Fatalf("glob migration file: %v", err)
			}
			if len(migrations) != 1 {
				t.Fatalf("expected exactly one generated migration, got %d", len(migrations))
			}
			assertGoFileParses(t, migrations[0])

			content := readFile(t, migrations[0])
			if strings.Contains(content, "example.com/testapp") {
				t.Fatalf("%s migration must not import a project package, got:\n%s", tc.arg, content)
			}
			if strings.Contains(content, "AutoMigrate(") {
				t.Fatalf("%s migration must not use AutoMigrate, got:\n%s", tc.arg, content)
			}
		})
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

func TestSetProjectNameOverridesConfig(t *testing.T) {
	resetMakeTestState(t)
	SetConfig(Config{ProjectName: "example.com/old"})
	SetProjectName("example.com/new")
	if got := CurrentConfig().ProjectName; got != "example.com/new" {
		t.Fatalf("SetProjectName should override project name, got %q", got)
	}
	SetProjectName("") // 空值忽略
	if got := CurrentConfig().ProjectName; got != "example.com/new" {
		t.Fatalf("empty SetProjectName should be ignored, got %q", got)
	}
}

type snapshotTestBase struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement"`
}

type snapshotTestStatus int8

type snapshotTestUser struct {
	snapshotTestBase
	Name      string             `gorm:"column:name;size:64;not null"`
	Nick      *string            `gorm:"column:nick;size:32"`
	Status    snapshotTestStatus `gorm:"column:status;default:0"`
	Payload   []byte             `gorm:"column:payload;type:json"`
	secret    string             //nolint:unused // 故意保留：验证快照生成跳过未导出字段
	Ignored   string             `gorm:"-"`
	CreatedAt time.Time          `gorm:"column:created_at;type:datetime;index"`
	DeletedAt gorm.DeletedAt     `gorm:"column:deleted_at;index"`
}

func (snapshotTestUser) TableName() string { return "snapshot_users" }

// TestMakeMigrationCreateFromModel 验证 --from-model：字段来自注册 model（嵌入展平、跳过未导出与 gorm:"-"、
// []byte 渲染、tag 原样、无 TODO）、不生成脚手架、产物经 gofmt；表名不匹配时报错且不落盘.
func TestMakeMigrationCreateFromModel(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)
	SetConfig(Config{DDLModels: []any{&snapshotTestUser{}}})

	executeMakeCommand(t, "migration", "--from-model", "create_snapshot_users_table")

	migrations, err := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_create_snapshot_users_table.go"))
	if err != nil || len(migrations) != 1 {
		t.Fatalf("expected exactly one generated migration, got %v (%v)", migrations, err)
	}
	assertGoFileParses(t, migrations[0])
	content := readFile(t, migrations[0])

	formatted, err := format.Source([]byte(content))
	if err != nil || string(formatted) != content {
		t.Fatalf("generated file must be gofmt-clean (err=%v):\n%s", err, content)
	}
	// 字段行用正则匹配：gofmt 的列对齐宽度取决于最长字段名与类型名，不在此固化空格数.
	for _, want := range []string{
		`ID\s+int64\s+` + "`gorm:\"column:id;primaryKey;autoIncrement\"`",
		`Nick\s+\*string\s+` + "`gorm:\"column:nick;size:32\"`",
		`Status\s+make\.snapshotTestStatus\s+` + "`gorm:\"column:status;default:0\"`",
		`Payload\s+\[\]byte\s+` + "`gorm:\"column:payload;type:json\"`",
		`DeletedAt\s+gorm\.DeletedAt\s+` + "`gorm:\"column:deleted_at;index\"`",
		`return "snapshot_users"`,
		"\n\t\"time\"\n",
		"\n\t\"github.com/gtkit/migrate/v2/make\"\n",
		`HasTable\("snapshot_users"\)`,
	} {
		if !regexp.MustCompile(want).MatchString(content) {
			t.Errorf("generated snapshot missing %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"secret", "Ignored", "TODO", "snapshotTestBase"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("generated snapshot must not contain %q:\n%s", unwanted, content)
		}
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "internal")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--from-model must not scaffold model/repository, stat err=%v", err)
	}

	// 表名与注册 model 不一致：报错列出已注册表名，且不写任何迁移文件.
	resetMakeTestState(t)
	SetConfig(Config{DDLModels: []any{&snapshotTestUser{}}})
	CmdMake.SetArgs([]string{"migration", "--from-model", "create_orders_table"})
	err = CmdMake.Execute()
	if err == nil || !strings.Contains(err.Error(), "snapshot_users") || !strings.Contains(err.Error(), `"orders"`) {
		t.Fatalf("mismatched table should fail naming registered tables, got %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*orders*")); len(leftovers) != 0 {
		t.Fatalf("failed --from-model must not write files, got %v", leftovers)
	}

	// 未注册任何 model 时同样报错.
	resetMakeTestState(t)
	SetConfig(Config{})
	CmdMake.SetArgs([]string{"migration", "--from-model", "create_snapshot_users_table"})
	if err := CmdMake.Execute(); err == nil {
		t.Fatalf("--from-model without registered models should error")
	}
}

// TestMakeMigrationAddWithColumnDefinition 验证 --type/--not-null/--default/--comment（可与 --after 组合）
// 生成完整 SQL、无 TODO，且 COMMENT 中的单引号被转义.
func TestMakeMigrationAddWithColumnDefinition(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	executeMakeCommand(t, "migration", "add_email_to_users_table",
		"--type", "VARCHAR(128)", "--not-null", "--default", "''", "--comment", "用户'邮箱", "--after", "name")

	migrations, err := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_add_email_to_users_table.go"))
	if err != nil || len(migrations) != 1 {
		t.Fatalf("expected exactly one generated migration, got %v (%v)", migrations, err)
	}
	assertGoFileParses(t, migrations[0])
	content := readFile(t, migrations[0])
	want := "ALTER TABLE `users` ADD COLUMN `email` VARCHAR(128) NOT NULL DEFAULT '' COMMENT '用户''邮箱' AFTER `name`"
	if !strings.Contains(content, want) {
		t.Fatalf("expected complete ADD COLUMN %q, got:\n%s", want, content)
	}
	if strings.Contains(content, "TODO") {
		t.Fatalf("complete column definition must not leave a TODO:\n%s", content)
	}

	// 只给 --type，不带修饰：无 NOT NULL/DEFAULT/COMMENT.
	resetMakeTestState(t)
	executeMakeCommand(t, "migration", "add_age_to_users_table", "--type", "INT")
	migrations, _ = filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_add_age_to_users_table.go"))
	content = readFile(t, migrations[0])
	if !strings.Contains(content, "ADD COLUMN `age` INT, ALGORITHM=INPLACE, LOCK=NONE") || strings.Contains(content, "NOT NULL") {
		t.Fatalf("--type alone should emit the bare type, got:\n%s", content)
	}
}

// TestMakeMigrationAddModifiersRequireType 验证 --not-null/--default/--comment 缺 --type 时报错且不落盘.
func TestMakeMigrationAddModifiersRequireType(t *testing.T) {
	for _, args := range [][]string{
		{"--not-null"},
		{"--default", "0"},
		{"--comment", "x"},
	} {
		t.Run(args[0], func(t *testing.T) {
			resetMakeTestState(t)
			tmpDir := t.TempDir()
			chdirForTest(t, tmpDir)
			CmdMake.SetArgs(append([]string{"migration", "add_phone_to_users_table"}, args...))
			err := CmdMake.Execute()
			if err == nil || !strings.Contains(err.Error(), "--type") {
				t.Fatalf("%v without --type should error mentioning --type, got %v", args, err)
			}
			if files, _ := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_add_phone_to_users_table.go")); len(files) != 0 {
				t.Fatalf("failed validation must not write files, got %v", files)
			}
		})
	}
}

// TestMakeModelResolvesProjectNameFromGoMod 验证未配置项目名时从执行目录向上查找 go.mod 解析 module path.
func TestMakeModelResolvesProjectNameFromGoMod(t *testing.T) {
	resetMakeTestState(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/fromgomod\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	nested := filepath.Join(root, "cmd", "app")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	chdirForTest(t, nested)
	SetConfig(Config{})

	executeMakeCommand(t, "model", "user")

	repository := readFile(t, filepath.Join(nested, "internal/repository/user/repository_util.go"))
	if !strings.Contains(repository, `"example.com/fromgomod/internal/models"`) {
		t.Fatalf("import prefix should come from go.mod, got:\n%s", repository)
	}
}

// TestMakeModelFailsWithoutProjectNameOrGoMod 验证既未配置项目名又找不到 go.mod 时 fail-closed 且不落盘.
func TestMakeModelFailsWithoutProjectNameOrGoMod(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)
	SetConfig(Config{})

	for _, args := range [][]string{
		{"model", "order"},
		{"migration", "create_orders_table"},
	} {
		CmdMake.SetArgs(args)
		err := CmdMake.Execute()
		if err == nil || !strings.Contains(err.Error(), "go.mod") {
			t.Fatalf("%v without project name or go.mod should error mentioning go.mod, got %v", args, err)
		}
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed generation must not write files, got %d entries", len(entries))
	}
}

// TestReadModulePath 验证 go.mod module 指令解析：带引号、无 module 行、文件不存在.
func TestReadModulePath(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	if got, ok := readModulePath(write("a.mod", "// comment\nmodule   example.com/a  \n")); !ok || got != "example.com/a" {
		t.Fatalf("plain module line: got %q %v", got, ok)
	}
	if got, ok := readModulePath(write("b.mod", "module \"example.com/b\"\n")); !ok || got != "example.com/b" {
		t.Fatalf("quoted module line: got %q %v", got, ok)
	}
	if _, ok := readModulePath(write("c.mod", "go 1.26\n")); ok {
		t.Fatalf("file without module line must report not found")
	}
	if _, ok := readModulePath(filepath.Join(dir, "missing.mod")); ok {
		t.Fatalf("missing file must report not found")
	}
}

// TestSnapshotRenderingHelpers 验证快照渲染的边界：[]byte 还原、指针/切片/映射穿透收集 import、含反引号的 tag.
func TestSnapshotRenderingHelpers(t *testing.T) {
	if got := typeString(reflect.TypeFor[[]byte]()); got != "[]byte" {
		t.Fatalf("[]byte should render as []byte, got %q", got)
	}
	if got := typeString(reflect.TypeFor[[]uint16]()); got != "[]uint16" {
		t.Fatalf("other slices keep reflect rendering, got %q", got)
	}

	imports := map[string]string{}
	collectImports(reflect.TypeFor[map[string]*[]time.Time](), imports)
	collectImports(reflect.TypeFor[gorm.DeletedAt](), imports)
	collectImports(reflect.TypeFor[string](), imports)
	if len(imports) != 2 || imports["time"] != "" || imports["gorm.io/gorm"] != "" {
		t.Fatalf("expected time and gorm imports without alias, got %v", imports)
	}

	if got := tagLiteral(`gorm:"column:a"`); got != "`gorm:\"column:a\"`" {
		t.Fatalf("plain tag should use raw string, got %s", got)
	}
	if got := tagLiteral("gorm:\"comment:a`b\""); got != `"gorm:\"comment:a`+"`"+`b\""` {
		t.Fatalf("tag containing a backtick must fall back to an interpreted literal, got %s", got)
	}

	// 匿名嵌入实现 driver.Valuer 的 struct 视为单列不展平.
	src, err := renderSnapshot(reflect.TypeOf(struct {
		gorm.DeletedAt
		Name string
	}{}))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(src.fields, "DeletedAt gorm.DeletedAt") {
		t.Fatalf("Valuer embed must stay a single column, got:\n%s", src.fields)
	}
	if _, err := renderSnapshot(reflect.TypeOf(struct{ secret string }{})); err == nil {
		t.Fatalf("struct without exported fields must error")
	}
}

// TestMakeMigrationAddIndex 验证加索引迁移的生成内容：默认索引名、在线 DDL 策略、
// HasIndex 幂等守卫、可逆的 down（不是 Irreversible）、无 TODO.
func TestMakeMigrationAddIndex(t *testing.T) {
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	executeMakeCommand(t, "migration", "add_index_email_to_users_table")

	migrations, err := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_add_index_email_to_users_table.go"))
	if err != nil || len(migrations) != 1 {
		t.Fatalf("expected exactly one generated migration, got %v (%v)", migrations, err)
	}
	assertGoFileParses(t, migrations[0])
	content := readFile(t, migrations[0])

	for _, want := range []string{
		"ALTER TABLE `users` ADD INDEX `idx_users_email` (`email`), ALGORITHM=INPLACE, LOCK=NONE",
		"ALTER TABLE `users` DROP INDEX `idx_users_email`, ALGORITHM=INPLACE, LOCK=NONE",
		`HasIndex("users", "idx_users_email")`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("generated index migration missing %q:\n%s", want, content)
		}
	}
	// 加索引天然可逆，且列定义无需补全.
	for _, unwanted := range []string{"TODO", "Irreversible", "ADD COLUMN"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("index migration must not contain %q:\n%s", unwanted, content)
		}
	}
}

// TestMakeMigrationAddIndexOptions 验证 --unique / --columns / --index-name 的组合效果.
func TestMakeMigrationAddIndexOptions(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		glob string
		args []string
		want string
	}{
		{
			name: "unique composite",
			arg:  "add_index_email_status_to_users_table",
			glob: "*_add_index_email_status_to_users_table.go",
			args: []string{"--unique", "--columns", "email, status"},
			want: "ADD UNIQUE INDEX `idx_users_email_status` (`email`, `status`)",
		},
		{
			name: "custom name",
			arg:  "add_index_email_to_users_table",
			glob: "*_add_index_email_to_users_table.go",
			args: []string{"--index-name", "uk_users_email"},
			want: "ADD INDEX `uk_users_email` (`email`)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMakeTestState(t)
			tmpDir := t.TempDir()
			chdirForTest(t, tmpDir)

			executeMakeCommand(t, append([]string{"migration", tc.arg}, tc.args...)...)

			migrations, err := filepath.Glob(filepath.Join(tmpDir, "database/migrations", tc.glob))
			if err != nil || len(migrations) != 1 {
				t.Fatalf("expected exactly one generated migration, got %v (%v)", migrations, err)
			}
			assertGoFileParses(t, migrations[0])
			if content := readFile(t, migrations[0]); !strings.Contains(content, tc.want) {
				t.Fatalf("expected %q, got:\n%s", tc.want, content)
			}
		})
	}
}

// TestMakeMigrationAddIndexRejectsInvalidInput 验证生成期的 fail-closed：
// add_unique_index_* 报错指引、超长索引名报错、空 --columns 报错，且都不落盘.
func TestMakeMigrationAddIndexRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "unique_index prefix",
			args:    []string{"migration", "add_unique_index_email_to_users_table"},
			wantErr: "--unique",
		},
		{
			name:    "empty column",
			args:    []string{"migration", "add_index__to_users_table"},
			wantErr: "could not parse column name",
		},
		{
			name:    "index name too long",
			args:    []string{"migration", "add_index_email_to_users_table", "--index-name", strings.Repeat("x", maxIdentifierLen+1)},
			wantErr: "--index-name",
		},
		{
			name:    "blank columns list",
			args:    []string{"migration", "add_index_email_to_users_table", "--columns", " , "},
			wantErr: "--columns",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMakeTestState(t)
			tmpDir := t.TempDir()
			chdirForTest(t, tmpDir)

			CmdMake.SetArgs(tc.args)
			err := CmdMake.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
			if files, _ := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_add_*.go")); len(files) != 0 {
				t.Fatalf("rejected input must not write files, got %v", files)
			}
		})
	}
}

// TestMakeMigrationAddFlagsAreScoped 验证 flag 与迁移形态不匹配时报错，不被静默忽略.
func TestMakeMigrationAddFlagsAreScoped(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"index flag on column migration", []string{"migration", "add_email_to_users_table", "--type", "VARCHAR(32)", "--unique"}},
		{"index-name on column migration", []string{"migration", "add_email_to_users_table", "--index-name", "idx_x"}},
		{"columns on column migration", []string{"migration", "add_email_to_users_table", "--columns", "email"}},
		{"type on index migration", []string{"migration", "add_index_email_to_users_table", "--type", "VARCHAR(32)"}},
		{"after on index migration", []string{"migration", "add_index_email_to_users_table", "--after", "id"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMakeTestState(t)
			tmpDir := t.TempDir()
			chdirForTest(t, tmpDir)

			CmdMake.SetArgs(tc.args)
			err := CmdMake.Execute()
			if err == nil || !strings.Contains(err.Error(), "does not apply to") {
				t.Fatalf("expected a flag-scope error, got %v", err)
			}
			if files, _ := filepath.Glob(filepath.Join(tmpDir, "database/migrations/*_add_*.go")); len(files) != 0 {
				t.Fatalf("rejected input must not write files, got %v", files)
			}
		})
	}
}

// TestParseMigrationNameAddVariants 验证 add 形态的解析分流：索引、列与被拒绝的写法.
func TestParseMigrationNameAddVariants(t *testing.T) {
	cases := []struct {
		arg     string
		object  string
		table   string
		column  string
		wantErr bool
	}{
		{arg: "add_index_email_to_users_table", object: "index", table: "users", column: "email"},
		{arg: "add_index_email_status_to_users_table", object: "index", table: "users", column: "email_status"},
		{arg: "add_email_to_users_table", object: "column", table: "users", column: "email"},
		// 列名恰好以 index_ 开头的旧写法改判为索引：属修正，CHANGELOG 已标注.
		{arg: "add_indexed_at_to_users_table", object: "column", table: "users", column: "indexed_at"},
		{arg: "add_unique_index_email_to_users_table", wantErr: true},
		{arg: "add_index__to_users_table", wantErr: true},
		{arg: "add_email_users_table", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			action, object, table, column, err := parseMigrationName(tc.arg, "")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %s, got action=%s object=%s table=%s column=%s", tc.arg, action, object, table, column)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if action != "add" || object != tc.object || table != tc.table || column != tc.column {
				t.Fatalf("parsed (%s, %s, %s, %s), want (add, %s, %s, %s)", action, object, table, column, tc.object, tc.table, tc.column)
			}
		})
	}
}

// generateMigration 生成一条迁移并返回其内容，供多个用例复用.
func generateMigration(t *testing.T, glob string, args ...string) string {
	t.Helper()
	resetMakeTestState(t)
	tmpDir := t.TempDir()
	chdirForTest(t, tmpDir)

	executeMakeCommand(t, append([]string{"migration"}, args...)...)

	files, err := filepath.Glob(filepath.Join(tmpDir, "database/migrations", glob))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected exactly one generated migration for %v, got %v (%v)", args, files, err)
	}
	assertGoFileParses(t, files[0])
	return readFile(t, files[0])
}

// TestMakeMigrationDropColumnReversible 验证删列在给出列定义时生成真实 down，
// 并写明数据不恢复；未给定义时仍为 Irreversible.
func TestMakeMigrationDropColumnReversible(t *testing.T) {
	content := generateMigration(t, "*_drop_column_email_from_users_table.go",
		"drop_column_email_from_users_table", "--type", "VARCHAR(128)", "--not-null", "--default", "''")

	for _, want := range []string{
		"ALTER TABLE `users` DROP COLUMN `email`, ALGORITHM=INPLACE, LOCK=NONE",
		"ALTER TABLE `users` ADD COLUMN `email` VARCHAR(128) NOT NULL DEFAULT '', ALGORITHM=INPLACE, LOCK=NONE",
		"重建的只是列结构",
		`HasColumn("users", "email")`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("reversible drop_column missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "Irreversible") {
		t.Fatalf("drop_column with --type must not be marked irreversible:\n%s", content)
	}

	// 不给列定义时保持不可逆.
	content = generateMigration(t, "*_drop_column_email_from_users_table.go", "drop_column_email_from_users_table")
	if !strings.Contains(content, "Irreversible") {
		t.Fatalf("drop_column without --type should stay irreversible:\n%s", content)
	}
}

// TestMakeMigrationDropIndexReversible 验证删索引的默认索引名与加索引一致、不再留 TODO，
// 且给出 --columns 时生成真实的重建 down.
func TestMakeMigrationDropIndexReversible(t *testing.T) {
	content := generateMigration(t, "*_drop_index_email_from_users_table.go", "drop_index_email_from_users_table")
	// 默认名与 add_index 对齐，且不再需要人工确认.
	if !strings.Contains(content, "ALTER TABLE `users` DROP INDEX `idx_users_email`, ALGORITHM=INPLACE, LOCK=NONE") {
		t.Fatalf("drop_index should default to the same name as add_index:\n%s", content)
	}
	if strings.Contains(content, "TODO") {
		t.Fatalf("drop_index must not leave a TODO placeholder:\n%s", content)
	}
	if !strings.Contains(content, "Irreversible") {
		t.Fatalf("drop_index without --columns should stay irreversible:\n%s", content)
	}

	content = generateMigration(t, "*_drop_index_email_status_from_users_table.go",
		"drop_index_email_status_from_users_table", "--columns", "email,status", "--unique")
	for _, want := range []string{
		"DROP INDEX `idx_users_email_status`, ALGORITHM=INPLACE, LOCK=NONE",
		"ADD UNIQUE INDEX `idx_users_email_status` (`email`, `status`), ALGORITHM=INPLACE, LOCK=NONE",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("reversible drop_index missing %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "Irreversible") {
		t.Fatalf("drop_index with --columns must not be marked irreversible:\n%s", content)
	}

	// --index-name 覆盖默认名.
	content = generateMigration(t, "*_drop_index_email_from_users_table.go",
		"drop_index_email_from_users_table", "--index-name", "uk_users_email")
	if !strings.Contains(content, "DROP INDEX `uk_users_email`") {
		t.Fatalf("--index-name should override the default:\n%s", content)
	}
}

// TestMakeMigrationModifyColumn 验证改列迁移：up 由参数完整生成，down 是同形骨架带 TODO.
func TestMakeMigrationModifyColumn(t *testing.T) {
	content := generateMigration(t, "*_modify_email_of_users_table.go",
		"modify_email_of_users_table", "--type", "VARCHAR(255)", "--not-null", "--comment", "邮箱")

	if !strings.Contains(content, "ALTER TABLE `users` MODIFY COLUMN `email` VARCHAR(255) NOT NULL COMMENT '邮箱'\"") {
		t.Fatalf("modify up should be fully generated:\n%s", content)
	}
	// 改列不预填在线 DDL 策略：MODIFY COLUMN 改类型时 INPLACE 多数不被支持，
	// 预填会让生成的 SQL 直接报错。策略只在模板注释里说明，不进 SQL。
	for line := range strings.SplitSeq(content, "\n") {
		if strings.Contains(line, "db.Exec(") && strings.Contains(line, "ALGORITHM=") {
			t.Fatalf("modify must not pre-fill an online DDL algorithm in SQL:\n%s", line)
		}
	}
	// down 保留成型骨架与 TODO：lint 会拦住未补全的迁移，强制先想清楚怎么退.
	if !strings.Contains(content, "MODIFY COLUMN `email` /* TODO: 变更前的列定义 */") {
		t.Fatalf("modify down should keep a shaped TODO skeleton:\n%s", content)
	}
	if strings.Contains(content, "AFTER") || strings.Contains(content, "ADD COLUMN") {
		t.Fatalf("modify must not emit ADD COLUMN or AFTER:\n%s", content)
	}
}

// TestMakeMigrationRejectsInvalidIdentifiers 验证表名/列名/索引名的白名单校验，报错且不落盘.
func TestMakeMigrationRejectsInvalidIdentifiers(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"space in column", []string{"migration", "add_ev il_to_users_table"}, "column name"},
		{"semicolon in table", []string{"migration", "create_users; DROP TABLE x_table"}, "table name"},
		{"backtick in table", []string{"migration", "add_index_email_to_user`s_table"}, "table name"},
		{"digit-leading column", []string{"migration", "add_1st_to_users_table"}, "column name"},
		{"overlong index name", []string{"migration", "add_index_email_to_users_table", "--index-name", strings.Repeat("x", maxIdentifierLen+1)}, "--index-name"},
		{"invalid --columns entry", []string{"migration", "add_index_email_to_users_table", "--columns", "email,st atus"}, "column name"},
		{"invalid --after", []string{"migration", "add_email_to_users_table", "--type", "INT", "--after", "id;x"}, "column name"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetMakeTestState(t)
			tmpDir := t.TempDir()
			chdirForTest(t, tmpDir)

			CmdMake.SetArgs(tc.args)
			err := CmdMake.Execute()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected an error containing %q, got %v", tc.wantErr, err)
			}
			if entries, _ := os.ReadDir(tmpDir); len(entries) != 0 {
				t.Fatalf("rejected input must not write anything, got %d entries", len(entries))
			}
		})
	}
}

// TestParseMigrationNameAmbiguousSeparator 验证分隔符出现多次时 fail-closed：
// 列名可能自带分隔符（reply_to_id），表名同样可能（order_to_shipment），
// 任何一种猜法都会在另一种场景静默切错，因此直接报错并要求用 --table 消歧.
func TestParseMigrationNameAmbiguousSeparator(t *testing.T) {
	ambiguous := []string{
		"add_reply_to_id_to_messages_table",
		"add_ref_to_order_to_shipment_table",
		"modify_number_of_items_of_orders_table",
		"drop_column_copied_from_id_from_orders_table",
		"add_index_reply_to_id_to_messages_table",
	}
	for _, arg := range ambiguous {
		t.Run(arg, func(t *testing.T) {
			_, _, _, _, err := parseMigrationName(arg, "")
			if err == nil || !strings.Contains(err.Error(), "--table") {
				t.Fatalf("ambiguous name should fail closed and point at --table, got %v", err)
			}
		})
	}

	// --table 给出后按后缀剥离，两种读法都能正确表达.
	cases := []struct {
		arg    string
		table  string
		action string
		object string
		column string
	}{
		{"add_reply_to_id_to_messages_table", "messages", "add", "column", "reply_to_id"},
		{"add_ref_to_order_to_shipment_table", "order_to_shipment", "add", "column", "ref"},
		{"modify_number_of_items_of_orders_table", "orders", "modify", "column", "number_of_items"},
		{"drop_column_copied_from_id_from_orders_table", "orders", "drop", "column", "copied_from_id"},
		{"add_index_reply_to_id_to_messages_table", "messages", "add", "index", "reply_to_id"},
	}
	for _, tc := range cases {
		t.Run(tc.arg+"/--table="+tc.table, func(t *testing.T) {
			action, object, table, column, err := parseMigrationName(tc.arg, tc.table)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if action != tc.action || object != tc.object || table != tc.table || column != tc.column {
				t.Fatalf("parsed (%s, %s, %s, %s), want (%s, %s, %s, %s)",
					action, object, table, column, tc.action, tc.object, tc.table, tc.column)
			}
		})
	}

	// --table 与迁移名对不上时报错，不静默采用.
	if _, _, _, _, err := parseMigrationName("add_email_to_users_table", "orders"); err == nil {
		t.Fatalf("mismatched --table should fail")
	}
}
