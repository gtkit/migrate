package migrate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
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

	// LockTimeout 获取迁移锁的最长等待时间（默认 10 秒）.
	// 超时后返回错误而非无限阻塞.
	LockTimeout time.Duration

	// Logger 自定义日志实现.
	// 传入 nil 时使用默认的 stdout 日志.
	Logger migration.Logger

	// MigrationsTable 迁移记录表名（默认 "migrations"）.
	// 多项目共用同一数据库时，各项目应使用独立的记录表.
	// 仅允许字母、数字与下划线且长度不超过 63，不支持 schema 限定名；
	// 空白同样非法，Setup 直接报错，绝不静默回退默认账本.
	MigrationsTable string

	// AllowFresh 显式授权 fresh 命令（默认 false，fresh 直接报错拒绝）.
	// fresh 会删除库内全部用户表，仅当本项目独占该数据库时才应授权.
	AllowFresh bool

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

// WithLockTimeout 设置获取迁移锁的最长等待时间.
// 超时后返回错误而非无限阻塞，避免某个实例的长迁移导致其他实例永久挂起.
func WithLockTimeout(d time.Duration) Option {
	return func(c *Config) {
		if d > 0 {
			c.LockTimeout = d
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

// WithMigrationsTable 设置迁移记录表名（默认 "migrations"）.
// 多项目共用同一数据库时，各项目应使用独立的记录表并配合 WithLockName 使用独立锁名，
// 迁移账本与漂移校验互不干扰.
// 表名仅允许字母、数字与下划线且长度不超过 63，不支持 schema 限定名；首尾空白自动规整.
// 空白字符串同样非法（不静默保持默认，防止配置意外为空时写错账本），
// 非法表名 Setup 直接报错.
func WithMigrationsTable(name string) Option {
	return func(c *Config) {
		c.MigrationsTable = strings.TrimSpace(name)
	}
}

// WithAllowFresh 显式授权 fresh 命令.
// fresh 会删除库内全部用户表（含其他项目的业务表与迁移账本），默认禁用；
// 仅当本项目独占该数据库时才应授权.数据库所有权无法从配置推断，必须由调用方声明.
// 该授权与 CLI 的 --force 分层：--force 防误触命令，WithAllowFresh 声明数据库独占.
func WithAllowFresh() Option {
	return func(c *Config) {
		c.AllowFresh = true
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
		ProjectName:     "project_name",
		MigrationDir:    "database/migrations",
		ModelDir:        "internal/models",
		RepositoryDir:   "internal/repository",
		DDLDir:          "database/ddl",
		Timeout:         5 * time.Minute,
		LockName:        "migrate_lock",
		LockTimeout:     10 * time.Second,
		MigrationsTable: "migrations",
	}
}

// app 全局配置实例（由 Setup 初始化）.
// 原子指针：Setup 与命令执行可能不在同一 goroutine，避免数据竞争.
var app atomic.Pointer[Config]

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

	// 表名会被拼入 SQL：配置期就 fail-closed，避免运行命令时才发现（或静默写错账本）.
	// 无条件校验：默认值 "migrations" 恒合法，显式配置的空白/非法值在此报错.
	if err := migration.ValidateMigrationsTable(cfg.MigrationsTable); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	app.Store(cfg)

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

// commandEnv 返回执行迁移命令所需的 Migrator 与带超时的 context.
// context 基于 cmd.Context() 派生：调用方经 ExecuteContext 传入的取消信号
// （如 signal.NotifyContext）能贯通到迁移执行.
// Setup 未调用时返回错误（库代码不 panic）.
func commandEnv(cmd *cobra.Command) (*migration.Migrator, context.Context, context.CancelFunc, error) {
	cfg := app.Load()
	if cfg == nil {
		return nil, nil, nil, errors.New("migrate: Setup() must be called before using migration commands")
	}

	var opts []migration.MigratorOption
	if cfg.LockName != "" {
		opts = append(opts, migration.WithLockName(cfg.LockName))
	}
	if cfg.LockTimeout > 0 {
		opts = append(opts, migration.WithLockTimeout(cfg.LockTimeout))
	}
	if cfg.Logger != nil {
		opts = append(opts, migration.WithLogger(cfg.Logger))
	}
	opts = append(opts, migration.WithMigrationsTable(cfg.MigrationsTable))
	if cfg.AllowFresh {
		opts = append(opts, migration.WithAllowFresh())
	}

	parent := context.Background()
	if cmd != nil && cmd.Context() != nil {
		parent = cmd.Context()
	}

	m := migration.NewMigrator(cfg.MigrationDir, cfg.DB, opts...)
	ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
	return m, ctx, cancel, nil
}

// --- Cobra Commands ---

// CmdMigrate 迁移根命令.
var CmdMigrate = &cobra.Command{
	Use:   "migrate",
	Short: "Run database migration",
}

// CmdMigrateUp 执行所有未运行的迁移（up）.
var CmdMigrateUp = &cobra.Command{
	Use:   "up",
	Short: "Run unmigrated migrations",
	RunE:  runUp,
}

// CmdMigrateRollback 回滚最后一个批次的迁移（down/rollback，需 --force）.
var CmdMigrateRollback = &cobra.Command{
	Use:     "down",
	Aliases: []string{"rollback"},
	Short:   "Reverse the last batch of migrations",
	RunE:    runDown,
}

// CmdMigrateReset 回滚全部迁移（reset，需 --force）.
var CmdMigrateReset = &cobra.Command{
	Use:   "reset",
	Short: "Rollback all database migrations",
	RunE:  runReset,
}

// CmdMigrateRefresh 回滚全部迁移后重新执行（refresh，需 --force）.
var CmdMigrateRefresh = &cobra.Command{
	Use:   "refresh",
	Short: "Reset and re-run all migrations",
	RunE:  runRefresh,
}

// CmdMigrateFresh 删除库内所有表并重新执行全部迁移
// （fresh，需 Setup 时 WithAllowFresh 授权 + 运行时 --force；仅限本项目独占的数据库）.
var CmdMigrateFresh = &cobra.Command{
	Use:   "fresh",
	Short: "Drop all tables and re-run all migrations",
	RunE:  runFresh,
}

// CmdMigrateStatus 显示每个迁移的执行状态（status）.
var CmdMigrateStatus = &cobra.Command{
	Use:   "status",
	Short: "Show the status of each migration",
	RunE:  runStatus,
}

// CmdMigratePending 列出待执行的迁移（pending，dry-run）.
var CmdMigratePending = &cobra.Command{
	Use:   "pending",
	Short: "Show pending migrations that would be executed by 'up' (dry-run)",
	RunE:  runPending,
}

// CmdMigrateLint 检查迁移文件、注册表与已应用记录的一致性与回滚风险（lint）.
var CmdMigrateLint = &cobra.Command{
	Use:   "lint",
	Short: "Lint migration files, registry, and applied records for drift and rollback risk",
	RunE:  runLint,
}

// CmdMigrateDownTo 回滚所有版本高于目标的迁移（down-to，需 --force，目标须已应用）.
var CmdMigrateDownTo = &cobra.Command{
	Use:   "down-to <version>",
	Short: "Roll back all migrations newer than <version> (exclusive)",
	Args:  cobra.ExactArgs(1),
	RunE:  runDownTo,
}

// CmdMigrateMarkApplied 将 pending 迁移标记为已应用而不执行（mark-applied，baseline 存量库，需 --force）.
var CmdMigrateMarkApplied = &cobra.Command{
	Use:   "mark-applied",
	Short: "Mark pending migrations as applied WITHOUT running them (baseline an existing database)",
	RunE:  runMarkApplied,
}

func init() {
	CmdMigrateLint.Flags().Bool("strict", false, "Fail on warnings as well as errors")
	CmdMigrateLint.Flags().Bool("skip-db", false, "Skip database-applied migration drift checks")

	CmdMigrateRollback.Flags().Bool("force", false, "Required: confirm rolling back the last batch")
	CmdMigrateDownTo.Flags().Bool("force", false, "Required: confirm rolling back migrations newer than the target")
	CmdMigrateReset.Flags().Bool("force", false, "Required: confirm rolling back all migrations")
	CmdMigrateRefresh.Flags().Bool("force", false, "Required: confirm rolling back and re-running all migrations")
	CmdMigrateFresh.Flags().Bool("force", false, "Required: confirm dropping all tables and destroying all data")

	CmdMigrateMarkApplied.Flags().String("to", "", "Only mark pending migrations up to and including this version")
	CmdMigrateMarkApplied.Flags().Bool("force", false, "Required: confirm marking versions as applied without running them")

	CmdMigrate.AddCommand(
		CmdMigrateUp,
		CmdMigrateRollback,
		CmdMigrateDownTo,
		CmdMigrateReset,
		CmdMigrateRefresh,
		CmdMigrateFresh,
		CmdMigrateStatus,
		CmdMigratePending,
		CmdMigrateLint,
		CmdMigrateMarkApplied,
	)
}

func runUp(cmd *cobra.Command, _ []string) error {
	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	// 预检待执行清单：无事可做时直接提示，避免误导性的 "Running migrations..."
	pending, err := m.Pending(ctx)
	if err != nil {
		return fmt.Errorf("check pending migrations: %w", err)
	}
	if len(pending) == 0 {
		cmd.Println("Database is up to date.")
		return nil
	}

	cmd.Printf("Running %d migration(s)...\n", len(pending))
	if err := m.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}

	cmd.Println("Migrations completed.")
	return nil
}

func runDown(cmd *cobra.Command, _ []string) error {
	if err := requireForce(cmd, "down rolls back the last batch of migrations and can drop data"); err != nil {
		return err
	}

	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	cmd.Println("Rolling back last batch...")
	if err := m.Rollback(ctx); err != nil {
		return fmt.Errorf("migrate rollback: %w", err)
	}

	cmd.Println("Rollback completed.")
	return nil
}

// requireForce 校验破坏性命令是否显式传入 --force，缺失时拒绝执行.
func requireForce(cmd *cobra.Command, warning string) error {
	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return err
	}
	if !force {
		return fmt.Errorf("%s; re-run with --force to confirm", warning)
	}
	return nil
}

func runReset(cmd *cobra.Command, _ []string) error {
	if err := requireForce(cmd, "reset rolls back ALL migrations and can drop application data"); err != nil {
		return err
	}

	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	cmd.Println("Resetting all migrations...")
	if err := m.Reset(ctx); err != nil {
		return fmt.Errorf("migrate reset: %w", err)
	}

	cmd.Println("Reset completed.")
	return nil
}

func runRefresh(cmd *cobra.Command, _ []string) error {
	if err := requireForce(cmd, "refresh rolls back ALL migrations then re-runs them and can drop application data"); err != nil {
		return err
	}

	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	cmd.Println("Refreshing all migrations...")
	if err := m.Refresh(ctx); err != nil {
		return fmt.Errorf("migrate refresh: %w", err)
	}

	cmd.Println("Refresh completed.")
	return nil
}

func runFresh(cmd *cobra.Command, _ []string) error {
	if err := requireForce(cmd, "fresh DROPS ALL TABLES in the database and destroys all data"); err != nil {
		return err
	}

	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	cmd.Println("Dropping all tables and re-running migrations...")
	if err := m.Fresh(ctx); err != nil {
		return fmt.Errorf("migrate fresh: %w", err)
	}

	cmd.Println("Fresh migration completed.")
	return nil
}

func runStatus(cmd *cobra.Command, _ []string) error {
	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	statuses, err := m.Status(ctx)
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
	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	pending, err := m.Pending(ctx)
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
	strict, err := cmd.Flags().GetBool("strict")
	if err != nil {
		return err
	}
	skipDB, err := cmd.Flags().GetBool("skip-db")
	if err != nil {
		return err
	}

	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	report, err := m.Lint(ctx, migration.LintOptions{
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

func runDownTo(cmd *cobra.Command, args []string) error {
	if err := requireForce(cmd, "down-to rolls back all migrations newer than the target and can drop data"); err != nil {
		return err
	}

	target := strings.TrimSpace(args[0])
	if target == "" {
		return fmt.Errorf("down-to requires a target migration version")
	}

	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	cmd.Printf("Rolling back migrations newer than %s...\n", target)
	if err := m.RollbackTo(ctx, target); err != nil {
		return fmt.Errorf("migrate down-to: %w", err)
	}

	cmd.Println("Rollback completed.")
	return nil
}

func runMarkApplied(cmd *cobra.Command, _ []string) error {
	if err := requireForce(cmd, "mark-applied records versions as applied WITHOUT running their SQL and can hide schema drift"); err != nil {
		return err
	}

	to, err := cmd.Flags().GetString("to")
	if err != nil {
		return err
	}
	to = strings.TrimSpace(to)

	m, ctx, cancel, err := commandEnv(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	marked, err := m.MarkApplied(ctx, to)
	if err != nil {
		return fmt.Errorf("migrate mark-applied: %w", err)
	}

	if len(marked) == 0 {
		cmd.Println("No pending migrations to mark as applied.")
		return nil
	}

	cmd.Println("Marked the following migration(s) as applied WITHOUT running them:")
	for _, name := range marked {
		cmd.Printf("  %s\n", name)
	}
	cmd.Printf("Marked %d migration(s) as applied.\n", len(marked))
	return nil
}
