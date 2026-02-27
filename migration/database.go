package migration

import (
	"errors"

	"github.com/gtkit/migrate/console"
	"gorm.io/gorm"
)

// SQLDB DB 对象
// var DB *gorm.DB.
//var SQLDB *sql.DB

// 获取当前数据库名称.
func CurrentDatabase(db *gorm.DB) (dbname string) {
	dbname = db.Migrator().CurrentDatabase()

	return
}

func DeleteAllTables(dbType string, db *gorm.DB) error {
	var err error
	switch dbType {
	case "mysql":
		err = deleteMySQLTables(db)
	case "sqlite":
		err = deleteAllSqliteTables(db)
	default:
		panic(errors.New("database connection not supported"))
	}

	return err
}

// 删除所有 sqlite 表.
func deleteAllSqliteTables(db *gorm.DB) error {
	tables := []string{}

	// 读取所有数据表
	err := db.Select(&tables, "SELECT name FROM sqlite_master WHERE type='table'").Error
	if err != nil {
		return err
	}

	// 删除所有表
	for _, table := range tables {
		err := db.Migrator().DropTable(table)
		if err != nil {
			return err
		}
	}
	return nil
}

// 删除所有 MySQL 表.
func deleteMySQLTables(db *gorm.DB) error {
	dbname := CurrentDatabase(db)
	tables := []string{}

	// 读取所有数据表
	err := db.Table("information_schema.tables").
		Where("table_schema = ?", dbname).
		Pluck("table_name", &tables).
		Error
	if err != nil {
		return err
	}

	// 暂时关闭外键检测
	db.Exec("SET foreign_key_checks = 0;")

	// 删除所有表
	for _, table := range tables {
		err := db.Migrator().DropTable(table)
		if err != nil {
			return err
		}
	}

	// 开启 MySQL 外键检测
	db.Exec("SET foreign_key_checks = 1;")
	return nil
}

// TableName 获取表名称.
func TableName(obj any, db *gorm.DB) string {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(obj); err != nil {
		console.Error("parse table name error: " + err.Error())
		return ""
	}
	return stmt.Schema.Table
}
