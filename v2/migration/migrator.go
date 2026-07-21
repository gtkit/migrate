package migration

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	if m.DB == nil {
		return errors.New("migrate: database connection is required")
	}
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
	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer release()

	// registry 为迁移集合的唯一真实来源，按文件名升序执行
	migrateFiles := m.registeredFiles()

	// 获取所有已执行的迁移记录
	migrated, err := m.getMigratedSet(ctx)
	if err != nil {
		return fmt.Errorf("get migrated records: %w", err)
	}

	// fail-closed：空 registry 或已应用迁移在当前 binary 缺失时报错
	if err := m.checkRegistryConsistency(migrated); err != nil {
		return err
	}

	// 获取当前批次
	batch, err := m.getBatch(ctx)
	if err != nil {
		return fmt.Errorf("get batch: %w", err)
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

	migrated, err := m.getMigratedSet(ctx)
	if err != nil {
		return false, err
	}

	// fail-closed：与 Up 一致，避免 runUp 用 IsUpToDate 短路时掩盖空 registry
	if err := m.checkRegistryConsistency(migrated); err != nil {
		return false, err
	}

	for _, mfile := range m.registeredFiles() {
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

	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
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
// steps 必须为正数：<=0 直接返回错误，避免负数经 GORM Limit(-1) 取消行数限制而回滚全部.
func (m *Migrator) RollbackSteps(ctx context.Context, steps int) error {
	if steps <= 0 {
		return fmt.Errorf("rollback steps must be greater than 0, got %d", steps)
	}
	if err := m.Setup(ctx); err != nil {
		return err
	}

	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
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

	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
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
	// 回滚前先做完整执行校验：空 registry / 重复名 / nil Up 都在回滚之前拦下.
	if err := m.validateRegistryForExecution(); err != nil {
		return err
	}
	if err := m.Setup(ctx); err != nil {
		return err
	}

	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
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
	// 删表前先做完整执行校验：空 registry / 重复名 / nil Up 都在删表之前拦下，绝不删光数据却不重建.
	if err := m.validateRegistryForExecution(); err != nil {
		return err
	}

	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
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

	migrated, err := m.getMigratedMap(ctx)
	if err != nil {
		return nil, err
	}

	set := make(map[string]struct{}, len(migrated))
	for name := range migrated {
		set[name] = struct{}{}
	}
	if err := m.checkRegistryConsistency(set); err != nil {
		return nil, err
	}

	migrateFiles := m.registeredFiles()

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

	migrated, err := m.getMigratedSet(ctx)
	if err != nil {
		return nil, err
	}
	if err := m.checkRegistryConsistency(migrated); err != nil {
		return nil, err
	}

	migrateFiles := m.registeredFiles()

	var pending []string
	for _, mfile := range migrateFiles {
		if _, ok := migrated[mfile.FileName]; !ok {
			pending = append(pending, mfile.FileName)
		}
	}

	return pending, nil
}

// MarkApplied 把 pending 迁移写入迁移记录而不执行其 Up，用于把「已存在等价 schema」
// 的存量库纳入迁移管理（baseline）.
// toFile 为空时标记所有 pending；否则只标记文件名 <= toFile 的 pending 迁移.
// ⚠️ 它不校验数据库真实结构是否与这些迁移等价——标错会让后续 up 跳过真实建表、造成漂移.
// 返回实际被标记的迁移文件名（升序）.
func (m *Migrator) MarkApplied(ctx context.Context, toFile string) ([]string, error) {
	if err := m.Setup(ctx); err != nil {
		return nil, err
	}

	release, err := m.acquireLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	migrated, err := m.getMigratedSet(ctx)
	if err != nil {
		return nil, fmt.Errorf("get migrated records: %w", err)
	}
	// 完整一致性校验（空/重复/nil Up + 漂移）：不在漂移状态下写新 baseline.
	if err := m.checkRegistryConsistency(migrated); err != nil {
		return nil, err
	}

	migrateFiles := m.registeredFiles()

	batch, err := m.getBatch(ctx)
	if err != nil {
		return nil, fmt.Errorf("get batch: %w", err)
	}

	// registeredFiles 按文件名（时间戳）升序返回，toMark 天然有序.
	var toMark []string
	for _, mfile := range migrateFiles {
		if _, ok := migrated[mfile.FileName]; ok {
			continue
		}
		if toFile != "" && mfile.FileName > toFile {
			continue
		}
		toMark = append(toMark, mfile.FileName)
	}

	if len(toMark) == 0 {
		return nil, nil
	}

	// 同一事务写入所有 baseline 记录，全成或全败，不留部分标记.
	if err := m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, name := range toMark {
			if err := tx.Create(&Migration{Migration: name, Batch: batch}).Error; err != nil {
				return fmt.Errorf("mark %s applied: %w", name, err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return toMark, nil
}

// RollbackTo 回滚所有版本高于 targetFile 的已应用迁移（不含 targetFile 本身）.
// targetFile 为空时等价于 Reset（回滚全部）.
func (m *Migrator) RollbackTo(ctx context.Context, targetFile string) error {
	if err := m.Setup(ctx); err != nil {
		return err
	}

	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer release()

	// targetFile 非空时必须是真实已应用版本，否则（如传 "0" 或早于首个版本的字符串）
	// 会命中"全部已应用"而误回滚全部.
	if targetFile != "" {
		var count int64
		if err := m.DB.WithContext(ctx).Model(&Migration{}).
			Where("migration = ?", targetFile).Count(&count).Error; err != nil {
			return fmt.Errorf("check target migration: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("target migration %q is not an applied migration", targetFile)
		}
	}

	// 按迁移文件名（版本）倒序回滚，保证后应用的先回滚.
	query := m.DB.WithContext(ctx).Order("migration DESC")
	if targetFile != "" {
		query = query.Where("migration > ?", targetFile)
	}

	var migrations []Migration
	if err := query.Find(&migrations).Error; err != nil {
		return fmt.Errorf("get migrations to rollback: %w", err)
	}

	return m.rollbackMigrations(ctx, migrations)
}

// --- 内部方法 ---

// acquireLock 校验连接池容量后获取迁移锁.
// 任何持锁方法都应经此获取：锁会占用一条连接，后续操作还需另一条，
// MaxOpenConns=1（非 SQLite）时会阻塞到超时，故先快速拒绝.
func (m *Migrator) acquireLock(ctx context.Context) (func(), error) {
	if err := m.ensureConcurrentConns(); err != nil {
		return nil, err
	}
	release, err := m.lock.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	return release, nil
}

// ensureConcurrentConns 确保连接池允许并发连接.
// 持锁命令需要一条连接持有迁移锁、另一条执行实际操作；
// MySQL、PostgreSQL 的锁绑定在专属连接上，连接池上限为 1 时会一直死等到超时，此处提前快速拒绝.
// SQLite 使用 noopLock（不占用连接），无此约束.
func (m *Migrator) ensureConcurrentConns() error {
	if m.DB == nil {
		return errors.New("migrate: database connection is required")
	}
	if m.dbType == DBTypeSQLite {
		return nil
	}
	sqlDB, err := m.DB.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB: %w", err)
	}
	if sqlDB.Stats().MaxOpenConnections == 1 {
		return fmt.Errorf(
			"this command needs at least 2 connections (one holds the migration lock while the other runs the migration); increase SetMaxOpenConns",
		)
	}
	return nil
}

// upWithoutLock 执行迁移（不获取锁，供 Fresh 内部使用）.
func (m *Migrator) upWithoutLock(ctx context.Context) error {
	if err := m.validateRegistryForExecution(); err != nil {
		return err
	}
	migrateFiles := m.registeredFiles()

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
		// fail-closed：无 Up 函数却记为已执行会让账本与真实 schema 不一致，直接报错.
		return fmt.Errorf("migration %s has no up function", mfile.FileName)
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

// rollbackMigrations 按倒序执行迁移的 Down 方法.
func (m *Migrator) rollbackMigrations(ctx context.Context, migrations []Migration) error {
	if len(migrations) == 0 {
		return nil
	}

	// 前置全量预检：任一待回滚记录在 registry 缺失或无 Down 则整体拒绝，
	// 避免"回滚了一部分才失败"的部分回滚.
	for _, record := range migrations {
		mfile, ok := m.registry.Get(record.Migration)
		if !ok {
			return fmt.Errorf("rollback aborted: migration %q not found in registry", record.Migration)
		}
		if mfile.Down == nil {
			return fmt.Errorf("rollback aborted: migration %q has no down function", record.Migration)
		}
	}

	for _, record := range migrations {
		mfile, _ := m.registry.Get(record.Migration)

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
		// fail-closed：无 Down 函数不能删记录，否则表还在、记录没了=账本损坏.
		// "不可回滚"应显式用 Irreversible（非 nil、返回 error）表达.
		if mfile.Down == nil {
			return fmt.Errorf("migration %s has no down function; cannot roll back", record.Migration)
		}
		if err := mfile.Down(tx); err != nil {
			return fmt.Errorf("execute down for %s: %w", record.Migration, err)
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

// registeredFiles 返回编译期 registry 中的全部迁移，按文件名（时间戳前缀）升序.
// 迁移执行以 registry 为唯一真实来源，不依赖运行时的 .go 源目录，
// 避免部署环境缺少源目录时静默漏执行.
func (m *Migrator) registeredFiles() []MigrationFile {
	files := m.registry.All()
	slices.SortFunc(files, func(a, b MigrationFile) int {
		return strings.Compare(a.FileName, b.FileName)
	})
	return files
}

// checkRegistryConsistency 在执行前校验 registry 与已应用记录的一致性，fail-closed.
// registry 为空（通常是漏 import 迁移包），或存在"已应用但当前 binary 未注册"的
// 迁移（结构可能已漂移）时返回错误，禁止在这两种状态下继续执行.
func (m *Migrator) checkRegistryConsistency(migrated map[string]struct{}) error {
	if err := m.validateRegistryForExecution(); err != nil {
		return err
	}
	for name := range migrated {
		if _, ok := m.registry.Get(name); !ok {
			return fmt.Errorf("applied migration %q is not registered in the current binary; schema may have drifted", name)
		}
	}
	return nil
}

// validateRegistryForExecution 校验 registry 适合执行，供执行/破坏性命令在动手前调用：
// registry 为空（通常漏 import 迁移包）、存在重复注册名（两个包注册同名）、
// 或任一迁移缺 Up 函数，均返回错误、fail-closed.
func (m *Migrator) validateRegistryForExecution() error {
	if m.registry.Len() == 0 {
		return errors.New("no migrations registered; did you forget to import the migrations package?")
	}
	if dups := m.registry.Duplicates(); len(dups) > 0 {
		return fmt.Errorf("duplicate migration registrations: %s", strings.Join(dups, ", "))
	}
	for _, mfile := range m.registry.All() {
		if mfile.Up == nil {
			return fmt.Errorf("migration %s has no up function", mfile.FileName)
		}
	}
	return nil
}
