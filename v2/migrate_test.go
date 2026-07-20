package migrate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/migrate/v2/migration"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// 注册一条用于 handler 端到端测试的迁移（本测试二进制的 defaultRegistry 只含它）。
func init() {
	migration.Add(handlerMigrationName,
		func(db *gorm.DB) error { return db.Exec("CREATE TABLE widgets (id integer primary key)").Error },
		func(db *gorm.DB) error { return db.Exec("DROP TABLE widgets").Error },
	)
}

const handlerMigrationName = "2026_01_01_000000_create_widgets_table"

// TestOptionsApply 覆盖所有 WithX 选项与 defaultConfig（含空值忽略分支）。
func TestOptionsApply(t *testing.T) {
	cfg := defaultConfig()
	logger := &migration.NopLogger{}
	for _, o := range []Option{
		WithProjectName("proj"),
		WithMigrationDir("db/mig"),
		WithModelDir("m/models"),
		WithRepositoryDir("m/repo"),
		WithDDLDir("db/ddl"),
		WithTimeout(9 * time.Minute),
		WithLockName("lk"),
		WithLockTimeout(7 * time.Second),
		WithLogger(logger),
		WithDDLModels(struct{}{}),
	} {
		o(cfg)
	}

	if cfg.ProjectName != "proj" || cfg.MigrationDir != "db/mig" || cfg.ModelDir != "m/models" ||
		cfg.RepositoryDir != "m/repo" || cfg.DDLDir != "db/ddl" || cfg.LockName != "lk" {
		t.Fatalf("string options not applied: %+v", cfg)
	}
	if cfg.Timeout != 9*time.Minute || cfg.LockTimeout != 7*time.Second {
		t.Fatalf("duration options not applied: %+v", cfg)
	}
	if cfg.Logger == nil || len(cfg.DDLModels) != 1 {
		t.Fatalf("logger/ddlmodels not applied: %+v", cfg)
	}

	// 空/零值应被忽略（覆盖 if 分支的另一半）。
	WithProjectName("")(cfg)
	WithMigrationDir("")(cfg)
	WithTimeout(0)(cfg)
	WithLockTimeout(0)(cfg)
	if cfg.ProjectName != "proj" || cfg.MigrationDir != "db/mig" || cfg.Timeout != 9*time.Minute {
		t.Fatalf("empty options should be ignored: %+v", cfg)
	}
}

func TestSetupRequiresDB(t *testing.T) {
	if err := Setup(nil); err == nil {
		t.Fatalf("Setup(nil) should return an error")
	}
}

