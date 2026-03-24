package migrate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gtkit/migrate/v2/make"
	"github.com/gtkit/migrate/v2/migration"
	"gorm.io/gorm"

	"github.com/spf13/cobra"
)

// Config 迁移配置.
type Config struct {
	// ProjectName 项目名称，用于代码生成.
	ProjectName string

	// DB 数据库连接.
	DB *gorm.DB

	// MigrationDir 迁移文件目录（默认 "database/migrations"）.
	MigrationDir string

	// ModelDir model 文件目录（默认 "internal/models"）.
	ModelDir string

	// RepositoryDir repository 文件目录（默认 "internal/repository"）.
	RepositoryDir string

	// DDLDir DDL 文件输出目录（默认 "database/ddl"）.
	DDLDir string

	// Timeout 迁移操作超时时间（默认 5 分钟）.
	Timeout time.Duration

	// LockName 迁移锁名称（默认 "migrate_lock"）.
	// 当同一数据库被多个项目共用时，不同项目应使用不同的锁名称.
	LockName string

	// Logger 自定义日志实现.
	// 传入 nil 时使用默认的 stdout 日志.
	Logger migration.Logger

	// DDLModels 用于 make ddl 的模型注册表。
	DDLModels []any
}

// Option 配置选项函数.
type Option func(*Config)

// WithProjectName 设置项目名称.
func WithProjectName(name string) Option {
	return func(c *Config) {
		if name != "" {
			c.ProjectName = name
		}
	}
}

// WithMigrationDir 设置迁移文件目录.
func WithMigrationDir(dir string) Option {
	return func(c *Config) {
		if dir != "" {
			c.MigrationDir = filepath.Clean(dir)
		}
	}
}

// WithModelDir 设置 model 生成目录。
func WithModelDir(dir string) Option {
	return func(c *Config) {
		if dir != "" {
			c.ModelDir = filepath.Clean(dir)
		}
	}
}

// WithRepositoryDir 设置 repository 生成目录。
func WithRepositoryDir(dir string) Option {
	return func(c *Config) {
		if dir != "" {
			c.RepositoryDir = filepath.Clean(dir)
		}
	}
}

// WithDDLDir 设置 DDL 输出目录。
func WithDDLDir(dir string) Option {
	return func(c *Config) {
		if dir != "" {
			c.DDLDir = filepath.Clean(dir)
		}
	}
}

// WithTimeout 设置迁移操作超时.
func WithTimeout(d time.Duration) Option {
	return func(c *Config) {
		if d > 0 {
			c.Timeout = d
		}
	}
}

// WithLockName 设置迁移锁名称.
// 当同一数据库被多个项目共用时，不同项目应使用不同的锁名称避免互相阻塞.
func WithLockName(name string) Option {
	return func(c *Config) {
		if name != "" {
			c.LockName = name
		}
	}
}

// WithLogger 设置结构化日志实现.
// 传入 nil 时使用默认的 stdout 日志.
// 生产环境推荐注入 zerolog/zap 等实现.
func WithLogger(l migration.Logger) Option {
	return func(c *Config) {
		c.Logger = l
	}
}

// WithDDLModels 注册可用于 make ddl 的 GORM 模型。
func WithDDLModels(models ...any) Option {
	return func(c *Config) {
		c.DDLModels = append(c.DDLModels, models...)
	}
}

// defaultConfig 返回默认配置.
func defaultConfig() *Config {
	return &Config{
		ProjectName:   "project_name",
		MigrationDir:  "database/migrations",
		ModelDir:      "internal/models",
		RepositoryDir: "internal/repository",
		DDLDir:        "database/ddl",
		Timeout:       5 * time.Minute,
		LockName:      "migrate_lock",
	}
}

// app 全局配置实例（由 Setup 初始化）.
var app *Config

