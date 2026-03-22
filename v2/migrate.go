package migrate

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/gtkit/migrate/v2/make"
	"github.com/gtkit/migrate/v2/migration"
	"gorm.io/gorm"

	"github.com/spf13/cobra"
)

// Config 杩佺Щ閰嶇疆.
type Config struct {
	// ProjectName 椤圭洰鍚嶇О锛岀敤浜庝唬鐮佺敓鎴?
	ProjectName string

	// DB 鏁版嵁搴撹繛鎺?
	DB *gorm.DB

	// MigrationDir 杩佺Щ鏂囦欢鐩綍锛堥粯璁?"database/migrations"锛?
	MigrationDir string

	// Timeout 杩佺Щ鎿嶄綔瓒呮椂鏃堕棿锛堥粯璁?5 鍒嗛挓锛?
	Timeout time.Duration

	// LockName 杩佺Щ閿佸悕绉帮紙榛樿 "migrate_lock"锛?
	// 褰撳悓涓€鏁版嵁搴撹澶氫釜椤圭洰鍏辩敤鏃讹紝涓嶅悓椤圭洰搴斾娇鐢ㄤ笉鍚岀殑閿佸悕绉?
	LockName string

	// Logger 鑷畾涔夋棩蹇楀疄鐜?
	// 浼犲叆 nil 鏃朵娇鐢ㄩ粯璁ょ殑 stdout 鏃ュ織.
	Logger migration.Logger
}

// Option 閰嶇疆閫夐」鍑芥暟.
type Option func(*Config)

// WithProjectName 璁剧疆椤圭洰鍚嶇О.
func WithProjectName(name string) Option {
	return func(c *Config) {
		if name != "" {
			c.ProjectName = name
		}
	}
}

// WithMigrationDir 璁剧疆杩佺Щ鏂囦欢鐩綍.
func WithMigrationDir(dir string) Option {
	return func(c *Config) {
		if dir != "" {
			c.MigrationDir = filepath.Clean(dir)
		}
	}
}

// WithTimeout 璁剧疆杩佺Щ鎿嶄綔瓒呮椂.
func WithTimeout(d time.Duration) Option {
	return func(c *Config) {
		if d > 0 {
			c.Timeout = d
		}
	}
}

// WithLockName 璁剧疆杩佺Щ閿佸悕绉?
// 褰撳悓涓€鏁版嵁搴撹澶氫釜椤圭洰鍏辩敤鏃讹紝涓嶅悓椤圭洰搴斾娇鐢ㄤ笉鍚岀殑閿佸悕绉伴伩鍏嶄簰鐩搁樆濉?
func WithLockName(name string) Option {
	return func(c *Config) {
		if name != "" {
			c.LockName = name
		}
	}
}

// WithLogger 璁剧疆缁撴瀯鍖栨棩蹇楀疄鐜?
// 浼犲叆 nil 鏃朵娇鐢ㄩ粯璁ょ殑 stdout 鏃ュ織.
// 鐢熶骇鐜鎺ㄨ崘娉ㄥ叆 zerolog/zap 绛夊疄鐜?
func WithLogger(l migration.Logger) Option {
	return func(c *Config) {
		c.Logger = l
	}
}

// defaultConfig 杩斿洖榛樿閰嶇疆.
func defaultConfig() *Config {
	return &Config{
		ProjectName:  "project_name",
		MigrationDir: "database/migrations",
		Timeout:      5 * time.Minute,
		LockName:     "migrate_lock",
	}
}

// app 鍏ㄥ眬閰嶇疆瀹炰緥锛堢敱 Setup 鍒濆鍖栵級.
var app *Config

// Setup 鍒濆鍖栬縼绉诲伐鍏?
// 蹇呴』鍦ㄤ娇鐢ㄤ换浣曡縼绉诲懡浠や箣鍓嶈皟鐢?
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

	// 鍚屾椤圭洰鍚嶇О鍒?make 鍖咃紙鐢ㄤ簬浠ｇ爜鐢熸垚锛?
	make.SetProjectName(cfg.ProjectName)

	return nil
}

// mustApp 鑾峰彇閰嶇疆锛屾湭鍒濆鍖栨椂 panic锛堜粎鍦?cobra.Command.Run 涓娇鐢級.
func mustApp() *Config {
	if app == nil {
		panic("migrate: Setup() must be called before using migration commands")
	}
	return app
}

// newMigrator 鍒涘缓 Migrator 瀹炰緥.
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

// newContext 鍒涘缓甯﹁秴鏃剁殑 context.
func newContext() (context.Context, context.CancelFunc) {
	cfg := mustApp()
	return context.WithTimeout(context.Background(), cfg.Timeout)
}

// --- Cobra Commands ---

// CmdMigrate 杩佺Щ鏍瑰懡浠?
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

func init() {
	CmdMigrate.AddCommand(
		CmdMigrateUp,
		CmdMigrateRollback,
		CmdMigrateReset,
		CmdMigrateRefresh,
		CmdMigrateFresh,
		CmdMigrateStatus,
		CmdMigratePending,
	)
}

func runUp(cmd *cobra.Command, _ []string) error {
	ctx, cancel := newContext()
	defer cancel()

	m := newMigrator()

	// 鍏堟鏌ユ槸鍚﹂渶瑕佽縼绉?
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
