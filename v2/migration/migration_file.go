package migration

import (
	"fmt"
	"sync"

	"gorm.io/gorm"
)

// MigrateFunc 定义迁移回调方法的类型，接收 *gorm.DB 并返回 error.
type MigrateFunc func(*gorm.DB) error

// MigrationFile 代表单个迁移文件.
type MigrationFile struct {
	FileName string
	Up       MigrateFunc
	Down     MigrateFunc
}

// Registry 管理所有已注册的迁移文件，线程安全.
type Registry struct {
	mu    sync.RWMutex
	files map[string]MigrationFile
	order []string // 保持注册顺序
}

// defaultRegistry 默认的全局注册表.
var defaultRegistry = NewRegistry()

// NewRegistry 创建新的迁移注册表.
func NewRegistry() *Registry {
	return &Registry{
		files: make(map[string]MigrationFile),
	}
}

// Add 注册一个迁移文件到默认注册表.
// 所有迁移文件都需要在 init() 中调用此方法注册.
func Add(name string, up, down MigrateFunc) {
	defaultRegistry.Add(name, up, down)
}

// Add 注册一个迁移文件到注册表.
func (r *Registry) Add(name string, up, down MigrateFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.files[name]; exists {
		// 重复注册直接忽略（程序重启时 init 会再次调用）
		return
	}

	r.files[name] = MigrationFile{
		FileName: name,
		Up:       up,
		Down:     down,
	}
	r.order = append(r.order, name)
}

// Get 通过名称获取迁移文件.
func (r *Registry) Get(name string) (MigrationFile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	mf, ok := r.files[name]
	return mf, ok
}

// All 按注册顺序返回所有迁移文件.
func (r *Registry) All() []MigrationFile {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]MigrationFile, 0, len(r.order))
	for _, name := range r.order {
		if mf, ok := r.files[name]; ok {
			result = append(result, mf)
		}
	}
	return result
}

// Len 返回已注册的迁移文件数量.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.files)
}

// getMigrationFile 从默认注册表获取迁移文件（内部使用）.
func getMigrationFile(name string) (MigrationFile, error) {
	mf, ok := defaultRegistry.Get(name)
	if !ok {
		return MigrationFile{}, fmt.Errorf("migration file %q not found in registry", name)
	}
	return mf, nil
}