// Setup 初始化迁移工具.
// 必须在使用任何迁移命令之前调用.
func Setup(db *gorm.DB, opts ...Option) error {
	if db == nil {
		return fmt.Errorf("migrate: database connection is required")
	}

	cfg := defaultConfig()
	cfg.DB = db

	for _, opt := range opts {
		opt(cfg)
	}

	app = cfg

	make.SetConfig(make.Config{
		ProjectName:   cfg.ProjectName,
		ModelDir:      cfg.ModelDir,
		RepositoryDir: cfg.RepositoryDir,
		MigrationDir:  cfg.MigrationDir,
		DDLDir:        cfg.DDLDir,
		DB:            cfg.DB,
		DDLModels:     cfg.DDLModels,
	})

	return nil
}

// mustApp 获取配置，未初始化时 panic（仅在 cobra.Command.Run 中使用）.
func mustApp() *Config {
	if app == nil {
		panic("migrate: Setup() must be called before using migration commands")
	}
	return app
}

// newMigrator 创建 Migrator 实例.
func newMigrator() *migration.Migrator {
	cfg := mustApp()

	var opts []migration.MigratorOption
	if cfg.LockName != "" {
		opts = append(opts, migration.WithLockName(cfg.LockName))
	}
	if cfg.Logger != nil {
		opts = append(opts, migration.WithLogger(cfg.Logger))
	}

	return migration.NewMigrator(cfg.MigrationDir, cfg.DB, opts...)
}

// newContext 创建带超时的 context.
func newContext() (context.Context, context.CancelFunc) {
	cfg := mustApp()
	return context.WithTimeout(context.Background(), cfg.Timeout)
}

// --- Cobra Commands ---

// CmdMigrate 迁移根命令.
var CmdMigrate = &cobra.Command{
	Use:   "migrate",
	Short: "Run database migration",
}

var CmdMigrateUp = &cobra.Command{
	Use:   "up",
	Short: "Run unmigrated migrations",
	RunE:  runUp,
}

var CmdMigrateRollback = &cobra.Command{
	Use:     "down",
	Aliases: []string{"rollback"},
	Short:   "Reverse the last batch of migrations",
	RunE:    runDown,
}

var CmdMigrateReset = &cobra.Command{
	Use:   "reset",
	Short: "Rollback all database migrations",
	RunE:  runReset,
}

var CmdMigrateRefresh = &cobra.Command{
	Use:   "refresh",
	Short: "Reset and re-run all migrations",
	RunE:  runRefresh,
}

var CmdMigrateFresh = &cobra.Command{
	Use:   "fresh",
	Short: "Drop all tables and re-run all migrations",
	RunE:  runFresh,
}

var CmdMigrateStatus = &cobra.Command{
	Use:   "status",
	Short: "Show the status of each migration",
	RunE:  runStatus,
}

var CmdMigratePending = &cobra.Command{
	Use:   "pending",
	Short: "Show pending migrations that would be executed by 'up' (dry-run)",
	RunE:  runPending,
}

var CmdMigrateLint = &cobra.Command{
	Use:   "lint",
	Short: "Lint migration files, registry, and applied records for drift and rollback risk",
	RunE:  runLint,
}

func init() {
	CmdMigrateLint.Flags().Bool("strict", false, "Fail on warnings as well as errors")
	CmdMigrateLint.Flags().Bool("skip-db", false, "Skip database-applied migration drift checks")

	CmdMigrate.AddCommand(
		CmdMigrateUp,
		CmdMigrateRollback,
		CmdMigrateReset,
		CmdMigrateRefresh,
		CmdMigrateFresh,
		CmdMigrateStatus,
		CmdMigratePending,
		CmdMigrateLint,
	)
}

func runUp(cmd *cobra.Command, _ []string) error {
	ctx, cancel := newContext()
	defer cancel()

	m := newMigrator()

	// 先检查是否需要迁移
	upToDate, err := m.IsUpToDate(ctx)
	if err != nil {
		return fmt.Errorf("check migration status: %w", err)
	}
	if upToDate {
		cmd.Println("Database is up to date.")
		return nil
	}

	cmd.Println("Running migrations...")
	if err := m.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}

	cmd.Println("Migrations completed.")
	return nil
}

