package make

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"gorm.io/gorm"
)

// Config 控制 make 系列命令的生成目录和 DDL 上下文。
type Config struct {
	ProjectName   string
	ModelDir      string
	RepositoryDir string
	MigrationDir  string
	DDLDir        string
	DB            *gorm.DB
	DDLModels     []any
}

var (
	configMu      sync.RWMutex
	currentConfig = defaultConfig()
)

func defaultConfig() Config {
	return Config{
		ModelDir:      "internal/models",
		RepositoryDir: "internal/repository",
		MigrationDir:  "database/migrations",
		DDLDir:        "database/ddl",
	}
}

// SetConfig 设置 make 命令配置。
func SetConfig(cfg Config) {
	configMu.Lock()
	defer configMu.Unlock()
	currentConfig = normalizeConfig(cfg)
}

// SetProjectName 单独设置项目名称。
//
// Deprecated: 仅为兼容保留；请使用 migrate.Setup 配合 WithProjectName，
// 或直接调用 SetConfig。
func SetProjectName(name string) {
	cfg := CurrentConfig()
	if name != "" {
		cfg.ProjectName = name
	}
	SetConfig(cfg)
}

// CurrentConfig 返回当前配置副本。
func CurrentConfig() Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return cloneConfig(currentConfig)
}

func cloneConfig(cfg Config) Config {
	cfg.DDLModels = slices.Clone(cfg.DDLModels)
	return cfg
}

func normalizeConfig(cfg Config) Config {
	def := defaultConfig()

	cfg.ModelDir = cleanDir(cfg.ModelDir, def.ModelDir)
	cfg.RepositoryDir = cleanDir(cfg.RepositoryDir, def.RepositoryDir)
	cfg.MigrationDir = cleanDir(cfg.MigrationDir, def.MigrationDir)
	cfg.DDLDir = cleanDir(cfg.DDLDir, def.DDLDir)
	cfg.DDLModels = slices.Clone(cfg.DDLModels)

	return cfg
}

func cleanDir(dir, def string) string {
	if strings.TrimSpace(dir) == "" {
		dir = def
	}
	return filepath.Clean(dir)
}

func resolveConfig(cmd *cobra.Command) Config {
	cfg := CurrentConfig()

	if dir := readStringFlag(cmd, "model-dir"); dir != "" {
		cfg.ModelDir = filepath.Clean(dir)
	}
	if dir := readStringFlag(cmd, "repository-dir"); dir != "" {
		cfg.RepositoryDir = filepath.Clean(dir)
	}
	if dir := readStringFlag(cmd, "migration-dir"); dir != "" {
		cfg.MigrationDir = filepath.Clean(dir)
	}
	if dir := readStringFlag(cmd, "ddl-dir"); dir != "" {
		cfg.DDLDir = filepath.Clean(dir)
	}

	return normalizeConfig(cfg)
}

// readStringFlag 读取命令自身或父命令持久化的字符串 flag（cobra 的 Flag 会沿父链查找）.
func readStringFlag(cmd *cobra.Command, name string) string {
	if cmd == nil {
		return ""
	}
	if flag := cmd.Flag(name); flag != nil {
		return strings.TrimSpace(flag.Value.String())
	}
	return ""
}

// resolveProjectName 返回代码生成用的 module path：显式配置优先，
// 否则从执行目录向上查找 go.mod 解析；两者都没有时 fail-closed 报错，不落盘错误 import.
func resolveProjectName(cfg Config) (string, error) {
	if cfg.ProjectName != "" {
		return cfg.ProjectName, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve project name: %w", err)
	}
	for {
		if name, ok := readModulePath(filepath.Join(dir, "go.mod")); ok {
			return name, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("project name is required: pass WithProjectName or run inside a Go module (no go.mod found)")
		}
		dir = parent
	}
}

// readModulePath 读取 go.mod 的 module 指令；文件不存在或无 module 行返回 false.
func readModulePath(goModPath string) (string, bool) {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), true
		}
	}
	return "", false
}

func moduleImportPath(projectName, dir string) string {
	cleaned := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(dir)), "./")
	return path.Join(projectName, cleaned)
}

func modelPackageName(dir string) string {
	return path.Base(filepath.ToSlash(filepath.Clean(dir)))
}

func modelFilePath(cfg Config, model Model) string {
	return filepath.Join(cfg.ModelDir, model.PackageName+".go")
}

func modelBaseFilePath(cfg Config) string {
	return filepath.Join(cfg.ModelDir, "model.go")
}

func modelDocFilePath(cfg Config) string {
	return filepath.Join(cfg.ModelDir, "doc.go")
}

func repositoryDirPath(cfg Config, model Model) string {
	return filepath.Join(cfg.RepositoryDir, model.PackageName)
}

func repositoryFilePath(cfg Config, model Model) string {
	return filepath.Join(repositoryDirPath(cfg, model), "repository.go")
}

func repositoryUtilFilePath(cfg Config, model Model) string {
	return filepath.Join(repositoryDirPath(cfg, model), "repository_util.go")
}

func migrationDocFilePath(cfg Config) string {
	return filepath.Join(cfg.MigrationDir, "doc.go")
}

func migrationFilePath(cfg Config, fileName string) string {
	return filepath.Join(cfg.MigrationDir, fileName+".go")
}

func ddlFilePath(cfg Config, tableName string) string {
	return filepath.Join(cfg.DDLDir, fmt.Sprintf("create_%s_table.sql", tableName))
}
