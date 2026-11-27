package migrate

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/migrate/internal/snapshot"
	makecmd "github.com/gtkit/migrate/make"
	"github.com/gtkit/migrate/migration"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func openCommandTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	previous := app
	t.Cleanup(func() {
		app = previous
		project := defaultConfig().ProjectName
		if previous != nil {
			project = previous.ProjectName
		}
		makecmd.SetProjectName(project)
	})
	return db
}

func TestSetupOptionsAndContextDeadline(t *testing.T) {
	db := openCommandTestDB(t)
	if err := Setup(nil); err == nil {
		t.Fatal("nil connection should be rejected")
	}
	defaults := *defaultConfig()
	if defaults.MigrationsTable != "migrations" {
		t.Fatalf("unexpected default migrations table: %q", defaults.MigrationsTable)
	}
	if err := Setup(db, WithProjectName(""), WithMigrationDir(""), WithTimeout(0), WithLockName(""), WithLockTimeout(-time.Second), WithLogger(nil)); err != nil {
		t.Fatal(err)
	}
	want := defaults
	want.DB = db
	if *app.Config != want {
		t.Fatalf("empty options changed defaults: %+v", app.Config)
	}

	log := &migration.NopLogger{}
	if err := Setup(db, WithProjectName("example.com/cli"), WithMigrationDir("schema/../migrations"),
		WithTimeout(2*time.Minute), WithLockName("cli_migrations"), WithLockTimeout(2*time.Second), WithLogger(log),
		WithMigrationsTable("  cli_history  ")); err != nil {
		t.Fatal(err)
	}
	want = Config{
		ProjectName: "example.com/cli", DB: db, MigrationDir: "migrations",
		Timeout: 2 * time.Minute, LockName: "cli_migrations", LockTimeout: 2 * time.Second, Logger: log,
		MigrationsTable: "cli_history",
	}
	if *app.Config != want {
		t.Fatalf("unexpected configuration: %+v", app.Config)
	}
	before := time.Now()
	ctx, cancel := newContext()
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Before(before.Add(want.Timeout)) || deadline.After(time.Now().Add(want.Timeout)) {
		t.Fatalf("unexpected deadline: %v", deadline)
	}
}

func ExampleWithMigrationsTable() {
	cfg := new(Config)
	WithMigrationsTable("app_migrations")(cfg)
	fmt.Println(cfg.MigrationsTable)
	// Output: app_migrations
}

func TestSetupRejectsInvalidMigrationsTable(t *testing.T) {
	db := openCommandTestDB(t)
	t.Cleanup(snapshot.Reset)
	if err := Setup(db, WithMigrationsTable(strings.Repeat("a", 63)), WithDDLModels(&snapshotTestUser{})); err != nil {
		t.Fatal(err)
	}
	previous := app
	for _, name := range []string{" \t ", "history AS h"} {
		if err := Setup(db, WithMigrationsTable(name)); err == nil || !strings.Contains(err.Error(), "invalid migrations table name") {
			t.Fatalf("Setup with %q: expected an invalid table error, got %v", name, err)
		}
		if app != previous {
			t.Fatal("invalid setup replaced the active configuration")
		}
	}
	if _, err := snapshot.FromModel("snapshot_users"); err != nil {
		t.Fatalf("invalid setup changed registered DDL models: %v", err)
	}
	if db.Migrator().HasTable("migrations") {
		t.Fatal("setup unexpectedly created a migrations table")
	}
}

type SnapshotTestBase struct {
	ID        uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	CreatedAt time.Time `gorm:"column:created_at;type:datetime;index"`
}

type SnapshotTestContact struct {
	Phone string `gorm:"column:phone;type:varchar(32)"`
}