func runDown(cmd *cobra.Command, _ []string) error {
	ctx, cancel := newContext()
	defer cancel()

	cmd.Println("Rolling back last batch...")
	if err := newMigrator().Rollback(ctx); err != nil {
		return fmt.Errorf("migrate rollback: %w", err)
	}

	cmd.Println("Rollback completed.")
	return nil
}

func runReset(cmd *cobra.Command, _ []string) error {
	ctx, cancel := newContext()
	defer cancel()

	cmd.Println("Resetting all migrations...")
	if err := newMigrator().Reset(ctx); err != nil {
		return fmt.Errorf("migrate reset: %w", err)
	}

	cmd.Println("Reset completed.")
	return nil
}

func runRefresh(cmd *cobra.Command, _ []string) error {
	ctx, cancel := newContext()
	defer cancel()

	cmd.Println("Refreshing all migrations...")
	if err := newMigrator().Refresh(ctx); err != nil {
		return fmt.Errorf("migrate refresh: %w", err)
	}

	cmd.Println("Refresh completed.")
	return nil
}

func runFresh(cmd *cobra.Command, _ []string) error {
	ctx, cancel := newContext()
	defer cancel()

	cmd.Println("Dropping all tables and re-running migrations...")
	if err := newMigrator().Fresh(ctx); err != nil {
		return fmt.Errorf("migrate fresh: %w", err)
	}

	cmd.Println("Fresh migration completed.")
	return nil
}

func runStatus(cmd *cobra.Command, _ []string) error {
	ctx, cancel := newContext()
	defer cancel()

	statuses, err := newMigrator().Status(ctx)
	if err != nil {
		return fmt.Errorf("migrate status: %w", err)
	}

	if len(statuses) == 0 {
		cmd.Println("No migrations found.")
		return nil
	}

	cmd.Println("Migration Status:")
	cmd.Println("--------------------------------------------------")
	for _, s := range statuses {
		status := "Pending"
		if s.Ran {
			status = fmt.Sprintf("Ran (batch %d)", s.Batch)
		}
		cmd.Printf("  %-50s %s\n", s.Name, status)
	}

	return nil
}

func runPending(cmd *cobra.Command, _ []string) error {
	ctx, cancel := newContext()
	defer cancel()

	pending, err := newMigrator().Pending(ctx)
	if err != nil {
		return fmt.Errorf("migrate pending: %w", err)
	}

	if len(pending) == 0 {
		cmd.Println("No pending migrations. Database is up to date.")
		return nil
	}

	cmd.Printf("Pending migrations (%d):\n", len(pending))
	for i, name := range pending {
		cmd.Printf("  %d. %s\n", i+1, name)
	}
	cmd.Println("\nRun 'migrate up' to execute these migrations.")

	return nil
}

func runLint(cmd *cobra.Command, _ []string) error {
	ctx, cancel := newContext()
	defer cancel()

	strict, err := cmd.Flags().GetBool("strict")
	if err != nil {
		return err
	}
	skipDB, err := cmd.Flags().GetBool("skip-db")
	if err != nil {
		return err
	}

	report, err := newMigrator().Lint(ctx, migration.LintOptions{
		SkipDatabase: skipDB,
	})
	if err != nil {
		return fmt.Errorf("migrate lint: %w", err)
	}

	if len(report.Issues) == 0 {
		cmd.Println("Migration lint passed. No issues found.")
		return nil
	}

	for _, issue := range report.Issues {
		cmd.Printf("[%s] %s: %s\n",
			strings.ToUpper(string(issue.Severity)),
			issue.Name,
			issue.Message,
		)
	}

	cmd.Printf("\nSummary: %d error(s), %d warning(s)\n", report.ErrorCount(), report.WarningCount())

	if report.HasErrors() || (strict && report.HasWarnings()) {
		return fmt.Errorf("%w: %d error(s), %d warning(s)", migration.ErrLintFailed, report.ErrorCount(), report.WarningCount())
	}

	return nil
}
