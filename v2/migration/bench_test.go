package migration

import (
	"fmt"
	"testing"

	"gorm.io/gorm"
)

func BenchmarkSQLDDLChecks(b *testing.B) {
	sql := "ALTER TABLE `users` ADD COLUMN `note` VARCHAR(64) NOT NULL DEFAULT 'algorithm=x' /* LOCK=NONE */, ALGORITHM=INPLACE, LOCK=NONE -- tail comment"
	b.ReportAllocs()
	for b.Loop() {
		sqlDDLChecks(sql)
	}
}

func BenchmarkRegistryAddAndAll(b *testing.B) {
	names := make([]string, 50)
	for i := range names {
		names[i] = fmt.Sprintf("2026_03_24_%06d_create_t%d_table", i, i)
	}
	noop := func(*gorm.DB) error { return nil }
	b.ReportAllocs()
	for b.Loop() {
		r := NewRegistry()
		for _, name := range names {
			r.Add(name, noop, noop)
		}
		_ = r.All()
	}
}

func BenchmarkFormatKV(b *testing.B) {
	kv := []any{"file", "2026_03_24_120000_create_users_table", "batch", 3, "elapsed", "12ms"}
	b.ReportAllocs()
	for b.Loop() {
		_ = formatKV(kv)
	}
}
