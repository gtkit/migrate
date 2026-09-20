// Package make 命令行的 make 命令.
package make

import (
	"embed"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/gtkit/migrate/v2/console"
	"github.com/gtkit/migrate/v2/file"
	"github.com/gtkit/stringx"
	"github.com/spf13/cobra"
)

// Model 是模板渲染变量，由用户输入的名称推导.
// 以 topic_comment 为例：TableName=topic_comments、StructName=TopicComment、
// VariableName=topicComment、VariableNamePlural=topicComments、PackageName=topic_comment.
type Model struct {
	TableName          string
	StructName         string
	VariableName       string
	VariableNamePlural string
	PackageName        string
	ProjectName        string
	ColumnName         string
	ModelPackageName   string
	ModelsImportPath   string
}

//go:embed stubs
var stubsFS embed.FS

// CmdMake 说明 cobra 命令.
var CmdMake = &cobra.Command{
	Use:   "make",
	Short: "Generate file and code",
}

func init() {
	CmdMake.PersistentFlags().String("model-dir", "", "Override model output directory")
	CmdMake.PersistentFlags().String("repository-dir", "", "Override repository output directory")
	CmdMake.PersistentFlags().String("migration-dir", "", "Override migration output directory")
	CmdMake.PersistentFlags().String("ddl-dir", "", "Override DDL output directory")

	CmdMake.AddCommand(
		CmdMakeCMD,
		CmdMakeModel,
		CmdMakeMigration,
		CmdMakeDDL,
	)
}

// newModel 由用户输入的名称与列名推导模板变量，并补齐目录相关的包名与 import 路径.
func newModel(cfg Config, name, column string) Model {
	structName := stringx.Singular(stringx.ToCamel(name))
	plural := stringx.Plural(structName)
	return Model{
		TableName:          stringx.ToSnake(plural),
		StructName:         structName,
		VariableName:       stringx.ToLowerCamel(structName),
		VariableNamePlural: stringx.ToLowerCamel(plural),
		PackageName:        stringx.ToSnake(structName),
		ProjectName:        cfg.ProjectName,
		ColumnName:         column,
		ModelPackageName:   modelPackageName(cfg.ModelDir),
		ModelsImportPath:   moduleImportPath(cfg.ProjectName, cfg.ModelDir),
	}
}

// createFileFromStub 读取 stub 模板、替换 Model 变量与 extra 中的附加变量后写入；
// .go 产物统一 gofmt，格式化失败视为生成失败.
func createFileFromStub(filePath, stubName string, model Model, mode fileWriteMode, extra map[string]string) error {
	stub, err := stubsFS.ReadFile("stubs/" + stubName + ".stub")
	if err != nil {
		return fmt.Errorf("read stub %s: %w", stubName, err)
	}

	replaces := map[string]string{
		"{{VariableName}}":       model.VariableName,
		"{{VariableNamePlural}}": model.VariableNamePlural,
		"{{StructName}}":         model.StructName,
		"{{PackageName}}":        model.PackageName,
		"{{TableName}}":          model.TableName,
		"{{ProjectName}}":        model.ProjectName,
		"{{ColumnName}}":         model.ColumnName,
		"{{ModelPackageName}}":   model.ModelPackageName,
		"{{ModelsImportPath}}":   model.ModelsImportPath,
	}
	maps.Copy(replaces, extra)

	modelStub := string(stub)
	for search, replace := range replaces {
		modelStub = strings.ReplaceAll(modelStub, search, replace)
	}

	data := []byte(modelStub)
	if strings.HasSuffix(filePath, ".go") {
		if data, err = format.Source(data); err != nil {
			return fmt.Errorf("format generated %s: %w", filePath, err)
		}
	}
	return writeGeneratedFile(filePath, data, mode)
}

type fileWriteMode int

const (
	writeFailIfExists fileWriteMode = iota
	writeSkipIfExists
	writeOverwrite
)

func writeGeneratedFile(filePath string, data []byte, mode fileWriteMode) error {
	// 三态判断：存在 / 不存在 / 未知错误——权限等未知错误显式上报，不静默当作已存在.
	switch _, err := os.Stat(filePath); {
	case err == nil:
		switch mode {
		case writeSkipIfExists:
			return nil
		case writeFailIfExists:
			return fmt.Errorf("%s already exists", filePath)
		case writeOverwrite:
			// continue
		default:
			return errors.New("unsupported file write mode")
		}
	case errors.Is(err, fs.ErrNotExist):
		// 不存在，继续创建
	default:
		return fmt.Errorf("stat %s: %w", filePath, err)
	}

	if err := file.CreateDirIfNotExists(filepath.Dir(filePath)); err != nil {
		return fmt.Errorf("create directory for %s: %w", filePath, err)
	}
	if err := file.Put(data, filePath); err != nil {
		return fmt.Errorf("write %s: %w", filePath, err)
	}
	console.Success(fmt.Sprintf("[%s] created.", filePath))
	return nil
}
