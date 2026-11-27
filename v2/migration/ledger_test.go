package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

func TestMySQLMissingLedgerUsesDriverErrorCode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"missing table", &mysql.MySQLError{Number: 1146, Message: "table is absent"}, true},
		{"permission denied", &mysql.MySQLError{Number: 1142, Message: "select denied"}, false},
		{"ordinary error with misleading text", errors.New("1146 table is absent"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("select migration ledger: %w", tc.err)
			if got := missingLedger(DBTypeMySQL, wrapped); got != tc.want {
				t.Fatalf("missingLedger = %t, expected %t", got, tc.want)
			}
		})
	}
}

func TestMySQLIndexDefinitionCheckRejectsBadInputAndCanceledReads(t *testing.T) {
	t.Parallel()
	if _, err := MySQLIndexMatches(nil, "accounts", "idx_email", false, "email"); err == nil || !strings.Contains(err.Error(), "MySQL connection") {
		t.Fatalf("expected incompatible database error, got %v", err)
	}
	db := openOfflineMySQLDialect(t)
	if _, err := MySQLIndexMatches(db, "accounts", "idx_email", false); err == nil || !strings.Contains(err.Error(), "at least one column") {
		t.Fatalf("expected missing column definition error, got %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if matches, err := MySQLIndexMatches(db.WithContext(ctx), "accounts", "idx_email", false, "email"); matches ||
		!errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "idx_email on accounts") {
		t.Fatalf("canceled index check must preserve context and identity: match=%t err=%v", matches, err)
	}
}
