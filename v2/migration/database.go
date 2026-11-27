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
//
// ⚠️ 低层清库工具：不经 WithAllowFresh 授权、不加迁移锁、没有 --force 保护，
// 调用即删、数据不可恢复.仅限在自己完全掌控的测试库上使用（如集成测试清场）；
// 常规"清库重放"请使用 Migrator.Fresh——它带显式授权、advisory lock 与迁移重放.
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
	// 使用专属 *sql.Conn 保存原值，恢复失败时物理关闭连接，避免把状态未知的会话归还连接池.
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

	var original bool
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.foreign_key_checks").Scan(&original); err != nil {
		return fmt.Errorf("read foreign key checks: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION foreign_key_checks = 0"); err != nil {
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

	// 无论删表成功与否都恢复外键检查原值.恢复使用独立超时上下文：业务 ctx 已取消时仍要
	// 尝试把会话复位.恢复失败则标坏连接（Close 物理关闭），并且仅在没有更早错误时上报，
	// 避免掩盖删表错误.
	restoreCtx, cancel := context.WithTimeout(context.Background(), lockReleaseTimeout)
	defer cancel()
	if _, err := conn.ExecContext(restoreCtx, "SET SESSION foreign_key_checks = ?", original); err != nil {
		markBadConn(conn)
		if dropErr == nil {
			return fmt.Errorf("restore foreign key checks: %w", err)
		}
	}
	return dropErr
}

// quotePostgresIdent 双引号转义 PostgreSQL 标识符.
func quotePostgresIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func deletePostgresTables(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var views, tables []string
		if err := tx.Raw(`SELECT viewname FROM pg_catalog.pg_views WHERE schemaname = 'public' ORDER BY viewname`).
			Scan(&views).Error; err != nil {
			return fmt.Errorf("list postgres views: %w", err)
		}
		if err := tx.Raw(`SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = 'public' ORDER BY tablename`).
			Scan(&tables).Error; err != nil {
			return fmt.Errorf("list postgres tables: %w", err)
		}

		for i, table := range tables {
			tables[i] = `"public".` + quotePostgresIdent(table)
		}
		if len(tables) != 0 {
			// 锁定清理目标，阻止并发 attach、detach 或其他 DDL 改变依赖。
			if err := tx.Exec("LOCK TABLE ONLY " + strings.Join(tables, ", ONLY ") + " IN ACCESS EXCLUSIVE MODE").Error; err != nil {
				return fmt.Errorf("lock postgres cleanup targets: %w", err)
			}
			var outsidePublic bool
			if err := tx.Raw(`
				SELECT EXISTS (
					SELECT 1 FROM pg_catalog.pg_inherits i
					JOIN pg_catalog.pg_class parent ON parent.oid = i.inhparent
					JOIN pg_catalog.pg_namespace parent_schema ON parent_schema.oid = parent.relnamespace
					JOIN pg_catalog.pg_class child ON child.oid = i.inhrelid
					JOIN pg_catalog.pg_namespace child_schema ON child_schema.oid = child.relnamespace
					WHERE (parent_schema.nspname = 'public' AND parent.relkind IN ('r', 'p') AND child_schema.nspname <> 'public')
					   OR (child_schema.nspname = 'public' AND child.relkind IN ('r', 'p') AND parent_schema.nspname <> 'public')
				)
			`).Scan(&outsidePublic).Error; err != nil {
				return fmt.Errorf("check postgres cleanup dependencies: %w", err)
			}
			if outsidePublic {
				return fmt.Errorf("postgres cleanup refused: inheritance or partition relationship crosses the public schema")
			}
		}

		if len(views) != 0 {
			for i, view := range views {
				views[i] = `"public".` + quotePostgresIdent(view)
			}
			if err := tx.Exec("DROP VIEW " + strings.Join(views, ", ") + " RESTRICT").Error; err != nil {
				return fmt.Errorf("drop postgres public views: %w", err)
			}
		}
		if len(tables) != 0 {
			if err := tx.Exec("DROP TABLE " + strings.Join(tables, ", ") + " RESTRICT").Error; err != nil {
				return fmt.Errorf("drop postgres public tables: %w", err)
			}
		}
		return nil
	})
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
