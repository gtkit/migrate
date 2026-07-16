package migration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Migrator 数据迁移操作核心.
type Migrator struct {
	Folder      string
	DB          *gorm.DB
	dbType      DBType
	lock        migrationLock
	lockName    string
	lockTimeout time.Duration
	registry    *Registry
	logger      Logger
}

// MigratorOption 配置 Migrator 的选项函数.
type MigratorOption func(*Migrator)

// WithLogger 设置自定义日志实现.
func WithLogger(l Logger) MigratorOption {
	return func(m *Migrator) {
		if l != nil {
			m.logger = l
		}
	}
}

// WithLockName 设置迁移锁名称.
// 当同一数据库被多个项目共用时，不同项目应使用不同的锁名称避免互相阻塞.
func WithLockName(name string) MigratorOption {
	return func(m *Migrator) {
		if name != "" {
			m.lockName = name
		}
	}
}

// WithLockTimeout 设置获取迁移锁的最长等待时间.
// MySQL 通过 GET_LOCK 的超时参数实现；PostgreSQL 通过 statement_timeout 实现.
// 超时后返回错误而非无限阻塞，避免某个实例的长迁移导致其他实例永久挂起.
func WithLockTimeout(d time.Duration) MigratorOption {
	return func(m *Migrator) {
		if d > 0 {
			m.lockTimeout = d
		}
	}
}

// WithRegistry 设置自定义迁移注册表.
func WithRegistry(r *Registry) MigratorOption {
	return func(m *Migrator) {
		if r != nil {
			m.registry = r
		}
	}
}

const defaultLockName = "migrate_lock"

// NewMigrator 创建 Migrator 实例.
// 自动检测数据库类型，默认使用全局注册表和 stdout 日志.
func NewMigrator(folder string, db *gorm.DB, opts ...MigratorOption) *Migrator {
	dbType := DetectDBType(db)
	m := &Migrator{
		Folder:      folder,
		DB:          db,
		dbType:      dbType,
		lockName:    defaultLockName,
		lockTimeout: defaultLockTimeout,
		registry:    defaultRegistry,
		logger:      &defaultLogger{},
	}

	for _, opt := range opts {
		opt(m)
	}

	// 所有选项应用完成后再构建锁，避免 WithLockName 与 WithLockTimeout 的顺序依赖.
	m.lock = newLock(db, dbType, m.lockName, m.lockTimeout)

	return m
}

// Setup 创建 migrations 表（如不存在）.
// 并发安全：如果多个进程同时调用，重复创建会被忽略.
func (m *Migrator) Setup(ctx context.Context) error {
	migrator := m.DB.WithContext(ctx).Migrator()
	if migrator.HasTable(&Migration{}) {
		return nil
	}
	if err := migrator.CreateTable(&Migration{}); err != nil {
		// 并发场景下另一个进程可能已经创建了表，再次检查
		if migrator.HasTable(&Migration{}) {
			return nil
		}
		return fmt.Errorf("create migrations table: %w", err)
	}
	return nil
}

// Up 执行所有未迁移的文件.
func (m *Migrator) Up(ctx context.Context) error {
	if err := m.Setup(ctx); err != nil {
		return err
	}

	// 获取迁移锁
	release, err := m.lock.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer release()

	// 读取所有已注册的迁移文件（按文件系统排序）
	migrateFiles, err := m.readAllMigrationFiles()
	if err != nil {
		return fmt.Errorf("read migration files: %w", err)
	}

	// 获取当前批次
	batch, err := m.getBatch(ctx)
	if err != nil {
		return fmt.Errorf("get batch: %w", err)
	}

	// 获取所有已执行的迁移记录
	migrated, err := m.getMigratedSet(ctx)
	if err != nil {
		return fmt.Errorf("get migrated records: %w", err)
	}

	// 执行未迁移的文件
	ran := false
	for _, mfile := range migrateFiles {
		if _, ok := migrated[mfile.FileName]; ok {
			continue // 已执行过
		}

		m.logger.Info("migrating", "file", mfile.FileName, "batch", batch)
		if err := m.runUpMigration(ctx, mfile, batch); err != nil {
			m.logger.Error("migration failed", "file", mfile.FileName, "error", err)
			return fmt.Errorf("migration %s failed: %w", mfile.FileName, err)
		}
		m.logger.Info("migrated", "file", mfile.FileName)
		ran = true
	}

	if !ran {
		m.logger.Info("database is up to date")
		return nil
	}

	return nil
}

