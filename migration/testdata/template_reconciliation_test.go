package migrations

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gtkit/migrate/migration"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestGeneratedObjectReconciliation(t *testing.T) {
	db, err := gorm.Open(mysql.Open(os.Getenv("MIGRATE_TEST_MYSQL_DSN")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := t.Context()
	exec := func(statement string) {
		t.Helper()
		if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	newMigrator := func(table string) *migration.Migrator {
		return migration.NewMigrator(os.Getenv("MIGRATE_RECONCILE_DIR"), db, migration.WithMigrationsTable(table))
	}
	upAndStatus := func(m *migration.Migrator) {
		t.Helper()
		if err := m.Up(ctx); err != nil {
			t.Fatal(err)
		}
		status, err := m.Status(ctx)
		if err != nil || len(status) != 4 {
			t.Fatalf("expected four migrations, got %+v: %v", status, err)
		}
		for _, record := range status {
			if !record.Ran || record.Batch != 1 {
				t.Fatalf("expected an applied migration in batch 1, got %+v", record)
			}
		}
	}
	exec("CREATE TABLE reconcile_columns (id INT PRIMARY KEY, tail INT)")
	exec("CREATE TABLE reconcile_indexes (id INT PRIMARY KEY, email VARCHAR(64))")
	upAndStatus(newMigrator("reconcile_created_history"))
	exec("INSERT INTO reconcile_created (id) VALUES (8)")
	exec("INSERT INTO reconcile_columns (id, label) VALUES (8, 'retain')")
	exec("INSERT INTO reconcile_indexes (id, email) VALUES (8, 'retain@example.com')")

	adopted := newMigrator("reconcile_adopted_history")
	upAndStatus(adopted)
	var id int
	var label, email string
	if err := db.Raw("SELECT id FROM reconcile_created WHERE id = 8").Row().Scan(&id); err != nil || id != 8 {
		t.Fatalf("existing table row changed: %d, %v", id, err)
	}
	if err := db.Raw("SELECT label FROM reconcile_columns WHERE id = 8").Row().Scan(&label); err != nil || label != "retain" {
		t.Fatalf("existing column row changed: %q, %v", label, err)
	}
	if err := db.Raw("SELECT email FROM reconcile_indexes WHERE id = 8").Row().Scan(&email); err != nil || email != "retain@example.com" {
		t.Fatalf("existing index table row changed: %q, %v", email, err)
	}

	for i, tc := range []struct {
		object, change, restore, wantError string
	}{
		{
			"create_reconcile_created_table",
			"ALTER TABLE reconcile_created ADD COLUMN unexpected INT",
			"ALTER TABLE reconcile_created DROP COLUMN unexpected",
			"MySQL table",
		},
		{
			"add_label_to_reconcile_columns_table",
			"ALTER TABLE reconcile_columns ALTER COLUMN label SET DEFAULT 'different'",
			"ALTER TABLE reconcile_columns ALTER COLUMN label SET DEFAULT 'guest'",
			"MySQL column",
		},
		{
			"add_label_to_reconcile_columns_table",
			"ALTER TABLE reconcile_columns MODIFY COLUMN label VARCHAR(32) NOT NULL DEFAULT 'guest' COMMENT 'label comment' AFTER tail",
			"ALTER TABLE reconcile_columns MODIFY COLUMN label VARCHAR(32) NOT NULL DEFAULT 'guest' COMMENT 'label comment' AFTER id",
			"not immediately after",
		},
		{
			"add_index_email_to_reconcile_indexes_table",
			"ALTER TABLE reconcile_indexes ALTER INDEX idx_reconcile_indexes_email INVISIBLE",
			"ALTER TABLE reconcile_indexes ALTER INDEX idx_reconcile_indexes_email VISIBLE",
			"MySQL index",
		},
	} {
		t.Run(fmt.Sprintf("%d/%s", i, tc.object), func(t *testing.T) {
			exec(tc.change)
			defer exec(tc.restore)
			m := newMigrator(fmt.Sprintf("reconcile_conflict_%d", i))
			if err := m.Up(ctx); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected a definition conflict with %q, got %v", tc.wantError, err)
			}
			status, err := m.Status(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range status {
				if strings.HasSuffix(record.Name, tc.object) {
					if record.Ran {
						t.Fatalf("conflicting migration was recorded: %+v", record)
					}
					return
				}
			}
			t.Fatalf("missing migration status for %s", tc.object)
		})
	}

	if err := adopted.Rollback(ctx); err != nil {
		t.Fatalf("rollback of adopted objects: %v", err)
	}
	migrator := db.Migrator()
	if migrator.HasTable("reconcile_created") || migrator.HasTable("reconcile_snapshot") || migrator.HasColumn("reconcile_columns", "label") ||
		migrator.HasIndex("reconcile_indexes", "idx_reconcile_indexes_email") {
		t.Fatal("Down did not remove all adopted objects")
	}
}
