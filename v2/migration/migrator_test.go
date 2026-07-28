package migration

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type execTestUser struct {
	ID   int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Name string `gorm:"column:name;size:64"`
}

func (execTestUser) TableName() string { return "exec_test_users" }

// openExecTestDB 打开一个共享缓存的 SQLite 内存库并限制为单连接，
// 保证事务与后续查询看到同一个库.
func openExecTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestMigratorUpAndRollback(t *testing.T) {
	db := openExecTestDB(t, "migrator_up_rollback")
	dir := t.TempDir()

	file := "2026_03_24_120000_create_exec_test_users_table"
	writeMigrationFile(t, dir, file, "package migrations\n")

	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		return tx.Migrator().CreateTable(&execTestUser{})
	}, func(tx *gorm.DB) error {
		return tx.Migrator().DropTable(&execTestUser{})
	})

	m := NewMigrator(dir, db, WithRegistry(registry), WithLockTimeout(2*time.Second))

	if err := m.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table to exist after up")
	}

	var count int64
	if err := db.Model(&Migration{}).Where("migration = ?", file).Count(&count).Error; err != nil {
		t.Fatalf("count records: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 migration record, got %d", count)
	}

	if err := m.Rollback(t.Context()); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table to be dropped after rollback")
	}
	if err := db.Model(&Migration{}).Where("migration = ?", file).Count(&count).Error; err != nil {
		t.Fatalf("count records after rollback: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 migration records after rollback, got %d", count)
	}
}

// TestMigratorUpRollsBackOnFailure 验证 Up 中途失败时整个迁移在事务内回滚：
// 已创建的表被撤销，且不写入迁移记录（SQLite 支持事务型 DDL）.
func TestMigratorUpRollsBackOnFailure(t *testing.T) {
	db := openExecTestDB(t, "migrator_up_failure")
	dir := t.TempDir()

	file := "2026_03_24_120000_create_exec_test_users_table"
	writeMigrationFile(t, dir, file, "package migrations\n")

	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		// 先建表，再返回错误——事务应把建表一并回滚.
		if err := tx.Migrator().CreateTable(&execTestUser{}); err != nil {
			return err
		}
		return errors.New("boom")
	}, nil)

	m := NewMigrator(dir, db, WithRegistry(registry))

	err := m.Up(t.Context())
	if err == nil {
		t.Fatalf("expected up to fail")
	}

	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table creation to be rolled back on failure")
	}

	var count int64
	if err := db.Model(&Migration{}).Where("migration = ?", file).Count(&count).Error; err != nil {
		t.Fatalf("count records: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no migration record after failed up, got %d", count)
	}
}

// TestMigratorUpRollsBackOnPanic 验证迁移函数 panic 时被转化为 error 且整体回滚.
func TestMigratorUpRollsBackOnPanic(t *testing.T) {
	db := openExecTestDB(t, "migrator_up_panic")
	dir := t.TempDir()

	file := "2026_03_24_120000_create_exec_test_users_table"
	writeMigrationFile(t, dir, file, "package migrations\n")

	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		if err := tx.Migrator().CreateTable(&execTestUser{}); err != nil {
			return err
		}
		panic("boom")
	}, nil)

	m := NewMigrator(dir, db, WithRegistry(registry))

	err := m.Up(t.Context())
	if err == nil {
		t.Fatalf("expected up to fail on panic")
	}

	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table creation to be rolled back on panic")
	}
}

