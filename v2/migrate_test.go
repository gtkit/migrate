package migrate

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	makecmd "github.com/gtkit/migrate/v2/make"
	"github.com/gtkit/migrate/v2/migration"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gorm.io/driver/mysql"
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
		WithMigrationsTable("  ledger_svc  "),
		WithAllowFresh(),
		WithAllowUnknownApplied(),
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
	if cfg.MigrationsTable != "ledger_svc" {
		t.Fatalf("migrations table should be trimmed and applied, got %q", cfg.MigrationsTable)
	}
	if !cfg.AllowFresh {
		t.Fatalf("WithAllowFresh should set AllowFresh")
	}
	if !cfg.AllowUnknownApplied {
		t.Fatalf("WithAllowUnknownApplied should set AllowUnknownApplied")
	}

	// 缺省值：锁名与项目名为空，分别表示"MySQL 按库名与账本表名派生"与"从 go.mod 解析".
	def := defaultConfig()
	if def.LockName != "" || def.ProjectName != "" {
		t.Fatalf("LockName and ProjectName must default to empty (derived), got %q %q", def.LockName, def.ProjectName)
	}

	// 空/零值应被忽略（覆盖 if 分支的另一半）。
	WithProjectName("")(cfg)
	WithMigrationDir("")(cfg)
	WithTimeout(0)(cfg)
	WithLockTimeout(0)(cfg)
	if cfg.ProjectName != "proj" || cfg.MigrationDir != "db/mig" || cfg.Timeout != 9*time.Minute {
		t.Fatalf("empty options should be ignored: %+v", cfg)
	}

	// 表名例外：空白不忽略、原样置空，由 Setup fail-closed 报错（绝不静默回退默认账本）。
	WithMigrationsTable("   ")(cfg)
	if cfg.MigrationsTable != "" {
		t.Fatalf("blank migrations table must not be ignored, got %q", cfg.MigrationsTable)
	}

	if def := defaultConfig(); def.MigrationsTable != "migrations" {
		t.Fatalf("default migrations table should be %q, got %q", "migrations", def.MigrationsTable)
	}
}

