package migration

import (
	"errors"
	"fmt"
)

// ErrIrreversible 表示当前 migration 的回滚逻辑需要人工补全。
var ErrIrreversible = errors.New("irreversible migration")

// Irreversible 返回一个带详细说明的不可逆 migration 错误。
func Irreversible(details string) error {
	if details == "" {
		details = "manual down migration is required"
	}
	return fmt.Errorf("%w: %s", ErrIrreversible, details)
}