// TestMigratorMarkApplied 验证 mark-applied 只写入迁移记录、不执行 Up.
func TestMigratorMarkApplied(t *testing.T) {
	db := openExecTestDB(t, "migrator_mark_applied")
	dir := t.TempDir()

	f1 := "2026_03_24_120000_create_a_table"
	f2 := "2026_03_24_120001_create_b_table"
	writeMigrationFile(t, dir, f1, "package migrations\n")
	writeMigrationFile(t, dir, f2, "package migrations\n")

	registry := NewRegistry()
	// Up 若被执行会建表；mark-applied 不应执行它们.
	registry.Add(f1, func(tx *gorm.DB) error { return tx.Migrator().CreateTable(&execTestUser{}) }, nil)
	registry.Add(f2, func(tx *gorm.DB) error { return tx.Migrator().CreateTable(&execTestUser{}) }, nil)

	m := NewMigrator(dir, db, WithRegistry(registry))

	marked, err := m.MarkApplied(t.Context(), "")
	if err != nil {
		t.Fatalf("mark applied: %v", err)
	}
	if len(marked) != 2 {
		t.Fatalf("expected 2 marked, got %d: %v", len(marked), marked)
	}
	if db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("mark-applied must not run Up (table should not exist)")
	}

	upToDate, err := m.IsUpToDate(t.Context())
	if err != nil {
		t.Fatalf("is up to date: %v", err)
	}
	if !upToDate {
		t.Fatalf("expected up-to-date after mark-applied")
	}

	again, err := m.MarkApplied(t.Context(), "")
	if err != nil {
		t.Fatalf("mark applied again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected nothing to mark on second run, got %v", again)
	}
}

// TestMigratorMarkAppliedTo 验证 --to 只标记不超过指定版本的 pending 迁移.
func TestMigratorMarkAppliedTo(t *testing.T) {
	db := openExecTestDB(t, "migrator_mark_applied_to")
	dir := t.TempDir()

	f1 := "2026_03_24_120000_create_a_table"
	f2 := "2026_03_24_120001_create_b_table"
	f3 := "2026_03_24_120002_create_c_table"
	noop := func(*gorm.DB) error { return nil }
	registry := NewRegistry()
	for _, f := range []string{f1, f2, f3} {
		writeMigrationFile(t, dir, f, "package migrations\n")
		registry.Add(f, noop, noop)
	}

	m := NewMigrator(dir, db, WithRegistry(registry))

	marked, err := m.MarkApplied(t.Context(), f2)
	if err != nil {
		t.Fatalf("mark applied to: %v", err)
	}
	if len(marked) != 2 || marked[0] != f1 || marked[1] != f2 {
		t.Fatalf("expected [%s %s], got %v", f1, f2, marked)
	}

	pending, err := m.Pending(t.Context())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0] != f3 {
		t.Fatalf("expected [%s] pending, got %v", f3, pending)
	}
}

// TestMigratorMarkAppliedToUnregisteredTarget 验证 --to 传入未注册版本时报错且不写任何记录.
func TestMigratorMarkAppliedToUnregisteredTarget(t *testing.T) {
	db := openExecTestDB(t, "migrator_mark_applied_bad_target")
	dir := t.TempDir()

	f1 := "2026_03_24_120000_create_a_table"
	noop := func(*gorm.DB) error { return nil }
	registry := NewRegistry()
	writeMigrationFile(t, dir, f1, "package migrations\n")
	registry.Add(f1, noop, noop)

	m := NewMigrator(dir, db, WithRegistry(registry))

	if _, err := m.MarkApplied(t.Context(), "2026_03_24_999999_typo_table"); err == nil {
		t.Fatalf("MarkApplied should reject an unregistered target")
	}

	pending, err := m.Pending(t.Context())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0] != f1 {
		t.Fatalf("no migration should be marked after rejected target, pending = %v", pending)
	}
}