type snapshotTestUser struct {
	SnapshotTestBase
	SnapshotTestContact `gorm:"embeddedPrefix:contact_"`
	Email               string         `gorm:"column:email_address;type:varchar(120);not null;uniqueIndex:idx_snapshot_email" json:"email"`
	Name                sql.NullString `gorm:"column:name;default:'unnamed'"`
	Payload             []byte         `gorm:"column:payload;type:blob"`
	Ignored             string         `gorm:"-"`
}

func (snapshotTestUser) TableName() string { return "snapshot_users" }

type snapshotPrivateState string

type snapshotTestAccount struct {
	Status snapshotPrivateState `gorm:"type:varchar(32)"`
}

func TestMakeCreateFromRegisteredModel(t *testing.T) {
	moduleDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	db := openCommandTestDB(t)
	db.NamingStrategy = schema.NamingStrategy{TablePrefix: "prefix_", SingularTable: true}
	t.Cleanup(snapshot.Reset)
	if err := Setup(db, WithDDLModels(nil, &snapshotTestAccount{}), WithDDLModels(&snapshotTestUser{})); err != nil {
		t.Fatal(err)
	}

	flag := makecmd.CmdMakeMigration.Flags().Lookup("from-model")
	previous, changed := flag.Value.String(), flag.Changed
	t.Cleanup(func() {
		if err := flag.Value.Set(previous); err != nil {
			t.Error(err)
		}
		flag.Changed = changed
		makecmd.CmdMake.SetArgs(nil)
	})
	run := func(name string) error {
		makecmd.CmdMake.SetArgs([]string{"migration", name, "--from-model"})
		return makecmd.CmdMake.Execute()
	}
	dir := t.TempDir()
	t.Chdir(dir)
	for _, tc := range []struct{ name, want string }{
		{"create_unknown_table", `no registered DDL model has table "unknown"`},
		{"create_prefix_snapshot_test_account_table", "cannot be rendered"},
		{"rename_users_to_accounts_table", "--from-model requires create"},
	} {
		if err := run(tc.name); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected %q, got %v", tc.name, tc.want, err)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Fatalf("invalid command wrote files: %v (%v)", entries, err)
		}
	}
	if err := run("create_snapshot_users_table"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("internal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--from-model created a model or repository scaffold: %v", err)
	}
	files, err := filepath.Glob("database/migrations/*_create_snapshot_users_table.go")
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one migration file: %v (%v)", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		`"database/sql"`, `"time"`, `sql.NullString`, `[]byte`,
		`gorm:"column:id;primaryKey;autoIncrement"`,
		`gorm:"column:created_at;type:datetime;index"`,
		`gorm:"column:email_address;type:varchar(120);not null;uniqueIndex:idx_snapshot_email" json:"email"`,
		`gorm:"embedded;embeddedPrefix:contact_"`,
		`db.Table("snapshot_users").Migrator().CreateTable(&snapshot{})`,
		`db.Migrator().DropTable("snapshot_users")`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("generated migration lacks %q:\n%s", want, content)
		}
	}
	if strings.Contains(content, "Ignored") || strings.Contains(content, "SnapshotTestBase") || strings.Contains(content, "{{") {
		t.Fatalf("unexpected generated source:\n%s", content)
	}
	cmd := exec.CommandContext(t.Context(), "go", "test", filepath.Join(dir, files[0]))
	cmd.Dir = moduleDir
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile generated migration: %v\n%s", err, output)
	}

	if err := Setup(db); err != nil {
		t.Fatal(err)
	}
	if err := run("create_snapshot_users_table"); err == nil || !strings.Contains(err.Error(), "no registered DDL model") {
		t.Fatalf("Setup leaked models from its previous configuration: %v", err)
	}
}