// TestSetupRejectsInvalidMigrationsTable 验证 Setup 对空白/超长/含 SQL 片段的表名 fail-closed.
func TestSetupRejectsInvalidMigrationsTable(t *testing.T) {
	db := newHandlerTestDB(t, "setup_invalid_table")
	for _, name := range []string{"   ", "ledger AS l", strings.Repeat("a", 64)} {
		if err := Setup(db, WithMigrationsTable(name)); err == nil {
			t.Fatalf("Setup should reject migrations table %q", name)
		}
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

	// fresh 双层保护：--force 只防误触命令，未经 WithAllowFresh 授权仍拒绝.
	if _, err := execMigrate(t, "fresh", "--force"); err == nil || !strings.Contains(err.Error(), "WithAllowFresh") {
		t.Fatalf("fresh without WithAllowFresh should be rejected, got: %v", err)
	}
	if !db.Migrator().HasTable("widgets") {
		t.Fatalf("rejected fresh must not drop tables")
	}

	if err := Setup(db, WithMigrationDir(dir), WithLogger(&migration.NopLogger{}), WithAllowFresh()); err != nil {
		t.Fatalf("re-setup with WithAllowFresh: %v", err)
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

// TestMigrateHandlersAllowUnknownApplied 验证顶层 WithAllowUnknownApplied 经 commandEnv 透传到 CLI：
// 账本含当前 binary 未注册的记录时，未授权的 pending/up 报错；授权后记 Warn 并继续.
func TestMigrateHandlersAllowUnknownApplied(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, handlerMigrationName+".go"), []byte("package migrations\n"), 0o644); err != nil {
		t.Fatalf("write migration file: %v", err)
	}

	db := newHandlerTestDB(t, "migrate_handlers_unknown_applied")
	if err := Setup(db, WithMigrationDir(dir), WithLogger(&migration.NopLogger{})); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := execMigrate(t, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}
	// 模拟新版本写入账本后回滚到旧 binary：注入一条当前未注册的已应用记录.
	const ghost = "2026_03_24_999999_ghost_from_newer_binary"
	if err := db.Table("migrations").Create(&migration.Migration{Migration: ghost, Batch: 2}).Error; err != nil {
		t.Fatalf("seed ghost: %v", err)
	}

	if _, err := execMigrate(t, "pending"); err == nil || !strings.Contains(err.Error(), ghost) {
		t.Fatalf("pending without WithAllowUnknownApplied should fail naming the ghost, got %v", err)
	}
	if _, err := execMigrate(t, "up"); err == nil {
		t.Fatalf("up without WithAllowUnknownApplied should fail")
	}

	if err := Setup(db, WithMigrationDir(dir), WithLogger(&migration.NopLogger{}), WithAllowUnknownApplied()); err != nil {
		t.Fatalf("re-setup with WithAllowUnknownApplied: %v", err)
	}
	out, err := execMigrate(t, "pending")
	if err != nil {
		t.Fatalf("pending with WithAllowUnknownApplied: %v", err)
	}
	if !strings.Contains(out, "up to date") {
		t.Fatalf("pending should report up to date, got:\n%s", out)
	}
	if _, err := execMigrate(t, "up"); err != nil {
		t.Fatalf("up with WithAllowUnknownApplied: %v", err)
	}
	out, err = execMigrate(t, "status")
	if err != nil {
		t.Fatalf("status with WithAllowUnknownApplied: %v", err)
	}
	if !strings.Contains(out, handlerMigrationName) || strings.Contains(out, ghost) {
		t.Fatalf("status should list registered migrations only, got:\n%s", out)
	}

	// 授权不放宽 baseline：mark-applied 仍拒绝.
	if _, err := execMigrate(t, "mark-applied", "--force"); err == nil || !strings.Contains(err.Error(), ghost) {
		t.Fatalf("mark-applied must stay fail-closed, got %v", err)
	}
}

// TestMigrateHandlersDownStepAndStatusTime 验证 CLI 的 down --step 与 status 应用时间输出.
func TestMigrateHandlersDownStepAndStatusTime(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, handlerMigrationName+".go"), []byte("package migrations\n"), 0o644); err != nil {
		t.Fatalf("write migration file: %v", err)
	}
	db := newHandlerTestDB(t, "migrate_handlers_down_step")
	if err := Setup(db, WithMigrationDir(dir), WithLogger(&migration.NopLogger{})); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// status/pending 只读：未 up 前不建账本表.
	if _, err := execMigrate(t, "status"); err != nil {
		t.Fatalf("status before up: %v", err)
	}
	if db.Migrator().HasTable("migrations") {
		t.Fatalf("status must not create the migrations table")
	}

	if _, err := execMigrate(t, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}
	out, err := execMigrate(t, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !regexp.MustCompile(`Ran \(batch 1, \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\)`).MatchString(out) {
		t.Fatalf("status should show batch and applied time, got:\n%s", out)
	}

	if _, err := execMigrate(t, "down", "--step", "-1", "--force"); err == nil || !strings.Contains(err.Error(), "--step") {
		t.Fatalf("negative --step should be rejected, got %v", err)
	}
	if !db.Migrator().HasTable("widgets") {
		t.Fatalf("rejected --step must not roll back anything")
	}
	if _, err := execMigrate(t, "down", "--step", "1"); err == nil {
		t.Fatalf("down --step without --force should error")
	}
	out, err = execMigrate(t, "down", "--step", "5", "--force")
	if err != nil {
		t.Fatalf("down --step 5 --force: %v", err)
	}
	if !strings.Contains(out, "at most 5") {
		t.Fatalf("down --step should report the step budget, got:\n%s", out)
	}
	if db.Migrator().HasTable("widgets") {
		t.Fatalf("down --step should roll back the applied migration")
	}
}

