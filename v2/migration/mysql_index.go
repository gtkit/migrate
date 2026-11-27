package migration

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"gorm.io/gorm"
)

// MySQLIndexMatches reports whether the generated index exists with the same definition.
// A conflicting index or an inspection error is returned to the migration.
func MySQLIndexMatches(db *gorm.DB, table, index string, unique bool, columns ...string) (bool, error) {
	if db == nil || db.Config == nil || db.Dialector == nil || db.Name() != "mysql" {
		return false, errors.New("checking a generated MySQL index requires a MySQL connection")
	}
	if len(columns) == 0 {
		return false, errors.New("checking a generated MySQL index requires at least one column")
	}
	actual, options, err := readMySQLDDL(db, table)
	if err != nil {
		return false, fmt.Errorf("read index %s on %s: %w", index, table, err)
	}
	if actual == nil {
		return false, nil
	}
	expected := []string{"key", quoteMySQLDDLName(index), "("}
	if unique {
		expected = append([]string{"unique"}, expected...)
	}
	for i, column := range columns {
		if i > 0 {
			expected = append(expected, ",")
		}
		expected = append(expected, quoteMySQLDDLName(column))
	}
	expected = append(expected, ")")
	for _, key := range actual.keys {
		at := namedMySQLIndex(key)
		if at < 0 || !strings.EqualFold(ddlName(key[at]), index) {
			continue
		}
		key = slices.Clone(key)
		key[at] = expected[namedMySQLIndex(expected)]
		// MySQL column names are case-insensitive, including KEY parts.
		if canonicalKey(foldMySQLIndexColumns(key, at), options.noEscapes) !=
			canonicalKey(foldMySQLIndexColumns(expected, namedMySQLIndex(expected)), options.noEscapes) {
			return false, fmt.Errorf("MySQL index %s on %s has a different definition", index, table)
		}
		return true, nil
	}
	return false, nil
}

func namedMySQLIndex(key []string) int {
	if len(key) < 3 {
		return -1
	}
	switch key[0] {
	case "key", "index":
		return 1
	case "unique", "fulltext", "spatial":
		if key[1] == "key" || key[1] == "index" {
			return 2
		}
	}
	return -1
}

func foldMySQLIndexColumns(key []string, nameAt int) []string {
	open := slices.Index(key[nameAt+1:], "(")
	if open < 0 {
		return key
	}
	open += nameAt + 1
	end := ddlCloseParen(key, open)
	for i := open + 1; i < end; i++ {
		if strings.HasPrefix(key[i], "`") {
			key[i] = strings.ToLower(key[i])
		}
	}
	return key
}

func quoteMySQLDDLName(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
