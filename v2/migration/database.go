package migration

import (
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
	name := db.Dialector.Name()
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
	dbname := CurrentDatabase(db)

	var tables []string
	err := db.Table("information_schema.tables").
		Where("table_schema = ?", dbname).
		Pluck("table_name", &tables).
		Error
	if err != nil {
		return fmt.Errorf("list mysql tables: %w", err)
	}

	if len(tables) == 0 {
		return nil
	}

	// 关闭外键检查、删表、恢复必须在同一连接上完成：foreign_key_checks 是会话级变量，
	// 若经连接池分发，关闭态可能落不到删表连接，或残留污染被业务复用的池内连接.
	return db.Connection(func(conn *gorm.DB) (err error) {
		if err = conn.Exec("SET foreign_key_checks = 0").Error; err != nil {
			return fmt.Errorf("disable foreign key checks: %w", err)
		}
		// 无论删表成功与否，都在该连接归还连接池前恢复外键检查；
		// 仅在没有更早错误时才上报恢复失败，避免掩盖删表错误.
		defer func() {
			if restoreErr := conn.Exec("SET foreign_key_checks = 1").Error; restoreErr != nil && err == nil {
				err = fmt.Errorf("enable foreign key checks: %w", restoreErr)
			}
		}()

		for _, table := range tables {
			// 用 conn 原生 Exec 删表：db.Connection 提供的是 *sql.Conn，
			// conn.Migrator() 内部需要 *sql.DB 会返回 "invalid db"，故直接执行 raw DROP.
			quoted := "`" + strings.ReplaceAll(table, "`", "``") + "`"
			if err = conn.Exec("DROP TABLE IF EXISTS " + quoted).Error; err != nil {
				return fmt.Errorf("drop table %s: %w", table, err)
			}
		}
		return nil
	})
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