// TestMigratorRollbackTo 验证 down-to 只回滚版本高于目标（不含目标）的迁移.
func TestMigratorRollbackTo(t *testing.T) {
	db := openExecTestDB(t, "migrator_rollback_to")
	dir := t.TempDir()

	f1 := "2026_03_24_120000_create_a_table"
	f2 := "2026_03_24_120001_create_b_table"
	f3 := "2026_03_24_120002_create_c_table"
	tables := map[string]string{f1: "rt_a", f2: "rt_b", f3: "rt_c"}

	registry := NewRegistry()
	for _, f := range []string{f1, f2, f3} {
		tbl := tables[f]
		writeMigrationFile(t, dir, f, "package migrations\n")
		registry.Add(f,
			func(tx *gorm.DB) error { return tx.Exec("CREATE TABLE " + tbl + " (id integer primary key)").Error },
			func(tx *gorm.DB) error { return tx.Exec("DROP TABLE " + tbl).Error },
		)
	}

	m := NewMigrator(dir, db, WithRegistry(registry))
	if err := m.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}

	// 回滚到 f1（不含）：应回滚 f2、f3.
	if err := m.RollbackTo(t.Context(), f1); err != nil {
		t.Fatalf("rollback-to: %v", err)
	}

	if !db.Migrator().HasTable("rt_a") {
		t.Fatalf("rt_a should remain (f1 not rolled back)")
	}
	if db.Migrator().HasTable("rt_b") || db.Migrator().HasTable("rt_c") {
		t.Fatalf("rt_b/rt_c should be dropped after rollback-to f1")
	}

	pending, err := m.Pending(t.Context())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending after rollback-to, got %d: %v", len(pending), pending)
	}
}

// TestMigrationLoggerAndHelpers 覆盖默认日志、NopLogger、Irreversible 与 hashLockName.
func TestMigrationLoggerAndHelpers(t *testing.T) {
	dl := &defaultLogger{}
	dl.Info("info")
	dl.Info("info", "k", "v")
	dl.Warn("warn")
	dl.Warn("warn", "k", "v")
	dl.Error("err")
	dl.Error("err", "k", "v", "odd")

	nop := &NopLogger{}
	nop.Info("i", "k", "v")
	nop.Warn("w")
	nop.Error("e")

	if Irreversible("manual down required") == nil {
		t.Fatalf("Irreversible should return a non-nil error")
	}
	hx := hashLockName("x")
	if hx != hashLockName("x") {
		t.Fatalf("hashLockName should be deterministic")
	}
	if hx == hashLockName("y") {
		t.Fatalf("hashLockName should differ for different names")
	}
}

// TestMigratorUpFailsClosedOnNilUp 验证 Up==nil 时 fail-closed：报错且不写记录.
func TestMigratorUpFailsClosedOnNilUp(t *testing.T) {
	db := openExecTestDB(t, "nil_up")
	registry := NewRegistry()
	registry.Add("2026_03_24_120000_noop_table", nil, nil)
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry))

	if err := m.Up(t.Context()); err == nil {
		t.Fatalf("Up should fail-closed on a nil Up function")
	}
	var n int64
	if err := db.Model(&Migration{}).Where("migration = ?", "2026_03_24_120000_noop_table").Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("nil-Up migration must not be recorded, got %d", n)
	}
}

// TestMigratorFreshRefreshFailClosedOnEmptyRegistry 验证空 registry 下 Fresh/Refresh
// 报错且不删/不回滚任何数据.
func TestMigratorFreshRefreshFailClosedOnEmptyRegistry(t *testing.T) {
	db := openExecTestDB(t, "fresh_empty_registry")
	if err := db.Exec("CREATE TABLE sentinel (id integer primary key)").Error; err != nil {
		t.Fatalf("create sentinel: %v", err)
	}
	m := NewMigrator(t.TempDir(), db, WithRegistry(NewRegistry()))

	if err := m.Fresh(t.Context()); err == nil {
		t.Fatalf("Fresh should fail-closed on empty registry")
	}
	if err := m.Refresh(t.Context()); err == nil {
		t.Fatalf("Refresh should fail-closed on empty registry")
	}
	if !db.Migrator().HasTable("sentinel") {
		t.Fatalf("Fresh/Refresh must not touch data on empty registry")
	}
}

