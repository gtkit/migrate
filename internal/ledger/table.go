package ledger

import (
	"fmt"
	"regexp"
)

// DefaultTableName is the default migration record table.
const DefaultTableName = "migrations"

var tableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateTableName checks the migration record table identifier.
func ValidateTableName(name string) error {
	if len(name) > 63 || !tableNamePattern.MatchString(name) {
		return fmt.Errorf("invalid migrations table name %q: use up to 63 ASCII letters, digits or underscores, starting with a letter or underscore", name)
	}
	return nil
}
