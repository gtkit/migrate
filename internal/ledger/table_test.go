package ledger

import (
	"strings"
	"testing"
)

func TestValidateTableName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{DefaultTableName, "_history2", strings.Repeat("a", 63)} {
		if err := ValidateTableName(name); err != nil {
			t.Errorf("valid table %q: %v", name, err)
		}
	}
	for _, name := range []string{"", "history AS h", "schema.history", "9history", "账本", strings.Repeat("a", 64)} {
		if err := ValidateTableName(name); err == nil {
			t.Errorf("accepted invalid table %q", name)
		}
	}
}
