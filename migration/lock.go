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
	wait, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	conn, err := reserveMigrationConn(wait, l.db)
	if err != nil {
		return nil, err
	}
	var result sql.NullInt64
	if err := conn.QueryRowContext(wait, "SELECT GET_LOCK(?, ?)", l.lockName, l.timeout.Seconds()).Scan(&result); err != nil {
		discardMigrationConn(conn)
		return nil, fmt.Errorf("acquire mysql lock %q: %w", l.lockName, err)
	}
	if !result.Valid || result.Int64 != 1 {
		_ = conn.Close()
		return nil, fmt.Errorf("mysql advisory lock %q was not acquired", l.lockName)
	}
	return func() {
		release, cancel := context.WithTimeout(context.Background(), defaultLockTimeout)
		defer cancel()
		var unlocked sql.NullInt64
		if err := conn.QueryRowContext(release, "SELECT RELEASE_LOCK(?)", l.lockName).Scan(&unlocked); err != nil || !unlocked.Valid || unlocked.Int64 != 1 {
			discardMigrationConn(conn)
			return
		}
		_ = conn.Close()
	}, nil
}

// postgresLock 用独立事务持有 PostgreSQL advisory lock，事务结束时释放.
type postgresLock struct {
	db      *gorm.DB
	lockKey int64
	timeout time.Duration
}

func (l *postgresLock) Acquire(ctx context.Context) (func(), error) {
	wait, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	conn, err := reserveMigrationConn(wait, l.db)
	if err != nil {
		return nil, err
	}
	// 锁的生命周期由 release 控制，即使业务被取消也保持互斥直到退出。
	if _, err := conn.ExecContext(wait, "BEGIN"); err != nil {
		discardMigrationConn(conn)
		return nil, fmt.Errorf("begin postgres lock transaction: %w", err)
	}
	release := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), defaultLockTimeout)
		defer cancel()
		if _, err := conn.ExecContext(cleanup, "ROLLBACK"); err != nil {
			discardMigrationConn(conn)
			return
		}
		_ = conn.Close()
	}
	if _, err := conn.ExecContext(wait, fmt.Sprintf("SET LOCAL statement_timeout = %d", max(int64(1), l.timeout.Milliseconds()))); err != nil {
		release()
		return nil, fmt.Errorf("set postgres lock timeout: %w", err)
	}
	if _, err := conn.ExecContext(wait, "SELECT pg_advisory_xact_lock($1)", l.lockKey); err != nil {
		release()
		return nil, fmt.Errorf("acquire postgres migration lock: %w", err)
	}
	return release, nil
}

func reserveMigrationConn(ctx context.Context, db *gorm.DB) (*sql.Conn, error) {
	pool, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get migration connection pool: %w", err)
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("reserve migration connection: %w", err)
	}
	return conn, nil
}

func discardMigrationConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}

// noopLock 空锁实现，用于不支持 advisory lock 的数据库.
type noopLock struct{}

func (l *noopLock) Acquire(_ context.Context) (func(), error) {
	return func() {}, nil
}
