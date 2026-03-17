package migration

import "time"

// Migration 对应数据库 migrations 表中的一条记录.
type Migration struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	Migration string    `gorm:"type:varchar(255);not null;uniqueIndex"`
	Batch     int       `gorm:"not null;default:0"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
}

// TableName 指定表名.
func (Migration) TableName() string {
	return "migrations"
}
