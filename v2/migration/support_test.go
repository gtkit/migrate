package migration

import (
	"errors"
	"testing"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// fakeDialector 仅覆盖 Name，用于测试 DetectDBType 的未知方言透传分支.
type fakeDialector struct{ gorm.Dialector }

func (fakeDialector) Name() string { return "oracle" }

// TestDetectDBTypeVariants 验证方言检测：nil/零值保护、MySQL/PostgreSQL 识别、未知方言透传.
func TestDetectDBTypeVariants(t *testing.T) {
	if got := DetectDBType(nil); got != DBType("") {
		t.Fatalf("nil db should yield empty DBType, got %q", got)
	}
	// 真零值：Config 为 nil，访问提升字段 Dialector 若不先判 Config 会 panic.
	if got := DetectDBType(&gorm.DB{}); got != DBType("") {
		t.Fatalf("zero-value db should yield empty DBType, got %q", got)
	}
	cases := map[DBType]gorm.Dialector{
		DBTypeMySQL:      mysql.Dialector{},
		DBTypePostgres:   postgres.Dialector{},
		DBType("oracle"): fakeDialector{},
	}
	for want, d := range cases {
		db := &gorm.DB{Config: &gorm.Config{Dialector: d}}
		if got := DetectDBType(db); got != want {
			t.Fatalf("DetectDBType(%s dialector) = %q, want %q", d.Name(), got, want)
		}
	}
}

// TestNewLockSelectsImplementation 验证按方言选择锁实现与超时回退.
func TestNewLockSelectsImplementation(t *testing.T) {
	if _, ok := newLock(nil, DBTypeMySQL, "lk", "migrations", time.Second).(*mysqlLock); !ok {
		t.Fatalf("mysql should use mysqlLock")
	}
	if _, ok := newLock(nil, DBTypePostgres, "lk", "migrations", time.Second).(*postgresLock); !ok {
		t.Fatalf("postgres should use postgresLock")
	}
	if _, ok := newLock(nil, DBTypeSQLite, "lk", "migrations", time.Second).(*noopLock); !ok {
		t.Fatalf("sqlite should use noopLock")
	}

	ml, ok := newLock(nil, DBTypeMySQL, "lk", "migrations", 0).(*mysqlLock)
	if !ok || ml.timeout != defaultLockTimeout {
		t.Fatalf("non-positive timeout should fall back to default, got %+v", ml)
	}

	if hashLockName("some_lock") < 0 {
		t.Fatalf("postgres lock key must be non-negative")
	}
}

// TestLockOptionsApplied 验证 WithLockName/WithLockTimeout 的应用与空值忽略.
func TestLockOptionsApplied(t *testing.T) {
	db := openExecTestDB(t, "lock_options")
	m := NewMigrator(t.TempDir(), db, WithLockName("custom_lock"), WithLockTimeout(3*time.Second))
	if m.lockName != "custom_lock" || m.lockTimeout != 3*time.Second {
		t.Fatalf("lock options not applied: %q %v", m.lockName, m.lockTimeout)
	}

	// 空锁名保持"派生"状态（MySQL 首次获取时按库名与账本表名派生），空超时回退默认.
	m = NewMigrator(t.TempDir(), db, WithLockName(""), WithLockTimeout(0))
	if m.lockName != "" || m.lockTimeout != defaultLockTimeout {
		t.Fatalf("empty lock options should keep defaults: %q %v", m.lockName, m.lockTimeout)
	}
}

// TestIrreversibleError 验证 Irreversible 错误的包装与默认说明.
func TestIrreversibleError(t *testing.T) {
	err := Irreversible("contact dba")
	if !errors.Is(err, ErrIrreversible) || err.Error() != "irreversible migration: contact dba" {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := Irreversible(""); !errors.Is(got, ErrIrreversible) ||
		got.Error() != "irreversible migration: manual down migration is required" {
		t.Fatalf("empty details should use default message, got: %v", got)
	}
}

// TestGlobalAddRegistersToDefaultRegistry 验证包级 Add 注册进默认注册表.
func TestGlobalAddRegistersToDefaultRegistry(t *testing.T) {
	name := "2026_07_29_000000_support_test_global_add"
	noop := func(*gorm.DB) error { return nil }
	Add(name, noop, noop)

	for _, f := range defaultRegistry.All() {
		if f.FileName == name {
			return
		}
	}
	t.Fatalf("global Add should register %s into the default registry", name)
}

// TestDefaultLoggerDoesNotPanic 验证默认 stdout 日志的三个级别可正常输出.
func TestDefaultLoggerDoesNotPanic(t *testing.T) {
	l := &defaultLogger{}
	l.Info("info message", "key", "value")
	l.Warn("warn message", "key", "value", "dangling")
	l.Error("error message")
}

// TestCurrentDatabaseNilSafe 验证导出函数对 nil/零值/未初始化输入返回空串而非 panic.
// 必须覆盖真零值 &gorm.DB{}：Dialector 是经嵌入指针 Config 提升的字段，
// Config 为 nil 时守卫若直接访问 db.Dialector 本身就会 panic.
func TestCurrentDatabaseNilSafe(t *testing.T) {
	for name, db := range map[string]*gorm.DB{
		"nil":           nil,
		"zero_value":    {},
		"nil_dialector": {Config: &gorm.Config{}},
	} {
		if got := CurrentDatabase(db); got != "" {
			t.Fatalf("CurrentDatabase(%s) should be empty, got %q", name, got)
		}
	}
}

// TestDeleteAllTablesSQLiteDropsViews 验证 SQLite 的 fresh 清理包含视图：
// 残留视图会使重放中的 CREATE VIEW 冲突.
func TestDeleteAllTablesSQLiteDropsViews(t *testing.T) {
	db := openExecTestDB(t, "sqlite_views")
	if err := db.Exec("CREATE TABLE base_t (id integer primary key)").Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := db.Exec("CREATE VIEW v_base AS SELECT id FROM base_t").Error; err != nil {
		t.Fatalf("create view: %v", err)
	}

	if err := DeleteAllTables(db); err != nil {
		t.Fatalf("delete all tables with a view present: %v", err)
	}

	var n int64
	if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type IN ('table','view') AND name != 'sqlite_sequence'").
		Scan(&n).Error; err != nil {
		t.Fatalf("count objects: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected empty schema after DeleteAllTables, got %d objects", n)
	}
}
