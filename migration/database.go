package migration

import (
	"context"
	"errors"
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
func DetectDBType(db *gorm.DB) DBType {
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
func CurrentDatabase(db *gorm.DB) string {
	return db.Migrator().CurrentDatabase()
}

// DeleteAllTables 删除所有用户表.
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

func deleteMySQLTables(db *gorm.DB) error {
	ctx := context.Background()
	if db.Statement != nil && db.Statement.Context != nil {
		ctx = db.Statement.Context
	}
	conn, err := reserveMigrationConn(ctx, db)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	rows, err := conn.QueryContext(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'")
	if err != nil {
		return fmt.Errorf("list mysql tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return errors.Join(fmt.Errorf("read mysql table: %w", err), rows.Close())
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list mysql tables: %w", err)
	}
	if len(tables) == 0 {
		return nil
	}

	var original bool
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.foreign_key_checks").Scan(&original); err != nil {
		return fmt.Errorf("read foreign key checks: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION foreign_key_checks = 0"); err != nil {
		discardMigrationConn(conn)
		return fmt.Errorf("disable foreign key checks: %w", err)
	}

	var dropErr error
	for _, table := range tables {
		quoted := "`" + strings.ReplaceAll(table, "`", "``") + "`"
		if _, err := conn.ExecContext(ctx, "DROP TABLE IF EXISTS "+quoted); err != nil {
			dropErr = fmt.Errorf("drop table %s: %w", table, err)
			break
		}
	}
	restore, cancel := context.WithTimeout(context.Background(), defaultLockTimeout)
	defer cancel()
	if _, err := conn.ExecContext(restore, "SET SESSION foreign_key_checks = ?", original); err != nil {
		discardMigrationConn(conn)
		return errors.Join(dropErr, fmt.Errorf("restore foreign key checks: %w", err))
	}
	return dropErr
}

func deletePostgresTables(db *gorm.DB) error {
	var tables []string
	err := db.Raw(`
		SELECT tablename FROM pg_tables 
		WHERE schemaname = 'public'
	`).Scan(&tables).Error
	if err != nil {
		return fmt.Errorf("list postgres tables: %w", err)
	}

	if len(tables) == 0 {
		return nil
	}

	// CASCADE 删除所有表及其依赖
	for _, table := range tables {
		if err := db.Exec("DROP TABLE IF EXISTS \"" + table + "\" CASCADE").Error; err != nil {
			return fmt.Errorf("drop table %s: %w", table, err)
		}
	}

	return nil
}

func deleteSQLiteTables(db *gorm.DB) error {
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
