package migration

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// DBType 数据库类型.
type DBType string

const (
	DBTypeMySQL    DBType = "mysql"
	DBTypePostgres DBType = "postgres"
	DBTypeSQLite   DBType = "sqlite"
)

// DetectDBType 从 GORM Dialector 自动检测数据库类型.
// db 为 nil 或未初始化（零值 gorm.DB）时返回空类型，不 panic.
func DetectDBType(db *gorm.DB) DBType {
	// Dialector 是经嵌入指针 Config 提升的字段：Config 为 nil 时
	// 访问 db.Dialector 本身就是 nil 解引用，必须先判 Config.
	if db == nil || db.Config == nil || db.Dialector == nil {
		return DBType("")
	}
	name := db.Name()
	switch name {
	case "mysql":
		return DBTypeMySQL
	case "postgres":
		return DBTypePostgres
	case "sqlite":
		return DBTypeSQLite
	default:
		return DBType(name)
	}
}

// CurrentDatabase 获取当前数据库名称.
// db 为 nil 或未初始化（零值 gorm.DB）时返回空字符串（导出 API 不因 nil 输入 panic）.
func CurrentDatabase(db *gorm.DB) string {
	// 同 DetectDBType：Config 为 nil 时访问提升字段 Dialector 会 panic，先判 Config.
	if db == nil || db.Config == nil || db.Dialector == nil {
		return ""
	}
	return db.Migrator().CurrentDatabase()
}

// DeleteAllTables 删除当前库默认 schema 内的全部用户表与视图.
// PostgreSQL 仅清理 public schema，其他 schema 的对象不受影响.
func DeleteAllTables(db *gorm.DB) error {
	dbType := DetectDBType(db)
	switch dbType {
	case DBTypeMySQL:
		return deleteMySQLTables(db)
	case DBTypePostgres:
		return deletePostgresTables(db)
	case DBTypeSQLite:
		return deleteSQLiteTables(db)
	default:
		return fmt.Errorf("unsupported database type: %s", dbType)
	}
}

// quoteMySQLIdent 反引号转义 MySQL 标识符.
func quoteMySQLIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func deleteMySQLTables(db *gorm.DB) error {
	dbname := CurrentDatabase(db)

	// 视图与基表分开处理：DROP TABLE 对 VIEW 直接报错，必须用 DROP VIEW.
	var views []string
	if err := db.Table("information_schema.tables").
		Where("table_schema = ? AND table_type = ?", dbname, "VIEW").
		Pluck("table_name", &views).Error; err != nil {
		return fmt.Errorf("list mysql views: %w", err)
	}
	var tables []string
	if err := db.Table("information_schema.tables").
		Where("table_schema = ? AND table_type = ?", dbname, "BASE TABLE").
		Pluck("table_name", &tables).Error; err != nil {
		return fmt.Errorf("list mysql tables: %w", err)
	}

	// 视图不受外键约束，先删；残留视图会使重放中的 CREATE VIEW 冲突.
	for _, view := range views {
		if err := db.Exec("DROP VIEW IF EXISTS " + quoteMySQLIdent(view)).Error; err != nil {
			return fmt.Errorf("drop view %s: %w", view, err)
		}
	}

	if len(tables) == 0 {
		return nil
	}

	// 关闭外键检查、删表、恢复必须在同一连接上完成：foreign_key_checks 是会话级变量，
	// 若经连接池分发，关闭态可能落不到删表连接，或残留污染被业务复用的池内连接.
	// 使用专属 *sql.Conn（与迁移锁同款模式）：恢复失败时标坏连接物理关闭结束会话，
	// 绝不把 foreign_key_checks=0 的连接归还连接池.
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql db: %w", err)
	}
	ctx := context.Background()
	if db.Statement != nil && db.Statement.Context != nil {
		ctx = db.Statement.Context
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "SET foreign_key_checks = 0"); err != nil {
		// 网络错误/上下文取消时服务端可能已执行成功——会话状态未知的连接
		// 不得归还连接池，标坏后 Close 物理关闭（与锁获取失败路径同一原则）.
		markBadConn(conn)
		return fmt.Errorf("disable foreign key checks: %w", err)
	}

	var dropErr error
	for _, table := range tables {
		if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoteMySQLIdent(table)); err != nil {
			dropErr = fmt.Errorf("drop table %s: %w", table, err)
			break
		}
	}

	// 无论删表成功与否都恢复外键检查.恢复使用独立超时上下文：业务 ctx 已取消时仍要
	// 尝试把会话复位.恢复失败则标坏连接（Close 物理关闭），并且仅在没有更早错误时上报，
	// 避免掩盖删表错误.
	restoreCtx, cancel := context.WithTimeout(context.Background(), lockReleaseTimeout)
	defer cancel()
	if _, err := conn.ExecContext(restoreCtx, "SET foreign_key_checks = 1"); err != nil {
		markBadConn(conn)
		if dropErr == nil {
			return fmt.Errorf("enable foreign key checks: %w", err)
		}
	}
	return dropErr
}

// quotePostgresIdent 双引号转义 PostgreSQL 标识符.
func quotePostgresIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func deletePostgresTables(db *gorm.DB) error {
	// 查询与 DROP 都显式限定 public：不依赖 search_path，
	// 其他 schema 的同名对象绝不受影响.
	var views []string
	if err := db.Raw(`SELECT viewname FROM pg_views WHERE schemaname = 'public'`).
		Scan(&views).Error; err != nil {
		return fmt.Errorf("list postgres views: %w", err)
	}
	// 先删视图：残留视图会使重放中的 CREATE VIEW 冲突（独立视图不被表的 CASCADE 覆盖）.
	for _, view := range views {
		if err := db.Exec(`DROP VIEW IF EXISTS "public".` + quotePostgresIdent(view) + " CASCADE").Error; err != nil {
			return fmt.Errorf("drop view %s: %w", view, err)
		}
	}

	var tables []string
	err := db.Raw(`SELECT tablename FROM pg_tables WHERE schemaname = 'public'`).
		Scan(&tables).Error
	if err != nil {
		return fmt.Errorf("list postgres tables: %w", err)
	}

	// CASCADE 删除所有表及其依赖
	for _, table := range tables {
		if err := db.Exec(`DROP TABLE IF EXISTS "public".` + quotePostgresIdent(table) + " CASCADE").Error; err != nil {
			return fmt.Errorf("drop table %s: %w", table, err)
		}
	}

	return nil
}

func deleteSQLiteTables(db *gorm.DB) error {
	// 先删视图：残留视图会使重放中的 CREATE VIEW 冲突.
	var views []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type='view'").
		Scan(&views).Error; err != nil {
		return fmt.Errorf("list sqlite views: %w", err)
	}
	for _, view := range views {
		if err := db.Migrator().DropView(view); err != nil {
			return fmt.Errorf("drop view %s: %w", view, err)
		}
	}

	var tables []string
	err := db.Raw("SELECT name FROM sqlite_master WHERE type='table' AND name != 'sqlite_sequence'").
		Scan(&tables).Error
	if err != nil {
		return fmt.Errorf("list sqlite tables: %w", err)
	}

	for _, table := range tables {
		if err := db.Migrator().DropTable(table); err != nil {
			return fmt.Errorf("drop table %s: %w", table, err)
		}
	}

	return nil
}