// IsUpToDate 检查数据库是否已是最新.
func (m *Migrator) IsUpToDate(ctx context.Context) (bool, error) {
	if err := m.Setup(ctx); err != nil {
		return false, err
	}

	migrateFiles, err := m.readAllMigrationFiles()
	if err != nil {
		return false, err
	}

	migrated, err := m.getMigratedSet(ctx)
	if err != nil {
		return false, err
	}

	for _, mfile := range migrateFiles {
		if _, ok := migrated[mfile.FileName]; !ok {
			return false, nil
		}
	}

	return true, nil
}

// Rollback 回滚最后一个批次的迁移.
func (m *Migrator) Rollback(ctx context.Context) error {
	if err := m.Setup(ctx); err != nil {
		return err
	}

	release, err := m.lock.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer release()

	// 获取最后一批次的迁移记录
	lastMigration := Migration{}
	if err := m.DB.WithContext(ctx).Order("id DESC").First(&lastMigration).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // 没有迁移记录
		}
		return fmt.Errorf("get last migration: %w", err)
	}

	var migrations []Migration
	if err := m.DB.WithContext(ctx).
		Where("batch = ?", lastMigration.Batch).
		Order("id DESC").
		Find(&migrations).Error; err != nil {
		return fmt.Errorf("get batch migrations: %w", err)
	}

	return m.rollbackMigrations(ctx, migrations)
}

// RollbackSteps 回滚指定步数的迁移.
func (m *Migrator) RollbackSteps(ctx context.Context, steps int) error {
	if err := m.Setup(ctx); err != nil {
		return err
	}

	release, err := m.lock.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer release()

	var migrations []Migration
	if err := m.DB.WithContext(ctx).
		Order("id DESC").
		Limit(steps).
		Find(&migrations).Error; err != nil {
		return fmt.Errorf("get migrations to rollback: %w", err)
	}

	return m.rollbackMigrations(ctx, migrations)
}

// Reset 回滚所有迁移.
func (m *Migrator) Reset(ctx context.Context) error {
	if err := m.Setup(ctx); err != nil {
		return err
	}

	release, err := m.lock.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer release()

	var migrations []Migration
	if err := m.DB.WithContext(ctx).
		Order("id DESC").
		Find(&migrations).Error; err != nil {
		return fmt.Errorf("get all migrations: %w", err)
	}

	if len(migrations) == 0 {
		return nil
	}

	return m.rollbackMigrations(ctx, migrations)
}

// Refresh 回滚所有迁移，然后重新执行.
func (m *Migrator) Refresh(ctx context.Context) error {
	if err := m.Setup(ctx); err != nil {
		return err
	}

	release, err := m.lock.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer release()

	// 回滚所有迁移
	var migrations []Migration
	if err := m.DB.WithContext(ctx).
		Order("id DESC").
		Find(&migrations).Error; err != nil {
		return fmt.Errorf("get all migrations: %w", err)
	}

	if err := m.rollbackMigrations(ctx, migrations); err != nil {
		return fmt.Errorf("reset: %w", err)
	}

	// 重新执行所有迁移
	return m.upWithoutLock(ctx)
}

// Fresh 删除所有表并重新执行所有迁移.
// ⚠️ 危险操作：会丢失所有数据.
func (m *Migrator) Fresh(ctx context.Context) error {
	release, err := m.lock.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer release()

	dbname := CurrentDatabase(m.DB)
	m.logger.Warn("dropping all tables", "database", dbname)

	if err := DeleteAllTables(m.DB); err != nil {
		return fmt.Errorf("delete all tables: %w", err)
	}
	m.logger.Info("all tables dropped", "database", dbname)

	if err := m.Setup(ctx); err != nil {
		return err
	}

	// 直接执行所有迁移（不需要再次获取锁）
	return m.upWithoutLock(ctx)
}

