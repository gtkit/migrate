package migration

import (
	"errors"
	"fmt"
)

var (
	// ErrIrreversible 表示当前 migration 的回滚逻辑需要人工补全。
	ErrIrreversible = errors.New("irreversible migration")

	// ErrNoMigrations 表示注册表为空（通常是漏 import 迁移包）。
	ErrNoMigrations = errors.New("no migrations registered")

	// ErrDuplicateRegistration 表示同名迁移被注册多次。
	ErrDuplicateRegistration = errors.New("duplicate migration registration")

	// ErrRegistryDrift 表示账本中存在当前 binary 未注册的已应用迁移。
	ErrRegistryDrift = errors.New("migration registry drift")

	// ErrLockNotAcquired 表示在等待时间内未获得迁移锁（另一实例可能正在迁移）。
	ErrLockNotAcquired = errors.New("migration lock not acquired")

	errDBRequired = errors.New("migrate: database connection is required")
)

// Irreversible 返回一个带详细说明的不可逆 migration 错误。
func Irreversible(details string) error {
	if details == "" {
		details = "manual down migration is required"
	}
	return fmt.Errorf("%w: %s", ErrIrreversible, details)
}
