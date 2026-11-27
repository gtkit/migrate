package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

// MySQLTableMatches reports whether the existing table matches the GORM snapshot.
// An existing table with a different definition returns an error.
func MySQLTableMatches(db *gorm.DB, table string, model any) (bool, error) {
	if db == nil || db.Config == nil || db.Dialector == nil {
		return false, errors.New("checking a generated table requires a database connection")
	}
	if db.Name() != "mysql" {
		return false, nil
	}
	actual, options, err := readMySQLDDL(db, table)
	if err != nil || actual == nil {
		return false, err
	}
	capture := &mysqlCreateRecorder{Interface: db.Logger}
	dryRun := db.Session(&gorm.Session{DryRun: true, Logger: capture}).Table(table)
	if err := dryRun.Migrator().CreateTable(model); err != nil {
		return false, fmt.Errorf("render expected MySQL table %q: %w", table, err)
	}
	if capture.count != 1 {
		return false, fmt.Errorf("could not verify MySQL table %q: expected one CREATE TABLE statement", table)
	}
	expected, err := parseMySQLDDL(capture.statement, options.noEscapes)
	if err != nil {
		return false, fmt.Errorf("parse expected MySQL table %q: %w", table, err)
	}
	if len(expected.columns) != len(actual.columns) {
		return false, fmt.Errorf("MySQL table %q has %d columns, expected %d", table, len(actual.columns), len(expected.columns))
	}
	for i, column := range expected.columns {
		name := ddlName(column[0])
		got := actual.columns[i]
		primary := expected.primaryColumn(name)
		if !strings.EqualFold(name, ddlName(got[0])) ||
			!slices.Equal(canonicalColumn(column[1:], primary, options.noEscapes), canonicalColumn(got[1:], primary, options.noEscapes)) {
			return false, fmt.Errorf("MySQL table %q differs at column %q", table, name)
		}
	}
	if !slices.Equal(canonicalKeys(expected, options.noEscapes), canonicalKeys(*actual, options.noEscapes)) {
		return false, fmt.Errorf("MySQL table %q has different indexes or constraints", table)
	}
	if !maps.Equal(canonicalTableOptions(expected.options, options), canonicalTableOptions(actual.options, options)) {
		return false, fmt.Errorf("MySQL table %q has different table options", table)
	}
	return true, nil
}

type mysqlCreateRecorder struct {
	gormlogger.Interface
	statement string
	count     int
}

func (r *mysqlCreateRecorder) Trace(_ context.Context, _ time.Time, sql func() (string, int64), _ error) {
	statement, _ := sql()
	switch upper := strings.ToUpper(strings.TrimSpace(statement)); {
	case strings.HasPrefix(upper, "CREATE TABLE "):
		r.statement = statement
		r.count++
	case strings.HasPrefix(upper, "CREATE "), strings.HasPrefix(upper, "ALTER "):
		r.count++
	}
}

func readMySQLDDL(db *gorm.DB, table string) (*mysqlDDL, mysqlDDLOptions, error) {
	var kind, sqlMode string
	var options mysqlDDLOptions
	err := db.Raw(`
		SELECT t.TABLE_TYPE, @@SESSION.default_storage_engine, s.DEFAULT_CHARACTER_SET_NAME,
		       s.DEFAULT_COLLATION_NAME, @@SESSION.sql_mode
		FROM information_schema.TABLES t
		JOIN information_schema.SCHEMATA s ON BINARY s.SCHEMA_NAME = BINARY t.TABLE_SCHEMA
		WHERE BINARY t.TABLE_SCHEMA = BINARY DATABASE()
		  AND IF(@@lower_case_table_names = 0, BINARY t.TABLE_NAME = BINARY ?, t.TABLE_NAME = ?)
	`, table, table).Row().Scan(&kind, &options.engine, &options.charset, &options.collation, &sqlMode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, options, nil
	}
	if err != nil {
		return nil, options, fmt.Errorf("inspect MySQL table %q: %w", table, err)
	}
	if kind != "BASE TABLE" {
		return nil, options, fmt.Errorf("MySQL object %q is a %s, expected a base table", table, kind)
	}
	options.engine = strings.ToLower(options.engine)
	options.noEscapes = slices.Contains(strings.Split(sqlMode, ","), "NO_BACKSLASH_ESCAPES")
	var name, definition string
	if err := db.Raw("SHOW CREATE TABLE ?", clause.Table{Name: table}).Row().Scan(&name, &definition); err != nil {
		return nil, options, fmt.Errorf("read definition of MySQL table %q: %w", table, err)
	}
	parsed, err := parseMySQLDDL(definition, options.noEscapes)
	if err != nil {
		return nil, options, fmt.Errorf("parse definition of MySQL table %q: %w", table, err)
	}
	return &parsed, options, nil
}

func matchMySQLColumn(table, column string, want []string, after string, actual *mysqlDDL, noEscapes bool) (bool, error) {
	if len(want) == 0 {
		return false, fmt.Errorf("missing definition for MySQL column %s.%s", table, column)
	}
	if actual == nil {
		return false, nil
	}
	for i, term := range actual.columns {
		if !strings.EqualFold(ddlName(term[0]), column) {
			continue
		}
		if !slices.Equal(canonicalColumn(want, false, noEscapes), canonicalColumn(term[1:], false, noEscapes)) {
			return false, fmt.Errorf("MySQL column %s.%s has a different definition", table, column)
		}
		if after != "" && (i == 0 || !strings.EqualFold(ddlName(actual.columns[i-1][0]), after)) {
			return false, fmt.Errorf("MySQL column %s.%s is not immediately after %q", table, column, after)
		}
		return true, nil
	}
	return false, nil
}

// MySQLAddColumnMatches checks the definition and optional position of a column
// using the same ALTER TABLE statement that would create it.
func MySQLAddColumnMatches(db *gorm.DB, table, column, statement string) (bool, error) {
	if db == nil || db.Config == nil || db.Dialector == nil {
		return false, errors.New("checking a generated column requires a database connection")
	}
	if db.Name() != "mysql" {
		return false, nil
	}
	actual, options, err := readMySQLDDL(db, table)
	if err != nil {
		return false, err
	}
	tokens := ddlTokens(statement, options.noEscapes)
	if len(tokens) < 7 || tokens[0] != "alter" || tokens[1] != "table" || ddlName(tokens[2]) != table ||
		tokens[3] != "add" || tokens[4] != "column" || !strings.EqualFold(ddlName(tokens[5]), column) {
		return false, fmt.Errorf("expected ADD COLUMN statement for %s.%s", table, column)
	}
	terms := ddlTerms(tokens[6:])
	for _, option := range terms[1:] {
		if len(option) != 3 || option[1] != "=" || (option[0] != "algorithm" && option[0] != "lock") {
			return false, fmt.Errorf("cannot verify additional ALTER TABLE operations for %s.%s", table, column)
		}
	}
	want, after := terms[0], ""
	if n := len(want); n >= 3 && want[n-2] == "after" {
		after, want = ddlName(want[n-1]), want[:n-2]
	}
	return matchMySQLColumn(table, column, want, after, actual, options.noEscapes)
}