// Status 返回所有迁移的执行状态.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	if err := m.Setup(ctx); err != nil {
		return nil, err
	}

	migrateFiles, err := m.readAllMigrationFiles()
	if err != nil {
		return nil, err
	}

	migrated, err := m.getMigratedMap(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]MigrationStatus, 0, len(migrateFiles))
	for _, mfile := range migrateFiles {
		status := MigrationStatus{
			Name: mfile.FileName,
			Ran:  false,
		}
		if record, ok := migrated[mfile.FileName]; ok {
			status.Ran = true
			status.Batch = record.Batch
		}
		result = append(result, status)
	}

	return result, nil
}

// MigrationStatus 迁移文件状态.
type MigrationStatus struct {
	Name  string
	Ran   bool
	Batch int
}

// Pending 返回所有待执行的迁移文件列表（dry-run 模式）.
// 不执行任何迁移操作，仅展示下次 Up 会执行哪些文件.
func (m *Migrator) Pending(ctx context.Context) ([]string, error) {
	if err := m.Setup(ctx); err != nil {
		return nil, err
	}

	migrateFiles, err := m.readAllMigrationFiles()
	if err != nil {
		return nil, err
	}

	migrated, err := m.getMigratedSet(ctx)
	if err != nil {
		return nil, err
	}

	var pending []string
	for _, mfile := range migrateFiles {
		if _, ok := migrated[mfile.FileName]; !ok {
			pending = append(pending, mfile.FileName)
		}
	}

	return pending, nil
}

// --- 内部方法 ---

// upWithoutLock 执行迁移（不获取锁，供 Fresh 内部使用）.
func (m *Migrator) upWithoutLock(ctx context.Context) error {
	migrateFiles, err := m.readAllMigrationFiles()
	if err != nil {
		return fmt.Errorf("read migration files: %w", err)
	}

	batch, err := m.getBatch(ctx)
	if err != nil {
		return fmt.Errorf("get batch: %w", err)
	}

	migrated, err := m.getMigratedSet(ctx)
	if err != nil {
		return fmt.Errorf("get migrated records: %w", err)
	}

	for _, mfile := range migrateFiles {
		if _, ok := migrated[mfile.FileName]; ok {
			continue
		}
		m.logger.Info("migrating", "file", mfile.FileName, "batch", batch)
		if err := m.runUpMigration(ctx, mfile, batch); err != nil {
			m.logger.Error("migration failed", "file", mfile.FileName, "error", err)
			return fmt.Errorf("migration %s failed: %w", mfile.FileName, err)
		}
		m.logger.Info("migrated", "file", mfile.FileName)
	}

	return nil
}

// runUpMigration 执行单个迁移.
// 迁移逻辑与迁移记录写入在同一事务中提交：对支持事务型 DDL 的数据库
// （PostgreSQL、SQLite）能保证原子性，中途失败整体回滚，不会留下
// "已执行但无记录" 的中间状态；MySQL 的 DDL 会隐式提交、无法回滚，
// 此为 MySQL 固有限制，此时事务仅保护记录写入.
func (m *Migrator) runUpMigration(ctx context.Context, mfile MigrationFile, batch int) (retErr error) {
	if mfile.Up == nil {
		// 没有 Up 函数，只记录
		return m.recordMigration(ctx, mfile.FileName, batch)
	}

	// 捕获迁移函数中的 panic，转化为 error（gorm.Transaction 遇 panic 会先回滚再向上抛出）
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("migration %s panicked: %v", mfile.FileName, r)
		}
	}()

	return m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := mfile.Up(tx); err != nil {
			return fmt.Errorf("execute up: %w", err)
		}
		if err := tx.Create(&Migration{Migration: mfile.FileName, Batch: batch}).Error; err != nil {
			return fmt.Errorf("record migration %s: %w", mfile.FileName, err)
		}
		return nil
	})
}

