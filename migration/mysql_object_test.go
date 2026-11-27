package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const matchingMySQLTable = "CREATE TABLE `accounts` (" +
	"`id` int NOT NULL AUTO_INCREMENT, `age` int NOT NULL DEFAULT '0', " +
	"`name` varchar(32) DEFAULT NULL, `created_at` datetime DEFAULT NULL, PRIMARY KEY (`id`), " +
	"KEY `idx_age` (`age`), CONSTRAINT `age_check` CHECK ((`age` >= 0))) " +
	"ENGINE=InnoDB AUTO_INCREMENT=35 DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci"

type mysqlShapeSnapshot struct {
	ID        int32      `gorm:"type:int;primaryKey;autoIncrement"`
	Age       int32      `gorm:"type:int;not null;default:0;index:idx_age;check:age_check,age >= 0"`
	Name      *string    `gorm:"type:varchar(32)"`
	CreatedAt *time.Time `gorm:"type:datetime(0)"`
}

func TestMySQLObjectDefinitionChecks(t *testing.T) {
	t.Parallel()
	fixture := &mysqlDDLTestConnector{ddl: matchingMySQLTable}
	pool := sql.OpenDB(fixture)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	table := func() (bool, error) { return MySQLTableMatches(db, "accounts", &mysqlShapeSnapshot{}) }
	column := func(name, definition, after string) (bool, error) {
		return MySQLColumnMatches(db, "accounts", name, definition, after)
	}
	index := func(name string, unique bool, columns ...string) (bool, error) {
		return MySQLIndexMatches(db, "accounts", name, unique, columns...)
	}
	check := func(label string, want bool, wantError string, check func() (bool, error)) {
		t.Helper()
		ok, err := check()
		if ok != want || (wantError == "" && err != nil) ||
			(wantError != "" && (err == nil || !strings.Contains(err.Error(), wantError))) {
			t.Errorf("%s: got (%t, %v), want (%t, %q)", label, ok, err, want, wantError)
		}
	}
	check("table matches", true, "", table)
	check("column matches", true, "", func() (bool, error) { return column("age", "INTEGER NOT NULL DEFAULT +0", "id") })
	check("column missing", false, "", func() (bool, error) { return column("missing", "varchar(32)", "") })
	check("column missing definition", false, "definition", func() (bool, error) { return column("age", "", "") })
	check("column differs", false, "different definition", func() (bool, error) { return column("age", "int NOT NULL DEFAULT 1", "") })
	check("position differs", false, "immediately after", func() (bool, error) { return column("age", "int NOT NULL DEFAULT 0", "name") })
	check("index matches", true, "", func() (bool, error) { return index("IDX_AGE", false, "aGe") })
	check("index missing", false, "", func() (bool, error) { return index("missing", false, "age") })
	check("index with no parts", false, "at least one", func() (bool, error) { return index("idx_age", false) })
	check("index differs", false, "different definition", func() (bool, error) { return index("idx_age", true, "age") })
	for _, tc := range []struct{ old, replacement, want string }{
		{"`name` varchar(32) DEFAULT NULL, ", "", "columns"},
		{"varchar(32)", "varchar(64)", "column"},
		{"KEY `idx_age`", "UNIQUE KEY `idx_age`", "indexes or constraints"},
		{"KEY `idx_age` (`age`)", "KEY `idx_age` (`age`) /*!80000 INVISIBLE */", "indexes or constraints"},
		{"CHECK ((`age` >= 0))", "CHECK ((`age` >= 1))", "indexes or constraints"},
		{"ENGINE=InnoDB", "ENGINE=MyISAM", "table options"},
		{"DEFAULT CHARSET=utf8mb4", "DEFAULT CHARSET=latin1", "table options"},
	} {
		fixture.ddl = strings.Replace(matchingMySQLTable, tc.old, tc.replacement, 1)
		check(tc.replacement, false, tc.want, table)
	}
	fixture.ddl = "CREATE VIEW accounts AS SELECT 1"
	check("invalid CREATE output", false, "parse definition", table)
	fixture.ddl = matchingMySQLTable
	fixture.kind = "VIEW"
	check("wrong object kind", false, "VIEW", table)
	fixture.kind = ""
	fixture.err = errors.New("metadata unavailable")
	check("metadata read error", false, "metadata unavailable", table)
	fixture.err = nil
	fixture.missing = true
	check("table missing", false, "", table)
	check("index table missing", false, "", func() (bool, error) { return index("idx_age", false, "age") })
	check("column table missing", false, "", func() (bool, error) { return column("age", "int", "") })
}