func TestMigrateHandlersEndToEnd(t *testing.T) {
	dir := t.TempDir()
	// 写一份自包含的磁盘迁移文件，让 lint 干净（registry 已注册同名迁移）。
	if err := os.WriteFile(filepath.Join(dir, handlerMigrationName+".go"), []byte("package migrations\n"), 0o644); err != nil {
		t.Fatalf("write migration file: %v", err)
	}

	db := newHandlerTestDB(t, "migrate_handlers")
	if err := Setup(db, WithMigrationDir(dir), WithLogger(&migration.NopLogger{})); err != nil {
		t.Fatalf("setup: %v", err)
	}

	out, err := execMigrate(t, "pending")
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if !strings.Contains(out, handlerMigrationName) {
		t.Fatalf("pending should list the migration, got:\n%s", out)
	}

	if _, err := execMigrate(t, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !db.Migrator().HasTable("widgets") {
		t.Fatalf("widgets table should exist after up")
	}

	out, _ = execMigrate(t, "status")
	if !strings.Contains(out, "Ran") {
		t.Fatalf("status should show Ran, got:\n%s", out)
	}

	if _, err := execMigrate(t, "lint"); err != nil {
		t.Fatalf("lint should pass for a clean self-contained migration: %v", err)
	}

	// down-to 目标为已应用版本本身：回滚比它新的（此处无），覆盖 runDownTo 正常路径.
	if _, err := execMigrate(t, "down-to", handlerMigrationName, "--force"); err != nil {
		t.Fatalf("down-to --force: %v", err)
	}
	if !db.Migrator().HasTable("widgets") {
		t.Fatalf("down-to target itself must not be rolled back")
	}

	if _, err := execMigrate(t, "down"); err == nil {
		t.Fatalf("down without --force should error")
	}
	if _, err := execMigrate(t, "down", "--force"); err != nil {
		t.Fatalf("down --force: %v", err)
	}
	if db.Migrator().HasTable("widgets") {
		t.Fatalf("widgets should be dropped after down --force")
	}

	if _, err := execMigrate(t, "refresh", "--force"); err != nil {
		t.Fatalf("refresh --force: %v", err)
	}
	if !db.Migrator().HasTable("widgets") {
		t.Fatalf("widgets should exist after refresh")
	}

	if _, err := execMigrate(t, "fresh", "--force"); err != nil {
		t.Fatalf("fresh --force: %v", err)
	}
	if !db.Migrator().HasTable("widgets") {
		t.Fatalf("widgets should exist after fresh")
	}

	if _, err := execMigrate(t, "reset", "--force"); err != nil {
		t.Fatalf("reset --force: %v", err)
	}
	if db.Migrator().HasTable("widgets") {
		t.Fatalf("widgets should be dropped after reset --force")
	}

	// mark-applied：无 --force 拒绝；有 --force 把 pending 标记为已应用而不建表.
	if _, err := execMigrate(t, "mark-applied"); err == nil {
		t.Fatalf("mark-applied without --force should error")
	}
	if _, err := execMigrate(t, "mark-applied", "--force"); err != nil {
		t.Fatalf("mark-applied --force: %v", err)
	}
	if db.Migrator().HasTable("widgets") {
		t.Fatalf("mark-applied must not create the table")
	}
}

func newHandlerTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// execMigrate 通过 cobra 命令树执行一条 migrate 子命令并返回输出.
func execMigrate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetFlags(CmdMigrate)
	root := &cobra.Command{Use: "app"}
	root.AddCommand(CmdMigrate)
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SilenceErrors = true
	root.SilenceUsage = true
	root.SetArgs(append([]string{"migrate"}, args...))
	err := root.Execute()
	return buf.String(), err
}

func resetFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	for _, c := range cmd.Commands() {
		resetFlags(c)
	}
}

// TestRequireForce 验证 requireForce 的两个分支.
func TestRequireForce(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("force", false, "")

	if err := requireForce(cmd, "danger"); err == nil {
		t.Fatalf("expected error when --force is absent")
	}

	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatalf("set force: %v", err)
	}
	if err := requireForce(cmd, "danger"); err != nil {
		t.Fatalf("expected no error when --force is set, got %v", err)
	}
}

// TestDestructiveRunFuncsRequireForce 验证 down/down-to/reset/refresh/fresh 未传 --force 时
// 直接返回错误并在触达数据库之前拒绝执行（app 未初始化仍不 panic，
// 证明 requireForce 在 newContext/newMigrator 之前短路）.
func TestDestructiveRunFuncsRequireForce(t *testing.T) {
	funcs := map[string]func(*cobra.Command, []string) error{
		"down":    runDown,
		"down-to": runDownTo,
		"reset":   runReset,
		"refresh": runRefresh,
		"fresh":   runFresh,
	}

	for name, fn := range funcs {
		t.Run(name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("force", false, "")

			// down-to 会读取 args[0]，但 requireForce 在此之前短路；传一个占位 arg 以防万一。
			err := fn(cmd, []string{"2026_01_01_000000_placeholder"})
			if err == nil {
				t.Fatalf("%s without --force should return an error", name)
			}
			if !strings.Contains(err.Error(), "--force") {
				t.Fatalf("%s error should mention --force, got: %v", name, err)
			}
		})
	}
}
