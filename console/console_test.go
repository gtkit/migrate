package console

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestConsoleReportsSeverityAndIgnoresNilError(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := read.Close(); err != nil {
			t.Error(err)
		}
	})
	original := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = original }()

	Success("migration applied")
	Warning("manual rollback required")
	Error("migration failed")
	ExitIf(nil)

	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	want := []string{"migration applied", "manual rollback required", "migration failed"}
	if len(lines) != len(want) {
		t.Fatalf("unexpected console output: %q", output)
	}
	for i, message := range want {
		if !strings.Contains(lines[i], message) {
			t.Errorf("line %d = %q, want %q", i, lines[i], message)
		}
	}
}