// TestGeneratedMigrationsPassLint 端到端锁定契约：make migration 的产出默认就能通过
// 自己的 migrate lint（MySQL 方言）。只有刻意留 TODO 骨架的形态才报 unfilled_placeholder，
// 只有真正不可逆的形态才报 irreversible_migration——其余一律零问题。
func TestGeneratedMigrationsPassLint(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantCodes []string // 期望的问题码集合；空表示必须零问题
	}{
		{name: "create", args: []string{"migration", "create_widgets_table"}, wantCodes: []string{"unfilled_placeholder"}},
		{name: "add column", args: []string{"migration", "add_email_to_widgets_table", "--type", "VARCHAR(128)", "--not-null", "--default", "''", "--comment", "邮箱"}},
		{name: "add index", args: []string{"migration", "add_index_email_to_widgets_table"}},
		{name: "add unique composite index", args: []string{"migration", "add_index_email_status_to_widgets_table", "--unique", "--columns", "email,status"}},
		// 改列与 update 是待补全骨架：TODO 未填是 error，缺在线 DDL 策略是 warning——
		// 两者都刻意保留，策略只能由使用者按实际变更决定。
		{name: "modify column", args: []string{"migration", "modify_email_of_widgets_table", "--type", "VARCHAR(255)"}, wantCodes: []string{"unfilled_placeholder", "missing_online_ddl"}},
		{name: "drop column reversible", args: []string{"migration", "drop_column_email_from_widgets_table", "--type", "VARCHAR(128)"}},
		{name: "drop index reversible", args: []string{"migration", "drop_index_email_from_widgets_table", "--columns", "email"}},
		{name: "drop column irreversible", args: []string{"migration", "drop_column_email_from_widgets_table"}, wantCodes: []string{"irreversible_migration"}},
		{name: "drop index irreversible", args: []string{"migration", "drop_index_email_from_widgets_table"}, wantCodes: []string{"irreversible_migration"}},
		{name: "drop table", args: []string{"migration", "drop_widgets_table"}, wantCodes: []string{"irreversible_migration"}},
		{name: "update", args: []string{"migration", "update_widgets_table"}, wantCodes: []string{"unfilled_placeholder", "missing_online_ddl"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := generateIntoTempDir(t, tc.args...)

			names, err := filepath.Glob(filepath.Join(dir, "2*.go"))
			if err != nil || len(names) != 1 {
				t.Fatalf("expected one generated migration, got %v (%v)", names, err)
			}
			name := strings.TrimSuffix(filepath.Base(names[0]), ".go")

			registry := migration.NewRegistry()
			noop := func(*gorm.DB) error { return nil }
			registry.Add(name, noop, noop)

			m := migration.NewMigrator(dir, offlineMySQLDB(t), migration.WithRegistry(registry), migration.WithLogger(&migration.NopLogger{}))
			report, err := m.Lint(t.Context(), migration.LintOptions{SkipDatabase: true})
			if err != nil {
				t.Fatalf("lint: %v", err)
			}

			got := make([]string, 0, len(report.Issues))
			for _, issue := range report.Issues {
				got = append(got, issue.Code)
			}
			slices.Sort(got)
			want := slices.Clone(tc.wantCodes)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("lint issues = %v, want %v (%+v)", got, want, report.Issues)
			}
		})
	}
}

// generateIntoTempDir 在临时目录内执行一条 make migration 并返回迁移目录.
func generateIntoTempDir(t *testing.T, args ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/lintcheck\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	makecmd.SetConfig(makecmd.Config{})
	resetFlags(makecmd.CmdMake)
	makecmd.CmdMake.SilenceErrors = true
	makecmd.CmdMake.SilenceUsage = true
	makecmd.CmdMake.SetOut(io.Discard)
	makecmd.CmdMake.SetErr(io.Discard)
	makecmd.CmdMake.SetArgs(args)
	if err := makecmd.CmdMake.Execute(); err != nil {
		t.Fatalf("make %v: %v", args, err)
	}
	return filepath.Join(root, "database/migrations")
}

// offlineMySQLDB 构造 MySQL 方言但不连接数据库，使 lint 走 MySQL 专属规则.
func offlineMySQLDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "offline:@tcp(127.0.0.1:1)/offline",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open offline mysql dialect: %v", err)
	}
	return db
}
