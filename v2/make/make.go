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

	pairs := make([]string, 0, 2*len(replaces))
	for name, value := range replaces {
		pairs = append(pairs, name, value)
	}
	data := []byte(strings.NewReplacer(pairs...).Replace(string(stub)))
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
	dir := filepath.Dir(filePath)
	if err := file.CreateDirIfNotExists(dir, 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", filePath, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open directory for %s: %w", filePath, err)
	}
	defer func() { _ = root.Close() }()

	name := filepath.Base(filePath)
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if mode == writeOverwrite {
		info, err := root.Lstat(name)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", filePath, err)
		}
		if info != nil && !info.Mode().IsRegular() {
			return fmt.Errorf("write %s: expected a regular file", filePath)
		}
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	out, err := root.OpenFile(name, flags, 0o644)
	if errors.Is(err, fs.ErrExist) {
		if mode == writeSkipIfExists {
			return nil
		}
		return fmt.Errorf("write %s: already exists: %w", filePath, err)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", filePath, err)
	}
	_, writeErr := out.Write(data)
	if err := errors.Join(writeErr, out.Close()); err != nil {
		return fmt.Errorf("write %s: %w", filePath, err)
	}
	console.Success(fmt.Sprintf("[%s] created.", filePath))
	return nil
}
