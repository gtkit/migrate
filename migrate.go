package migrate

import (
	"strings"

	"github.com/gtkit/logger"
	"github.com/gtkit/migrate/migration"
	"gorm.io/gorm"

	"github.com/spf13/cobra"
)

var CmdMigrate = &cobra.Command{
	Use:   "migrate",
	Short: "Run database migration",
	// 所有 migrate 下的子命令都会执行以下代码
}

var CmdMigrateUp = &cobra.Command{
	Use:   "up",
	Short: "Run unmigrated migrations",
	Run:   runUp,
}

var (
	// 数据库连接.
	dB *gorm.DB
	// 迁移文件目录.
	migrationDir = "database/migrations"

	ProjectName = "project_name"
)

func init() {
	CmdMigrate.AddCommand(
		CmdMigrateUp,
		CmdMigrateRollback,
		CmdMigrateRefresh,
		CmdMigrateReset,
		CmdMigrateFresh,
	)
}

func Set(projectName string, db *gorm.DB, dir ...string) {
	if projectName != "" {
		ProjectName = projectName
	}

	if db == nil {
		logger.Error("Database connection is nil")
		return
	}
	dB = db
	if len(dir) > 0 && dir[0] != "" {
		if strings.HasSuffix(dir[0], "/") {
			migrationDir = dir[0]
		} else {
			migrationDir = dir[0] + "/"
		}

	}
}

func runUp(cmd *cobra.Command, args []string) {
	logger.Info("-------------- migrate up start ----------")

	migrator().Up()
}

func migrator() *migration.Migrator {
	// 初始化 migrator
	return migration.NewMigrator(migrationDir, dB)
}

var CmdMigrateRollback = &cobra.Command{
	Use: "down",
	// 设置别名 migrate down == migrate rollback
	Aliases: []string{"rollback"},
	Short:   "Reverse the up command",
	Run:     runDown,
}

func runDown(cmd *cobra.Command, args []string) {
	migrator().Rollback()
}

var CmdMigrateReset = &cobra.Command{
	Use:   "reset",
	Short: "Rollback all database migrations",
	Run:   runReset,
}

func runReset(cmd *cobra.Command, args []string) {
	migrator().Reset()
}

var CmdMigrateRefresh = &cobra.Command{
	Use:   "refresh",
	Short: "Reset and re-run all migrations",
	Run:   runRefresh,
}

func runRefresh(cmd *cobra.Command, args []string) {
	migrator().Refresh()
}

var CmdMigrateFresh = &cobra.Command{
	Use:   "fresh",
	Short: "Drop all tables and re-run all migrations",
	Run:   runFresh,
}

func runFresh(cmd *cobra.Command, args []string) {
	migrator().Fresh()
}
