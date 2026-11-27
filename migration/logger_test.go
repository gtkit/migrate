package migration

import (
	"io"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestLoggerLevelsAndOptionalFields(t *testing.T) {
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

	log := &defaultLogger{}
	log.Info("starting")
	log.Info("applying", "file", "sample", "batch", 2)
	log.Warn("careful")
	log.Warn("locking", "waiting")
	log.Error("failed")
	log.Error("reverting", "batch", 2, "retry")

	silent := &NopLogger{}
	silent.Info("ignored info", "value", 1)
	silent.Warn("ignored warning")
	silent.Error("ignored error")

	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		got = append(got, strings.Join(strings.Fields(line), " "))
	}
	want := []string{
		"[INFO] starting", "[INFO] applying file=sample batch=2",
		"[WARN] careful", "[WARN] locking waiting",
		"[ERROR] failed", "[ERROR] reverting batch=2 retry",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected log output: %q", got)
	}
	if got := formatKV(nil); got != "" {
		t.Fatalf("empty fields formatted as %q", got)
	}
}
