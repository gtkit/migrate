package migration

import (
	"context"
	"fmt"
	"hash/fnv"

	"gorm.io/gorm"
)

// migrationLock 迁移锁接口.
type migrationLock interface {
	// Acquire 获取锁，返回释放函数.
	Acquire(ctx context.Context) (release func(), err error)
}

// newLock 根据数据库类型创建对应的迁移锁.
// lockName 用于区分不同项目在同一数据库上的迁移锁.
func newLock(db *gorm.DB, dbType DBType, lockName string) migrationLock {
	switch dbType {
	case DBTypeMySQL:
		return &mysqlLock{db: db, lockName: lockName}
	case DBTypePostgres:
		return &postgresLock{db: db, lockKey: hashLockName(lockName)}
	default:
		// SQLite 等不支持 advisory lock 的数据库，使用空锁（单进程场景可接受）
		return &noopLock{}
	}
}

// hashLockName 将字符串 lock name 转为 int64，用于 PostgreSQL advisory lock.
func hashLockName(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64() & 0x7FFFFFFFFFFFFFFF) // 保证正数
}

// mysqlLock 使用 MySQL GET_LOCK 实现的迁移锁.
// 重要：GET_LOCK 绑定到连接，必须使用同一个连接获取和释放.
type mysqlLock struct {
	db       *gorm.DB
	lockName string
}

func (l *mysqlLock) Acquire(ctx context.Context) (func(), error) {
	const lockTimeout = 10 // 秒

	// 通过 Begin 拿到专属连接，确保获取锁和释放锁在同一连接上
	session := l.db.WithContext(ctx).Session(&gorm.Session{PrepareStmt: false})
	tx := session.Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("begin lock session: %w", tx.Error)
	}

	var result int
	if err := tx.Raw("SELECT GET_LOCK(?, ?)", l.lockName, lockTimeout).Scan(&result).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("acquire mysql lock %q: %w", l.lockName, err)
	}
	if result != 1 {
		tx.Rollback()
		return nil, fmt.Errorf("failed to acquire mysql advisory lock %q (timeout %ds)", l.lockName, lockTimeout)
	}

	release := func() {
		_ = tx.Exec("SELECT RELEASE_LOCK(?)", l.lockName).Error
		tx.Rollback() // 释放连接回连接池
	}
	return release, nil
}

// postgresLock 使用 PostgreSQL pg_advisory_lock 实现的迁移锁.
// 重要：pg_advisory_lock 绑定到连接，必须使用同一个连接获取和释放.
type postgresLock struct {
	db      *gorm.DB
	lockKey int64
}

func (l *postgresLock) Acquire(ctx context.Context) (func(), error) {
	// 通过 Begin 拿到专属连接
	tx := l.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("begin lock session: %w", tx.Error)
	}

	if err := tx.Exec("SELECT pg_advisory_lock(?)", l.lockKey).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("acquire postgres lock (key=%d): %w", l.lockKey, err)
	}

	release := func() {
		_ = tx.Exec("SELECT pg_advisory_unlock(?)", l.lockKey).Error
		tx.Rollback() // 释放连接回连接池
	}
	return release, nil
}

// noopLock 空锁实现，用于不支持 advisory lock 的数据库.
type noopLock struct{}

func (l *noopLock) Acquire(_ context.Context) (func(), error) {
	return func() {}, nil
}