func TestMigrationCommandsReportStateAndFailures(t *testing.T) {
	db := openCommandTestDB(t)
	dir := t.TempDir()
	const name = "2026_11_26_120000_create_cli_objects"
	registry := migration.NewRegistry()
	var upErr, downErr error
	registry.Add(name, func(tx *gorm.DB) error {
		if upErr != nil {
			return upErr
		}
		return tx.Exec("CREATE TABLE cli_objects (id INTEGER PRIMARY KEY)").Error
	}, func(tx *gorm.DB) error {
		if downErr != nil {
			return downErr
		}
		return tx.Exec("DROP TABLE cli_objects").Error
	})

	const ledger = "cli_migration_history"
	if err := Setup(db, WithMigrationDir(dir), WithLogger(&migration.NopLogger{}), WithMigrationsTable(ledger)); err != nil {
		t.Fatal(err)
	}
	app.registry = registry
	var buffer bytes.Buffer
	out, errOut := CmdMigrate.OutOrStdout(), CmdMigrate.ErrOrStderr()
	CmdMigrate.SetOut(&buffer)
	CmdMigrate.SetErr(&buffer)
	t.Cleanup(func() {
		CmdMigrate.SetArgs(nil)
		CmdMigrate.SetOut(out)
		CmdMigrate.SetErr(errOut)
	})
	run := func(command, wantOutput, wantError string) {
		t.Helper()
		buffer.Reset()
		CmdMigrate.SetArgs([]string{command})
		err := CmdMigrate.Execute()
		if wantError == "" {
			if err != nil {
				t.Fatalf("%s: %v\n%s", command, err, &buffer)
			}
		} else if err == nil || !strings.Contains(err.Error(), wantError) {
			t.Fatalf("%s: got error %v, want %q", command, err, wantError)
		}
		if !strings.Contains(buffer.String(), wantOutput) {
			t.Fatalf("%s: expected %q in %q", command, wantOutput, &buffer)
		}
	}

	run("status", "No migrations found.", "")
	if !db.Migrator().HasTable(ledger) || db.Migrator().HasTable("migrations") {
		t.Fatal("status did not create only the configured table")
	}
	run("pending", "No pending migrations.", "")
	run("up", "Database is up to date.", "")
	run("reset", "Reset completed.", "")
	run("down", "Rollback completed.", "")

	if err := os.WriteFile(filepath.Join(dir, name+".go"), []byte("package migrations\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("pending", name, "")
	run("status", "Pending", "")
	upErr = errors.New("fixture up failure")
	run("up", "Running migrations...", "fixture up failure")
	run("status", "Pending", "")
	upErr = nil
	run("up", "Migrations completed.", "")
	run("status", "Ran (batch 1)", "")

	downErr = errors.New("fixture down failure")
	run("down", "Rolling back", "fixture down failure")
	run("reset", "Resetting", "fixture down failure")
	run("refresh", "Refreshing", "fixture down failure")
	run("status", "Ran (batch 1)", "")
	if !db.Migrator().HasTable("cli_objects") {
		t.Fatal("failed rollback changed the applied migration")
	}

	downErr = nil
	run("rollback", "Rollback completed.", "")
	run("status", "Pending", "")
	run("up", "Migrations completed.", "")
	run("refresh", "Refresh completed.", "")
	if err := db.Exec("CREATE TABLE cli_scratch (id INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
	run("fresh", "Fresh migration completed.", "")
	if !db.Migrator().HasTable(ledger) || db.Migrator().HasTable("migrations") {
		t.Fatal("fresh did not rebuild the configured migration record table")
	}
	if db.Migrator().HasTable("cli_scratch") || !db.Migrator().HasTable("cli_objects") {
		t.Fatal("fresh should rebuild migration tables and remove unrelated fixture tables")
	}
	run("reset", "Reset completed.", "")
	if db.Migrator().HasTable("cli_objects") {
		t.Fatal("reset left the migration table applied")
	}

	app.MigrationDir = filepath.Join(dir, "missing")
	run("up", "", "check migration status")
	run("pending", "", "migrate pending")
	run("status", "", "migrate status")
	run("fresh", "", "migrate fresh")
	run("refresh", "", "migrate refresh")
}
