package migration

import (
	"cmp"
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

// mysqlLockNameMax 是 MySQL GET_LOCK 锁名的长度上限（MySQL 5.7+ 强制，超长直接报错）.
const mysqlLockNameMax = 64

// newLock 根据数据库类型创建对应的迁移锁.
// lockName 为空表示使用默认：MySQL 按"库名 + 账本表名"在首次获取时派生
// （GET_LOCK 命名空间是整个实例全局的，固定锁名会让同实例不同库互相等锁）；
// PostgreSQL advisory lock 本身按库隔离，沿用 defaultLockName.
// timeout 为获取锁的最长等待时间，<=0 时回退到 defaultLockTimeout.
func newLock(db *gorm.DB, dbType DBType, lockName, tableName string, timeout time.Duration) migrationLock {
	if timeout <= 0 {
		timeout = defaultLockTimeout
	}
	switch dbType {
	case DBTypeMySQL:
		return &mysqlLock{db: db, lockName: lockName, tableName: tableName, timeout: timeout}
	case DBTypePostgres:
		return &postgresLock{db: db, lockKey: hashLockName(cmp.Or(lockName, defaultLockName)), timeout: timeout}
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

// deriveMySQLLockName 由数据库名与账本表名稳定派生 MySQL 锁名.
// 超过 64 字符时截断并追加 FNV-64a 哈希后缀，保证稳定且不同输入不碰撞.
func deriveMySQLLockName(dbName, tableName string) string {
	name := "migrate:" + dbName + ":" + tableName
	if len(name) <= mysqlLockNameMax {
		return name
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	suffix := fmt.Sprintf(":%x", h.Sum64())
	return name[:mysqlLockNameMax-len(suffix)] + suffix
}

// mysqlLock 使用 MySQL GET_LOCK 实现的迁移锁.
// 重要：GET_LOCK 绑定到连接，必须使用同一个连接获取和释放.
type mysqlLock struct {
	db        *gorm.DB
	lockName  string // 空表示按库名与 tableName 派生
	tableName string
	timeout   time.Duration
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

	lockName := l.lockName
	if lockName == "" {
		var dbName sql.NullString
		if err := conn.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&dbName); err != nil {
			markBadConn(conn)
			_ = conn.Close()
			return nil, fmt.Errorf("resolve database name for lock: %w", err)
		}
		lockName = deriveMySQLLockName(dbName.String, l.tableName)
	}

	var result sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", lockName, timeoutSec).Scan(&result); err != nil {
		// 查询出错（网络/取消）时服务端可能已授锁而客户端不知道：
		// 标坏连接物理关闭、结束会话，未确认的锁随会话释放，绝不带锁归还连接池.
		markBadConn(conn)
		_ = conn.Close()
		return nil, fmt.Errorf("acquire mysql lock %q: %w", lockName, err)
	}
	if !result.Valid || result.Int64 != 1 {
		// 服务端明确拒绝（等待超时），连接状态确定，可正常归还.
		_ = conn.Close()
		return nil, fmt.Errorf("%w: mysql advisory lock %q not acquired within %ds", ErrLockNotAcquired, lockName, timeoutSec)
	}

	release := func() {
		// 用独立上下文释放：即使业务 ctx 已取消/超时，RELEASE_LOCK 仍能执行.
		relCtx, cancel := context.WithTimeout(context.Background(), lockReleaseTimeout)
		defer cancel()

		var released sql.NullInt64
		err := conn.QueryRowContext(relCtx, "SELECT RELEASE_LOCK(?)", lockName).Scan(&released)
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
		// SET 出错后会话状态不确定，物理关闭，不归还连接池.
		markBadConn(conn)
		_ = conn.Close()
		return nil, fmt.Errorf("set postgres lock statement_timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", l.lockKey); err != nil {
		// 出错时服务端可能已授锁（网络错误），且会话还带着 statement_timeout：
		// 标坏连接物理关闭、结束会话——未确认的锁与超时设置随会话一并消失，绝不归还连接池.
		markBadConn(conn)
		_ = conn.Close()
		return nil, fmt.Errorf("acquire postgres lock (key=%d, timeout %dms): %w", l.lockKey, timeoutMS, err)
	}

	release := func() {
		relCtx, cancel := context.WithTimeout(context.Background(), lockReleaseTimeout)
		defer cancel()

		// 先清掉本连接的 statement_timeout（连接会回池复用），再解锁.
		resetStatementTimeout(conn)

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

// markBadConn 将专属连接标记为坏连接：随后的 Close 会物理关闭并结束数据库会话，
// 而非归还连接池.用于锁状态或会话状态不确定的场景（未确认的锁随会话结束而释放）.
func markBadConn(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

// resetStatementTimeout 将连接的会话级 statement_timeout 复位为 0（锁释放正常路径使用）.
// 使用独立超时上下文（不受业务 ctx 取消影响）；复位失败时标记坏连接，
// 使随后的 Close 物理关闭而非归还连接池，绝不让带超时设置的连接被业务复用.
func resetStatementTimeout(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), lockReleaseTimeout)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "SET statement_timeout = 0"); err != nil {
		markBadConn(conn)
	}
}

// noopLock 空锁实现，用于不支持 advisory lock 的数据库.
type noopLock struct{}

func (l *noopLock) Acquire(_ context.Context) (func(), error) {
	return func() {}, nil
}
