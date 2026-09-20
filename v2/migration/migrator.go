package migration

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Migrator 数据迁移操作核心.
//
// Folder 与 DB 为兼容保留的导出字段，是构造期快照、仅供读取；
// 构造后写入不影响迁移执行——迁移、记录读写与加锁始终以 NewMigrator
// 传入的值为唯一真实来源，避免"旧库持锁、新库跑迁移"的分裂.
type Migrator struct {
	Folder      string
	DB          *gorm.DB
	folder      string   // 迁移目录唯一真实来源
	db          *gorm.DB // 数据库唯一真实来源，与 dbType、lock 构造期绑定
	dbType      DBType
	lock        migrationLock
	lockName    string
	lockTimeout time.Duration
	tableName   string
	allowFresh  bool // Fresh 删全库表，必须经 WithAllowFresh 显式授权
	// 容忍"已应用但当前 binary 未注册"的记录（应用回滚窗口），必须经 WithAllowUnknownApplied 显式授权
	allowUnknownApplied bool
	configErr           error // 构造期配置错误（如非法表名），所有执行入口 fail-closed 返回
	registry            *Registry
	logger              Logger
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

// WithLockName 设置迁移锁名称，覆盖默认派生规则.
// 默认：MySQL 按 "migrate:<数据库名>:<账本表名>" 派生（超 64 字符截断并追加哈希），
// 同一实例上不同库、不同账本表天然互不阻塞；PostgreSQL 默认 "migrate_lock"（advisory lock 按库隔离）.
// 多项目共库时按 DDL 资源边界选择：项目间无共享表/外键时用独立锁名避免互相阻塞；
// 存在共享 DDL 资源时相关项目应配相同锁名，用同一把锁串行化迁移.
// MySQL 上显式锁名不得超过 64 字符（GET_LOCK 硬限制），超长时所有执行入口 fail-closed 报错.
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

// WithMigrationsTable 设置迁移记录表名（默认 "migrations"）.
// 当同一数据库被多个项目共用时，各项目应使用独立的记录表（并配合 WithLockName
// 使用独立锁名），迁移账本与一致性校验互不干扰.
// 表名仅允许字母、数字与下划线且长度不超过 63（见 ValidateMigrationsTable），
// 不支持 schema 限定名；首尾空白自动规整.空白字符串同样非法：不静默保持默认
// （多项目共库下配置意外为空时写默认账本比报错危险得多），与其他非法表名一样
// 使所有迁移入口 fail-closed 报错.
func WithMigrationsTable(name string) MigratorOption {
	return func(m *Migrator) {
		m.tableName = strings.TrimSpace(name)
	}
}

// WithAllowFresh 显式授权 Fresh 执行.
// Fresh 会删除库内全部用户表（含其他项目的业务表与迁移账本），默认禁用；
// 仅当本项目独占该数据库时才应授权.数据库所有权无法从配置推断，必须由调用方声明.
func WithAllowFresh() MigratorOption {
	return func(m *Migrator) {
		m.allowFresh = true
	}
}

// WithAllowUnknownApplied 显式授权 Up/IsUpToDate/Status/Pending 容忍
// "已应用但当前 binary 未注册"的迁移记录：逐条记 Warn 后继续，只处理已注册且未应用的迁移.
// 用于应用回滚窗口——旧版本 binary 面对新版本已写入的账本时不再启动失败.
// 默认关闭（fail-closed）；MarkApplied 与所有回滚入口不受影响，始终严格.
func WithAllowUnknownApplied() MigratorOption {
	return func(m *Migrator) {
		m.allowUnknownApplied = true
	}
}

// migrationsTablePattern 迁移记录表名白名单：普通标识符，不支持 schema.table.
var migrationsTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// maxMigrationsTableLen 迁移记录表名长度上限.
// MySQL 表名上限 64 字符；PostgreSQL 标识符默认上限 63 字节且超长会被静默截断
// ——截断在共库下可能撞名、写错账本，比报错更危险.取跨方言交集 63；
// 白名单仅放行 ASCII，字节数即字符数.
const maxMigrationsTableLen = 63

// ValidateMigrationsTable 校验迁移记录表名是否合法.
// 仅允许字母、数字与下划线且不以数字开头，长度不超过 63；
// 不支持 schema 限定名（如 "public.migrations"）.
// 表名会被拼入 SQL，非法字符（空格、反引号、分号等）会被 GORM 当作 SQL 表达式处理，必须拒绝.
func ValidateMigrationsTable(name string) error {
	if !migrationsTablePattern.MatchString(name) {
		return fmt.Errorf(
			"invalid migrations table name %q: only letters, digits and underscores are allowed (schema-qualified names are not supported)",
			name,
		)
	}
	if len(name) > maxMigrationsTableLen {
		return fmt.Errorf(
			"invalid migrations table name %q: %d chars exceeds the cross-dialect limit of %d (MySQL allows 64, PostgreSQL silently truncates identifiers to 63 bytes)",
			name, len(name), maxMigrationsTableLen,
		)
	}
	return nil
}

const (
	defaultLockName  = "migrate_lock"
	defaultTableName = "migrations"
)

// NewMigrator 创建 Migrator 实例.
// 自动检测数据库类型，默认使用全局注册表和 stdout 日志.
func NewMigrator(folder string, db *gorm.DB, opts ...MigratorOption) *Migrator {
	dbType := DetectDBType(db)
	m := &Migrator{
		Folder:      folder,
		DB:          db,
		folder:      folder,
		db:          db,
		dbType:      dbType,
		lockTimeout: defaultLockTimeout,
		tableName:   defaultTableName,
		registry:    defaultRegistry,
		logger:      &defaultLogger{},
	}

	for _, opt := range opts {
		opt(m)
	}

	// 选项无法返回错误：非法表名记入 configErr，由 Setup/Fresh 等入口 fail-closed 返回，
	// 绝不静默回退默认表名（多项目共库下写错账本比报错危险得多）.
	m.configErr = ValidateMigrationsTable(m.tableName)
	if m.configErr == nil && dbType == DBTypeMySQL && len(m.lockName) > mysqlLockNameMax {
		m.configErr = fmt.Errorf("invalid lock name %q: %d chars exceeds the MySQL GET_LOCK limit of %d", m.lockName, len(m.lockName), mysqlLockNameMax)
	}

	// 所有选项应用完成后再构建锁，避免 WithLockName 与 WithLockTimeout 的顺序依赖.
	m.lock = newLock(db, dbType, m.lockName, m.tableName, m.lockTimeout)

	return m
}

// Setup 创建迁移记录表（如不存在），表名由 WithMigrationsTable 配置.
// 并发安全：如果多个进程同时调用，重复创建会被忽略.
func (m *Migrator) Setup(ctx context.Context) error {
	if m.configErr != nil {
		return m.configErr
	}
	if m.db == nil {
		return errDBRequired
	}
	db := m.db.WithContext(ctx)
	if db.Migrator().HasTable(m.tableName) {
		return nil
	}
	if err := db.Table(m.tableName).Migrator().CreateTable(&Migration{}); err != nil {
		// 并发场景下另一个进程可能已经创建了表，再次检查
		if db.Migrator().HasTable(m.tableName) {
			return nil
		}
		return fmt.Errorf("create migrations table %s: %w", m.tableName, err)
	}
	return nil
}

// records 返回绑定迁移记录表与上下文的查询入口.
// 所有迁移记录的读写统一经此走配置表名，避免散落的默认表名查询.
func (m *Migrator) records(ctx context.Context) *gorm.DB {
	return m.db.WithContext(ctx).Table(m.tableName)
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

	return m.upWithoutLock(ctx)
}

// IsUpToDate 检查数据库是否已是最新.
func (m *Migrator) IsUpToDate(ctx context.Context) (bool, error) {
	migrated, err := m.readMigratedMap(ctx)
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

	// 获取最后一批次的迁移记录.
	// 用 Limit(1).Find 而非 First：空账本是正常状态（如新库首次操作），
	// First 会触发 gorm.ErrRecordNotFound，被 GORM 默认 logger 打成错误日志污染监控.
	var lastMigrations []Migration
	if err := m.records(ctx).Order("id DESC").Limit(1).Find(&lastMigrations).Error; err != nil {
		return fmt.Errorf("get last migration: %w", err)
	}
	if len(lastMigrations) == 0 {
		return nil // 没有迁移记录
	}
	lastMigration := lastMigrations[0]

	var migrations []Migration
	if err := m.records(ctx).
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
	if err := m.records(ctx).
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
	if err := m.records(ctx).
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
	if err := m.records(ctx).
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
// ⚠️ 危险操作：会丢失所有数据.默认禁用：必须经 WithAllowFresh 显式授权，
// 且仅当本项目独占该数据库时才应授权（fresh 会删除库内全部用户表，
// 包括其他项目的业务表与迁移账本）.
// 清理范围含表与视图；PostgreSQL 仅清理 public schema，其他 schema 的对象不受影响.
func (m *Migrator) Fresh(ctx context.Context) error {
	// Fresh 不经 Setup 且先删表，配置错误必须在此独立拦截.
	if m.configErr != nil {
		return m.configErr
	}
	// 数据库所有权无法从表名等配置推断（共库项目可能用默认表名，独占库
	// 也可能用自定义表名），删全库表必须由调用方显式授权.
	if !m.allowFresh {
		return errors.New(
			"fresh is disabled by default: it drops ALL tables in the database; enable it with WithAllowFresh only on a database this project owns exclusively",
		)
	}
	// 删表前先做完整执行校验：空 registry / 重复名 / nil Up 都在删表之前拦下，绝不删光数据却不重建.
	if err := m.validateRegistryForExecution(); err != nil {
		return err
	}

	release, err := m.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer release()

	// 携带 ctx：删表 DDL 与库名查询同样受外部取消/超时约束.
	dbname := CurrentDatabase(m.db.WithContext(ctx))
	m.logger.Warn("dropping all tables", "database", dbname)

	if err := DeleteAllTables(m.db.WithContext(ctx)); err != nil {
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
// 只读：不获取锁、不创建账本表，账本表不存在时全部为未执行.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	migrated, err := m.readMigratedMap(ctx)
	if err != nil {
		return nil, err
	}

	if err := m.checkRegistryConsistency(migrated); err != nil {
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
			status.AppliedAt = record.CreatedAt
		}
		result = append(result, status)
	}

	return result, nil
}

// MigrationStatus 迁移文件状态.
type MigrationStatus struct {
	Name      string
	Ran       bool
	Batch     int
	AppliedAt time.Time // 账本记录写入时间，未执行时为零值
}

// Pending 返回所有待执行的迁移文件列表（dry-run 模式）.
// 只读：不执行任何迁移、不获取锁、不创建账本表，仅展示下次 Up 会执行哪些文件.
func (m *Migrator) Pending(ctx context.Context) ([]string, error) {
	migrated, err := m.readMigratedMap(ctx)
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
	// toFile 必须是已注册迁移名：拼写错误的目标会静默标记错误范围（比目标晚的全跳过、
	// 早的全标），对 baseline 这类破坏性操作必须 fail-closed.
	if toFile != "" {
		if _, ok := m.registry.Get(toFile); !ok {
			return nil, fmt.Errorf("mark-applied target %q is not a registered migration", toFile)
		}
	}

	if err := m.Setup(ctx); err != nil {
		return nil, err
	}

	release, err := m.acquireLock(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	migrated, err := m.getMigratedMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("get migrated records: %w", err)
	}
	// 完整一致性校验（空/重复/nil Up + 漂移）：不在漂移状态下写新 baseline.
	// baseline 是破坏性写入，WithAllowUnknownApplied 对此不生效，未知记录始终拒绝.
	if err := m.checkRegistryConsistency(migrated); err != nil {
		return nil, err
	}
	if unknown := m.unknownApplied(migrated); len(unknown) > 0 {
		return nil, fmt.Errorf("%w: mark-applied aborted, applied migrations not registered in the current binary: %s", ErrRegistryDrift, strings.Join(unknown, ", "))
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
	if err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, name := range toMark {
			if err := tx.Table(m.tableName).Create(&Migration{Migration: name, Batch: batch}).Error; err != nil {
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
		if err := m.records(ctx).
			Where("migration = ?", targetFile).Count(&count).Error; err != nil {
			return fmt.Errorf("check target migration: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("target migration %q is not an applied migration", targetFile)
		}
	}

	// 按迁移文件名（版本）倒序回滚，保证后应用的先回滚.
	query := m.records(ctx).Order("migration DESC")
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
	if m.db == nil {
		return errDBRequired
	}
	if m.dbType == DBTypeSQLite {
		return nil
	}
	sqlDB, err := m.db.DB()
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

// upWithoutLock 执行所有未迁移的文件（不获取锁，调用方需已持锁）.
// registry 为迁移集合的唯一真实来源，按文件名升序执行.
func (m *Migrator) upWithoutLock(ctx context.Context) error {
	// 获取所有已执行的迁移记录
	migrated, err := m.getMigratedMap(ctx)
	if err != nil {
		return fmt.Errorf("get migrated records: %w", err)
	}

	// fail-closed：空 registry、重复注册、缺 Up 或已应用迁移在当前 binary 缺失时报错
	if err := m.checkRegistryConsistency(migrated); err != nil {
		return err
	}

	batch, err := m.getBatch(ctx)
	if err != nil {
		return fmt.Errorf("get batch: %w", err)
	}

	ran := false
	for _, mfile := range m.registeredFiles() {
		if _, ok := migrated[mfile.FileName]; ok {
			continue // 已执行过
		}

		m.logger.Info("migrating", "file", mfile.FileName, "batch", batch)
		start := time.Now()
		if err := m.runUpMigration(ctx, mfile, batch); err != nil {
			m.logger.Error("migration failed", "file", mfile.FileName, "error", err)
			return fmt.Errorf("migration %s failed: %w", mfile.FileName, err)
		}
		m.logger.Info("migrated", "file", mfile.FileName, "duration", time.Since(start).Round(time.Millisecond))
		ran = true
	}

	if !ran {
		m.logger.Info("database is up to date")
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

	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := mfile.Up(tx); err != nil {
			return fmt.Errorf("execute up: %w", err)
		}
		if err := tx.Table(m.tableName).Create(&Migration{Migration: mfile.FileName, Batch: batch}).Error; err != nil {
			return fmt.Errorf("record migration %s: %w", mfile.FileName, err)
		}
		return nil
	})
}

// rollbackMigrations 按倒序执行迁移的 Down 方法.
func (m *Migrator) rollbackMigrations(ctx context.Context, migrations []Migration) error {
	// 重复注册名意味着回滚会静默使用先注册者的 Down、另一份同名实现被吞掉，
	// 与 Up 的执行校验对称，fail-closed 拒绝（所有回滚入口共同经过此处）.
	if dupes := m.registry.Duplicates(); len(dupes) > 0 {
		return fmt.Errorf("%w: rollback aborted, migration names registered more than once: %s", ErrDuplicateRegistration, strings.Join(dupes, ", "))
	}

	if len(migrations) == 0 {
		return nil
	}

	// 前置全量预检：任一待回滚记录在 registry 缺失或无 Down 则整体拒绝，
	// 避免"回滚了一部分才失败"的部分回滚.
	for _, record := range migrations {
		mfile, ok := m.registry.Get(record.Migration)
		if !ok {
			return fmt.Errorf("%w: rollback aborted, migration %q not found in registry", ErrRegistryDrift, record.Migration)
		}
		if mfile.Down == nil {
			return fmt.Errorf("rollback aborted: migration %q has no down function", record.Migration)
		}
	}

	for _, record := range migrations {
		mfile, _ := m.registry.Get(record.Migration)

		m.logger.Info("rolling back", "file", record.Migration, "batch", record.Batch)
		start := time.Now()
		if err := m.runDownMigration(ctx, mfile, record); err != nil {
			m.logger.Error("rollback failed", "file", record.Migration, "error", err)
			return err
		}
		m.logger.Info("rolled back", "file", record.Migration, "duration", time.Since(start).Round(time.Millisecond))
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

	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// fail-closed：无 Down 函数不能删记录，否则表还在、记录没了=账本损坏.
		// "不可回滚"应显式用 Irreversible（非 nil、返回 error）表达.
		if mfile.Down == nil {
			return fmt.Errorf("migration %s has no down function; cannot roll back", record.Migration)
		}
		if err := mfile.Down(tx); err != nil {
			return fmt.Errorf("execute down for %s: %w", record.Migration, err)
		}
		if err := tx.Table(m.tableName).Delete(&Migration{}, record.ID).Error; err != nil {
			return fmt.Errorf("delete migration record %s: %w", record.Migration, err)
		}
		return nil
	})
}

// getBatch 获取下一个批次号.
func (m *Migrator) getBatch(ctx context.Context) (int, error) {
	// 同 Rollback：空账本正常，避免 First 触发 ErrRecordNotFound 误报日志.
	var lastMigrations []Migration
	err := m.records(ctx).Order("id DESC").Limit(1).Find(&lastMigrations).Error
	if err != nil {
		return 0, fmt.Errorf("get last batch: %w", err)
	}
	if len(lastMigrations) == 0 {
		return 1, nil
	}
	lastMigration := lastMigrations[0]
	return lastMigration.Batch + 1, nil
}

// getMigratedMap 获取所有已执行的迁移记录映射.
func (m *Migrator) getMigratedMap(ctx context.Context) (map[string]Migration, error) {
	var migrations []Migration
	if err := m.records(ctx).Find(&migrations).Error; err != nil {
		return nil, err
	}

	result := make(map[string]Migration, len(migrations))
	for _, mg := range migrations {
		result[mg.Migration] = mg
	}
	return result, nil
}

// readMigratedMap 只读地获取已执行记录：不创建账本表，表不存在时返回空映射.
// 供 Status/Pending/IsUpToDate 等诊断入口使用，使它们能在只读账号或新库上运行.
func (m *Migrator) readMigratedMap(ctx context.Context) (map[string]Migration, error) {
	if m.configErr != nil {
		return nil, m.configErr
	}
	if m.db == nil {
		return nil, errDBRequired
	}
	if !m.db.WithContext(ctx).Migrator().HasTable(m.tableName) {
		return map[string]Migration{}, nil
	}
	return m.getMigratedMap(ctx)
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
// 经 WithAllowUnknownApplied 授权时，未注册的已应用记录改为逐条 Warn 后放行.
func (m *Migrator) checkRegistryConsistency(migrated map[string]Migration) error {
	if err := m.validateRegistryForExecution(); err != nil {
		return err
	}
	unknown := m.unknownApplied(migrated)
	if len(unknown) == 0 {
		return nil
	}
	if !m.allowUnknownApplied {
		return fmt.Errorf("%w: applied migration %q is not registered in the current binary; schema may have drifted", ErrRegistryDrift, unknown[0])
	}
	for _, name := range unknown {
		m.logger.Warn("applied migration not registered in current binary; tolerated by WithAllowUnknownApplied", "file", name)
	}
	return nil
}

// unknownApplied 返回账本中当前 binary 未注册的迁移名，升序.
func (m *Migrator) unknownApplied(migrated map[string]Migration) []string {
	var unknown []string
	for name := range migrated {
		if _, ok := m.registry.Get(name); !ok {
			unknown = append(unknown, name)
		}
	}
	slices.Sort(unknown)
	return unknown
}

// validateRegistryForExecution 校验 registry 适合执行，供执行/破坏性命令在动手前调用：
// registry 为空（通常漏 import 迁移包）、存在重复注册名（两个包注册同名）、
// 任一迁移缺 Up 函数、或迁移名不符合时间戳命名格式，均返回错误、fail-closed.
func (m *Migrator) validateRegistryForExecution() error {
	if m.registry.Len() == 0 {
		return fmt.Errorf("%w; did you forget to import the migrations package?", ErrNoMigrations)
	}
	if dups := m.registry.Duplicates(); len(dups) > 0 {
		return fmt.Errorf("%w: %s", ErrDuplicateRegistration, strings.Join(dups, ", "))
	}
	for _, mfile := range m.registry.All() {
		if mfile.Up == nil {
			return fmt.Errorf("migration %s has no up function", mfile.FileName)
		}
		// 迁移执行顺序依赖文件名的时间戳前缀，乱名会破坏顺序且绕过磁盘/lint 体系，
		// 与 lint 用同一白名单在运行时 fail-closed（不能指望调用方记得跑 lint）.
		// 回滚路径故意不查：历史误入账本的坏名仍可通过 down/reset 清理.
		if !migrationNamePattern.MatchString(mfile.FileName) {
			return fmt.Errorf(
				"invalid migration name %q: must match YYYY_MM_DD_HHMMSS_<description>",
				mfile.FileName,
			)
		}
	}
	return nil
}