func TestCanonicalMySQLDDL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		left, right string
		noEscapes   bool
		equal       bool
	}{
		{"BOOLEAN DEFAULT TRUE", "tinyint(1) DEFAULT '1'", false, true},
		{"TIMESTAMP(0) NULL DEFAULT CURRENT_TIMESTAMP(0)", "timestamp DEFAULT CURRENT_TIMESTAMP", false, true},
		{"DECIMAL(8,2) DEFAULT -2.3", "DECIMAL(8,2) DEFAULT '-02.30'", false, true},
		{"VARCHAR(20) COMMENT 'It''s fine'", "VARCHAR(20) COMMENT 'It\\'s fine'", false, true},
		{"ENUM('A,B','x') DEFAULT 'A,B'", "enum('A,B','x') default 'A,B'", false, true},
		{"VARCHAR(32) DEFAULT 'A'", "VARCHAR(32) DEFAULT 'a'", false, false},
		{"VARCHAR(32) DEFAULT 'a\nb'", "VARCHAR(32) DEFAULT 'ab'", true, false},
		{"INT(4) ZEROFILL", "INT(5) ZEROFILL", false, false},
		{"DATETIME(3)", "DATETIME", false, false},
		{"INT DEFAULT (1+2)", "INT DEFAULT (1+3)", false, false},
	} {
		left := canonicalColumn(ddlTokens(tc.left, tc.noEscapes), false, tc.noEscapes)
		right := canonicalColumn(ddlTokens(tc.right, tc.noEscapes), false, tc.noEscapes)
		if slices.Equal(left, right) != tc.equal {
			t.Errorf("comparison of %s and %s: %q vs %q, expected equal=%t", tc.left, tc.right, left, right, tc.equal)
		}
	}
	if _, err := parseMySQLDDL("create table broken (id integer", false); err == nil {
		t.Error("accepted an incomplete definition")
	}
	if _, err := parseMySQLDDL("create table broken (id)", false); err == nil {
		t.Error("accepted an incomplete column")
	}
}

type mysqlDDLTestConnector struct {
	ddl, kind string
	err       error
	missing   bool
}

func (c *mysqlDDLTestConnector) Connect(context.Context) (driver.Conn, error) {
	return mysqlDDLTestConn{c}, nil
}

func (*mysqlDDLTestConnector) Driver() driver.Driver { return nil }

type mysqlDDLTestConn struct{ *mysqlDDLTestConnector }

func (mysqlDDLTestConn) Close() error { return nil }
func (mysqlDDLTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}

func (mysqlDDLTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}

func (c mysqlDDLTestConn) QueryContext(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.err != nil {
		return nil, c.err
	}
	switch {
	case strings.Contains(query, "information_schema.TABLES"):
		result := &mysqlDDLTestRow{columns: []string{"table_type", "engine", "charset", "collation", "sql_mode"}}
		if !c.missing {
			kind := c.kind
			if kind == "" {
				kind = "BASE TABLE"
			}
			result.values = []driver.Value{kind, "InnoDB", "utf8mb4", "utf8mb4_0900_ai_ci", ""}
		}
		return result, nil
	case strings.HasPrefix(query, "SHOW CREATE TABLE"):
		return &mysqlDDLTestRow{columns: []string{"table", "create"}, values: []driver.Value{"accounts", c.ddl}}, nil
	default:
		return nil, errors.New("unexpected query: " + query)
	}
}

type mysqlDDLTestRow struct {
	columns []string
	values  []driver.Value
}

func (r *mysqlDDLTestRow) Columns() []string { return r.columns }
func (*mysqlDDLTestRow) Close() error        { return nil }

func (r *mysqlDDLTestRow) Next(values []driver.Value) error {
	if r.values == nil {
		return io.EOF
	}
	copy(values, r.values)
	r.values = nil
	return nil
}