// TestMigratorDiagnosticsFailClosedOnEmptyRegistry 验证空 registry 下 Pending/Status/MarkApplied 报错.
func TestMigratorDiagnosticsFailClosedOnEmptyRegistry(t *testing.T) {
	db := openExecTestDB(t, "diag_empty_registry")
	m := NewMigrator(t.TempDir(), db, WithRegistry(NewRegistry()))

	if _, err := m.Pending(t.Context()); err == nil {
		t.Fatalf("Pending should fail-closed on empty registry")
	}
	if _, err := m.Status(t.Context()); err == nil {
		t.Fatalf("Status should fail-closed on empty registry")
	}
	if _, err := m.MarkApplied(t.Context(), ""); err == nil {
		t.Fatalf("MarkApplied should fail-closed on empty registry")
	}
}

// TestMigratorRollbackFailsClosedOnNilDown 验证 Down==nil 时回滚报错、记录保留、结构不变.
func TestMigratorRollbackFailsClosedOnNilDown(t *testing.T) {
	db := openExecTestDB(t, "nil_down")
	registry := NewRegistry()
	registry.Add("2026_03_24_120000_create_x_table",
		func(tx *gorm.DB) error { return tx.Exec("CREATE TABLE x (id integer primary key)").Error },
		nil)
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry))
	if err := m.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}

	if err := m.Rollback(t.Context()); err == nil {
		t.Fatalf("Rollback should fail-closed on a nil Down function")
	}
	if !db.Migrator().HasTable("x") {
		t.Fatalf("table must not be dropped when Down is nil")
	}
	var n int64
	db.Model(&Migration{}).Count(&n)
	if n != 1 {
		t.Fatalf("migration record must be kept when Down is nil, got %d", n)
	}
}

// TestMigratorRollbackAbortsOnUnregisteredRecord 验证回滚批中含未注册记录时整体拒绝、无一回滚.
func TestMigratorRollbackAbortsOnUnregisteredRecord(t *testing.T) {
	db := openExecTestDB(t, "rollback_unregistered")
	m, _ := setupThreeApplied(t, db)
	if err := db.Create(&Migration{Migration: "2026_03_24_119999_ghost", Batch: 1}).Error; err != nil {
		t.Fatalf("seed ghost record: %v", err)
	}

	if err := m.Reset(t.Context()); err == nil {
		t.Fatalf("Reset should abort when a record is not in registry")
	}
	var n int64
	db.Model(&Migration{}).Count(&n)
	if n != 4 {
		t.Fatalf("no migration should be rolled back on abort, got %d records", n)
	}
	if !db.Migrator().HasTable("rb_a") {
		t.Fatalf("tables must remain when rollback aborts upfront")
	}
}

// TestMigratorFreshFailsClosedOnNilUp 验证 registry 含 nil Up 时 Fresh 在删表前失败、不动数据.
func TestMigratorFreshFailsClosedOnNilUp(t *testing.T) {
	db := openExecTestDB(t, "fresh_nil_up")
	if err := db.Exec("CREATE TABLE sentinel (id integer primary key)").Error; err != nil {
		t.Fatalf("create sentinel: %v", err)
	}
	registry := NewRegistry()
	registry.Add("2026_03_24_120000_bad", nil, func(*gorm.DB) error { return nil }) // nil Up
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry))

	if err := m.Fresh(t.Context()); err == nil {
		t.Fatalf("Fresh should fail-closed when a migration has nil Up")
	}
	if !db.Migrator().HasTable("sentinel") {
		t.Fatalf("Fresh must not drop tables when validation fails")
	}
}

// TestMigratorUpFailsClosedOnDuplicateRegistration 验证重复注册名时 Up 报错.
func TestMigratorUpFailsClosedOnDuplicateRegistration(t *testing.T) {
	db := openExecTestDB(t, "dup_reg")
	up := func(tx *gorm.DB) error { return tx.Exec("CREATE TABLE d (id integer primary key)").Error }
	down := func(*gorm.DB) error { return nil }
	registry := NewRegistry()
	registry.Add("2026_03_24_120000_dup", up, down)
	registry.Add("2026_03_24_120000_dup", up, down) // 同名重复
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry))

	if err := m.Up(t.Context()); err == nil {
		t.Fatalf("Up should fail-closed on duplicate registration")
	}
}

