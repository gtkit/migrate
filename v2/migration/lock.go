package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"hash/fnv"
	"time"

	"gorm.io/gorm"
)

// defaultLockTimeout 是获取迁移锁的默认最长等待时间.
const defaultLockTimeout = 10 * time.Second

// lockReleaseTimeout 是释放锁时独立上下文的超时（与业务上下文无关，保证取消后仍能释放）.
const lockReleaseTimeout = 10 * time.Second

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

	// 用专属 *sql.Conn 获取锁：GET_LOCK 是会话锁，获取与释放必须在同一连接上，
	// 且不能用绑业务 ctx 的事务（取消后无法可靠释放）.
	sqlDB, err := l.db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB for lock: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserve lock connection: %w", err)
	}

	var result sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", l.lockName, timeoutSec).Scan(&result); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("acquire mysql lock %q: %w", l.lockName, err)
	}
	if !result.Valid || result.Int64 != 1 {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to acquire mysql advisory lock %q (timeout %ds)", l.lockName, timeoutSec)
	}

	release := func() {
		// 用独立上下文释放：即使业务 ctx 已取消/超时，RELEASE_LOCK 仍能执行.
		relCtx, cancel := context.WithTimeout(context.Background(), lockReleaseTimeout)
		defer cancel()

		var released sql.NullInt64
		err := conn.QueryRowContext(relCtx, "SELECT RELEASE_LOCK(?)", l.lockName).Scan(&released)
		if err != nil || !released.Valid || released.Int64 != 1 {
			// 释放不确定：标记为坏连接，Close 时物理关闭、结束会话，确保命名锁被释放.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
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
	// 用专属 *sql.Conn：pg_advisory_lock（会话级）获取与释放须同一连接，且不绑业务 ctx.
	sqlDB, err := l.db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB for lock: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserve lock connection: %w", err)
	}

	// pg_advisory_lock 不受 lock_timeout 约束，用 statement_timeout 给获取锁的语句加超时.
	timeoutMS := max(int64(1), l.timeout.Milliseconds())
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SET statement_timeout = %d", timeoutMS)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set postgres lock statement_timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", l.lockKey); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("acquire postgres lock (key=%d, timeout %dms): %w", l.lockKey, timeoutMS, err)
	}

	release := func() {
		relCtx, cancel := context.WithTimeout(context.Background(), lockReleaseTimeout)
		defer cancel()

		// 先清掉本连接的 statement_timeout（连接会回池复用），再解锁.
		_, _ = conn.ExecContext(relCtx, "SET statement_timeout = 0")

		var released sql.NullBool
		err := conn.QueryRowContext(relCtx, "SELECT pg_advisory_unlock($1)", l.lockKey).Scan(&released)
		if err != nil || !released.Valid || !released.Bool {
			// 释放不确定：标记为坏连接，Close 时物理关闭、结束会话，确保锁被释放.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}
	return release, nil
}

// noopLock 空锁实现，用于不支持 advisory lock 的数据库.
type noopLock struct{}

func (l *noopLock) Acquire(_ context.Context) (func(), error) {
	return func() {}, nil
}
