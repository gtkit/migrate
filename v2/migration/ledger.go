package migration

import (
	"context"
	"errors"
	"fmt"

	mysql "github.com/go-sql-driver/mysql"
)

// readLedger distinguishes a new database from an unsuccessful read.
func (m *Migrator) readLedger(ctx context.Context) ([]Migration, error) {
	if m.configErr != nil {
		return nil, m.configErr
	}
	if m.db == nil {
		return nil, errDBRequired
	}

	if m.dbType == DBTypeSQLite {
		var exists bool
		if err := m.db.WithContext(ctx).
			Raw("SELECT EXISTS (SELECT 1 FROM main.sqlite_master WHERE name = ? COLLATE NOCASE AND type IN ('table', 'view'))", m.tableName).
			Scan(&exists).Error; err != nil {
			return nil, fmt.Errorf("check sqlite migration ledger %s: %w", m.tableName, err)
		}
		if !exists {
			return nil, nil
		}
	}

	var records []Migration
	if err := m.records(ctx).Select("id", "migration", "batch", "created_at").Find(&records).Error; err != nil {
		if missingLedger(m.dbType, err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read migration ledger %s: %w", m.tableName, err)
	}
	return records, nil
}

func missingLedger(dialect DBType, err error) bool {
	switch dialect {
	case DBTypeMySQL:
		dbErr, ok := errors.AsType[*mysql.MySQLError](err)
		return ok && dbErr.Number == 1146
	case DBTypePostgres:
		dbErr, ok := errors.AsType[interface {
			error
			SQLState() string
		}](err)
		return ok && dbErr.SQLState() == "42P01"
	default:
		return false
	}
}
