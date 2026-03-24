// Package make 命令行的 make 命令.
package make

import (
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gtkit/migrate/v2/console"
	"github.com/gtkit/migrate/v2/file"
	"github.com/gtkit/stringx"
	"github.com/spf13/cobra"
)

// Model 参数解释
//
// 单个词，以 User 模型为例：
//
//	{
//	    "TableName": "users",
//	    "StructName": "User",
//	    "StructNamePlural": "Users",
//	    "VariableName": "user",
//	    "VariableNamePlural": "users",
//	    "PackageName": "user"
//	}
//
// 两个词以上，以 TopicComment 模型为例：
//
//	{
//	    "TableName": "topic_comments",
//	    "StructName": "TopicComment",
//	    "StructNamePlural": "TopicComments",
//	    "VariableName": "topicComment",
//	    "VariableNamePlural": "topicComments",
//	    "PackageName": "topic_comment"
//	}
type Model struct {
	TableName          string
	StructName         string
	StructNamePlural   string
	StructFieldName    string
	VariableName       string
	VariableNamePlural string
	PackageName        string
	ActionName         string
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

// makeModelFromString 格式化用户输入的内容.
func makeModelFromString(project, action, name, column string) Model {
	model := Model{}
	model.StructName = stringx.Singular(stringx.ToCamel(name))
	model.StructNamePlural = stringx.Plural(model.StructName)
	model.TableName = stringx.ToSnake(model.StructNamePlural)
	model.VariableName = stringx.ToLowerCamel(model.StructName)
	model.PackageName = stringx.ToSnake(model.StructName)
	model.VariableNamePlural = stringx.ToLowerCamel(model.StructNamePlural)
	model.ActionName = action
	model.ProjectName = project
	model.ColumnName = column
	model.StructFieldName = stringx.ToCamel(column)

	return model
}

func enrichModel(cfg Config, model Model) Model {
	model.ProjectName = cfg.ProjectName
	model.ModelPackageName = modelPackageName(cfg.ModelDir)
	model.ModelsImportPath = moduleImportPath(cfg.ProjectName, cfg.ModelDir)
	return model
}

// createFileFromStub 读取 stub 文件并进行变量替换.
// 最后一个选项可选，如若传参，应传 map[string]string 类型作为附加的变量替换.
func createFileFromStub(filePath, stubName string, model Model, mode fileWriteMode, variables ...any) error {
	replaces := make(map[string]string)
	if len(variables) > 0 {
		if m, ok := variables[0].(map[string]string); ok {
			replaces = m
		}
	}

	// 读取 stub 模板文件
	modelData, err := stubsFS.ReadFile("stubs/" + stubName + ".stub")
	if err != nil {
		return fmt.Errorf("read stub %s: %w", stubName, err)
	}

	modelStub := string(modelData)

	// 默认替换变量
	replaces["{{VariableName}}"] = model.VariableName
	replaces["{{VariableNamePlural}}"] = model.VariableNamePlural
	replaces["{{StructName}}"] = model.StructName
	replaces["{{StructNamePlural}}"] = model.StructNamePlural
	replaces["{{PackageName}}"] = model.PackageName
	replaces["{{TableName}}"] = model.TableName
	replaces["{{ActionName}}"] = model.ActionName
	replaces["{{ProjectName}}"] = model.ProjectName
	replaces["{{ColumnName}}"] = model.ColumnName
	replaces["{{StructFieldName}}"] = model.StructFieldName
	replaces["{{ModelPackageName}}"] = model.ModelPackageName
	replaces["{{ModelsImportPath}}"] = model.ModelsImportPath

	for search, replace := range replaces {
		modelStub = strings.ReplaceAll(modelStub, search, replace)
	}

	return writeGeneratedFile(filePath, []byte(modelStub), mode)
}

type fileWriteMode int

const (
	writeFailIfExists fileWriteMode = iota
	writeSkipIfExists
	writeOverwrite
)

func writeGeneratedFile(filePath string, data []byte, mode fileWriteMode) error {
	if file.Exists(filePath) {
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