// TestMigratorDiagnosticsFailClosedOnDrift 验证存在未注册 ghost 迁移时
// Pending/Status/MarkApplied 报错（不掩盖漂移）.
func TestMigratorDiagnosticsFailClosedOnDrift(t *testing.T) {
	db := openExecTestDB(t, "diag_drift")
	registry := NewRegistry()
	registry.Add("2026_03_24_120000_a", func(*gorm.DB) error { return nil }, func(*gorm.DB) error { return nil })
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry))
	if err := m.Setup(t.Context()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := db.Create(&Migration{Migration: "2026_03_24_119999_ghost", Batch: 1}).Error; err != nil {
		t.Fatalf("seed ghost record: %v", err)
	}

	if _, err := m.Pending(t.Context()); err == nil {
		t.Fatalf("Pending should fail-closed on drift")
	}
	if _, err := m.Status(t.Context()); err == nil {
		t.Fatalf("Status should fail-closed on drift")
	}
	if _, err := m.MarkApplied(t.Context(), ""); err == nil {
		t.Fatalf("MarkApplied should fail-closed on drift")
	}
}

// TestNewMigratorNilDBReturnsError 验证 nil db 不 panic，方法返回可处理错误.
func TestNewMigratorNilDBReturnsError(t *testing.T) {
	m := NewMigrator(t.TempDir(), nil, WithRegistry(NewRegistry()))
	if m == nil {
		t.Fatalf("NewMigrator must not return nil")
	}
	if err := m.Setup(t.Context()); err == nil {
		t.Fatalf("Setup should return an error for a nil db")
	}
}

// setupThreeApplied 建三条已应用迁移（各建一张表），返回 Migrator 与版本名.
func setupThreeApplied(t *testing.T, db *gorm.DB) (*Migrator, [3]string) {
	t.Helper()
	f1 := "2026_03_24_120000_create_a_table"
	f2 := "2026_03_24_120001_create_b_table"
	f3 := "2026_03_24_120002_create_c_table"
	tables := map[string]string{f1: "rb_a", f2: "rb_b", f3: "rb_c"}
	registry := NewRegistry()
	for _, f := range []string{f1, f2, f3} {
		tbl := tables[f]
		registry.Add(f,
			func(tx *gorm.DB) error { return tx.Exec("CREATE TABLE " + tbl + " (id integer primary key)").Error },
			func(tx *gorm.DB) error { return tx.Exec("DROP TABLE " + tbl).Error },
		)
	}
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry))
	if err := m.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	return m, [3]string{f1, f2, f3}
}

func countMigrations(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&Migration{}).Count(&n).Error; err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	return n
}

// TestMigratorRollbackStepsRejectsNonPositive 验证 steps<=0 报错且不回滚任何迁移
// （防止负数经 GORM Limit(-1) 取消 LIMIT 回滚全部）.
func TestMigratorRollbackStepsRejectsNonPositive(t *testing.T) {
	db := openExecTestDB(t, "rollback_steps_guard")
	m, _ := setupThreeApplied(t, db)

	for _, steps := range []int{0, -1} {
		if err := m.RollbackSteps(t.Context(), steps); err == nil {
			t.Fatalf("RollbackSteps(%d) should return an error", steps)
		}
		if n := countMigrations(t, db); n != 3 {
			t.Fatalf("RollbackSteps(%d) must not roll back; expected 3 records, got %d", steps, n)
		}
	}

	if err := m.RollbackSteps(t.Context(), 2); err != nil {
		t.Fatalf("RollbackSteps(2): %v", err)
	}
	if n := countMigrations(t, db); n != 1 {
		t.Fatalf("RollbackSteps(2) should leave 1 record, got %d", n)
	}
}