// recordMigration 写入迁移记录.
func (m *Migrator) recordMigration(ctx context.Context, fileName string, batch int) error {
	return m.DB.WithContext(ctx).Create(&Migration{
		Migration: fileName,
		Batch:     batch,
	}).Error
}

// rollbackMigrations 按倒序执行迁移的 Down 方法.
func (m *Migrator) rollbackMigrations(ctx context.Context, migrations []Migration) error {
	if len(migrations) == 0 {
		return nil
	}

	for _, record := range migrations {
		mfile, ok := m.registry.Get(record.Migration)
		if !ok {
			return fmt.Errorf("rollback %s: migration file %q not found in registry", record.Migration, record.Migration)
		}

		m.logger.Info("rolling back", "file", record.Migration, "batch", record.Batch)

		if err := m.runDownMigration(ctx, mfile, record); err != nil {
			m.logger.Error("rollback failed", "file", record.Migration, "error", err)
			return err
		}

		m.logger.Info("rolled back", "file", record.Migration)
	}

	return nil
}

// runDownMigration 在同一事务中执行 Down 逻辑并删除迁移记录，带 panic 保护.
// 与 runUpMigration 相同：PostgreSQL、SQLite 可保证原子回滚；MySQL 的 DDL 无法回滚.
func (m *Migrator) runDownMigration(ctx context.Context, mfile MigrationFile, record Migration) (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("migration %s down panicked: %v", mfile.FileName, r)
		}
	}()

	return m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if mfile.Down != nil {
			if err := mfile.Down(tx); err != nil {
				return fmt.Errorf("execute down for %s: %w", record.Migration, err)
			}
		}
		if err := tx.Delete(&record).Error; err != nil {
			return fmt.Errorf("delete migration record %s: %w", record.Migration, err)
		}
		return nil
	})
}

// getBatch 获取下一个批次号.
func (m *Migrator) getBatch(ctx context.Context) (int, error) {
	var lastMigration Migration
	err := m.DB.WithContext(ctx).Order("id DESC").First(&lastMigration).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 1, nil
		}
		return 0, fmt.Errorf("get last batch: %w", err)
	}
	return lastMigration.Batch + 1, nil
}

// getMigratedSet 获取所有已执行的迁移文件名集合（O(1) 查询）.
func (m *Migrator) getMigratedSet(ctx context.Context) (map[string]struct{}, error) {
	var migrations []Migration
	if err := m.DB.WithContext(ctx).Find(&migrations).Error; err != nil {
		return nil, err
	}

	set := make(map[string]struct{}, len(migrations))
	for _, mg := range migrations {
		set[mg.Migration] = struct{}{}
	}
	return set, nil
}

// getMigratedMap 获取所有已执行的迁移记录映射.
func (m *Migrator) getMigratedMap(ctx context.Context) (map[string]Migration, error) {
	var migrations []Migration
	if err := m.DB.WithContext(ctx).Find(&migrations).Error; err != nil {
		return nil, err
	}

	result := make(map[string]Migration, len(migrations))
	for _, mg := range migrations {
		result[mg.Migration] = mg
	}
	return result, nil
}

// readAllMigrationFiles 从文件目录读取文件，并与注册表匹配.
// 按文件名排序（文件名以时间戳开头，确保顺序正确）.
func (m *Migrator) readAllMigrationFiles() ([]MigrationFile, error) {
	files, err := os.ReadDir(m.Folder)
	if err != nil {
		return nil, fmt.Errorf("read migration dir %s: %w", m.Folder, err)
	}

	var result []MigrationFile
	for _, f := range files {
		if f.IsDir() {
			continue
		}

		// 去除文件后缀 .go
		ext := filepath.Ext(f.Name())
		if ext != ".go" {
			continue
		}
		fileName := strings.TrimSuffix(f.Name(), ext)

		mfile, ok := m.registry.Get(fileName)
		if !ok {
			// 文件存在但未注册（可能是新创建尚未编译的迁移文件），跳过
			continue
		}

		result = append(result, mfile)
	}

	return result, nil
}
