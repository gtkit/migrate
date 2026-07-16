package migration

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"gorm.io/gorm"
)

// defaultLockTimeout 是获取迁移锁的默认最长等待时间.
const defaultLockTimeout = 10 * time.Second

// migrationLock 迁移锁接口.
type migrationLock interface {
	// Acquire 获取锁，返回释放函数.
	Acquire(ctx context.Context) (release func(), err error)
}

// newLock 根据数据库类型创建对应的迁移锁.
// lockName 用于区分不同项目在同一数据库上的迁移锁.
// timeout 为获取锁的最长等待时间，<=0 时回退到 defaultLockTimeout.
func newLock(db *gorm.DB, dbType DBType, lockName string, timeout time.Duration) migrationLock {
	if timeout <= 0 {
		timeout = defaultLockTimeout
	}
	switch dbType {
	case DBTypeMySQL:
		return &mysqlLock{db: db, lockName: lockName, timeout: timeout}
	case DBTypePostgres:
		return &postgresLock{db: db, lockKey: hashLockName(lockName), timeout: timeout}
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
	timeout  time.Duration
}

func (l *mysqlLock) Acquire(ctx context.Context) (func(), error) {
	// GET_LOCK 的超时参数以秒为单位，至少为 1 秒.
	timeoutSec := max(1, int(l.timeout.Seconds()))

	// 通过 Begin 拿到专属连接，确保获取锁和释放锁在同一连接上
	session := l.db.WithContext(ctx).Session(&gorm.Session{PrepareStmt: false})
	tx := session.Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("begin lock session: %w", tx.Error)
	}

	var result int
	if err := tx.Raw("SELECT GET_LOCK(?, ?)", l.lockName, timeoutSec).Scan(&result).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("acquire mysql lock %q: %w", l.lockName, err)
	}
	if result != 1 {
		tx.Rollback()
		return nil, fmt.Errorf("failed to acquire mysql advisory lock %q (timeout %ds)", l.lockName, timeoutSec)
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
	timeout time.Duration
}

func (l *postgresLock) Acquire(ctx context.Context) (func(), error) {
	// 通过 Begin 拿到专属连接
	tx := l.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("begin lock session: %w", tx.Error)
	}

	// pg_advisory_lock 不受 lock_timeout 约束，用 statement_timeout 给这次获取锁的语句加超时，
	// 避免另一个实例正在执行长迁移时本实例无限阻塞. SET LOCAL 仅作用于当前事务连接.
	timeoutMS := max(int64(1), l.timeout.Milliseconds())
	if err := tx.Exec(fmt.Sprintf("SET LOCAL statement_timeout = %d", timeoutMS)).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("set postgres lock statement_timeout: %w", err)
	}

	if err := tx.Exec("SELECT pg_advisory_lock(?)", l.lockKey).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("acquire postgres lock (key=%d, timeout %dms): %w", l.lockKey, timeoutMS, err)
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