// TestMigratorRollbackToRejectsUnknownTarget 验证无效目标报错且不回滚.
func TestMigratorRollbackToRejectsUnknownTarget(t *testing.T) {
	db := openExecTestDB(t, "rollback_to_guard")
	m, versions := setupThreeApplied(t, db)

	if err := m.RollbackTo(t.Context(), "0"); err == nil {
		t.Fatalf("RollbackTo(\"0\") should return an error for an unknown target")
	}
	if n := countMigrations(t, db); n != 3 {
		t.Fatalf("RollbackTo with unknown target must not roll back; expected 3, got %d", n)
	}

	// 有效目标（f1）：只回滚 f2、f3.
	if err := m.RollbackTo(t.Context(), versions[0]); err != nil {
		t.Fatalf("RollbackTo(%s): %v", versions[0], err)
	}
	if n := countMigrations(t, db); n != 1 {
		t.Fatalf("RollbackTo(f1) should leave 1 record, got %d", n)
	}
}

// TestMigratorUpUsesRegistryWithoutDiskFiles 验证 Up 以 registry 为执行源：
// 迁移目录为空（模拟部署后不带 .go 源目录）时仍执行 registry 中的迁移，
// 而非旧行为「空目录 → database is up to date」的静默漏执行.
func TestMigratorUpUsesRegistryWithoutDiskFiles(t *testing.T) {
	db := openExecTestDB(t, "migrator_up_no_disk")
	emptyDir := t.TempDir() // 有意不写任何迁移文件

	file := "2026_03_24_120000_create_exec_test_users_table"
	registry := NewRegistry()
	registry.Add(file, func(tx *gorm.DB) error {
		return tx.Migrator().CreateTable(&execTestUser{})
	}, func(tx *gorm.DB) error {
		return tx.Migrator().DropTable(&execTestUser{})
	})

	m := NewMigrator(emptyDir, db, WithRegistry(registry))

	if err := m.Up(t.Context()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !db.Migrator().HasTable(&execTestUser{}) {
		t.Fatalf("expected table to exist: Up must run registry migrations even with an empty dir")
	}

	var count int64
	if err := db.Model(&Migration{}).Where("migration = ?", file).Count(&count).Error; err != nil {
		t.Fatalf("count records: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 migration record, got %d", count)
	}
}

// TestMigratorFailsClosedOnEmptyRegistry 验证空 registry 时 Up 与 IsUpToDate
// fail-closed 报错，而非静默报「已最新」.
func TestMigratorFailsClosedOnEmptyRegistry(t *testing.T) {
	db := openExecTestDB(t, "migrator_empty_registry")
	m := NewMigrator(t.TempDir(), db, WithRegistry(NewRegistry()))

	if err := m.Up(t.Context()); err == nil {
		t.Fatalf("expected Up to fail on empty registry")
	}

	upToDate, err := m.IsUpToDate(t.Context())
	if err == nil {
		t.Fatalf("expected IsUpToDate to fail on empty registry")
	}
	if upToDate {
		t.Fatalf("IsUpToDate must not report up-to-date on empty registry")
	}
}

// TestMigratorFailsClosedOnDrift 验证存在「已应用但当前 binary 未注册」的迁移时
// Up fail-closed，避免在结构漂移状态下继续执行.
func TestMigratorFailsClosedOnDrift(t *testing.T) {
	db := openExecTestDB(t, "migrator_drift")

	registry := NewRegistry()
	registry.Add("2026_03_24_120000_create_a", func(tx *gorm.DB) error { return nil }, nil)
	m := NewMigrator(t.TempDir(), db, WithRegistry(registry))

	if err := m.Setup(t.Context()); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// 注入一条当前 registry 未注册的已应用记录.
	if err := db.Create(&Migration{Migration: "2026_03_24_119999_ghost", Batch: 1}).Error; err != nil {
		t.Fatalf("seed record: %v", err)
	}

	if err := m.Up(t.Context()); err == nil {
		t.Fatalf("expected Up to fail on applied-but-unregistered migration")
	}
}
