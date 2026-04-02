package make

import (
	"fmt"
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
		ProjectName:   "project_name",
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

// SetProjectName 兼容旧调用方式。
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

	if cfg.ProjectName == "" {
		cfg.ProjectName = def.ProjectName
	}
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

func readStringFlag(cmd *cobra.Command, name string) string {
	if cmd == nil {
		return ""
	}

	if flag := cmd.Flag(name); flag != nil {
		if value := strings.TrimSpace(flag.Value.String()); value != "" {
			return value
		}
	}

	inherited := cmd.InheritedFlags()
	if inherited == nil {
		return ""
	}
	if flag := inherited.Lookup(name); flag != nil {
		return strings.TrimSpace(flag.Value.String())
	}

	return ""
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
	return filepath.Join(repositoryDirPath(cfg, model), model.PackageName+"_i.go")
}

func repositoryUtilFilePath(cfg Config, model Model) string {
	return filepath.Join(repositoryDirPath(cfg, model), model.PackageName+"_util.go")
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
