// Package make 命令行的 make 命令.
package make

import (
	"embed"
	"fmt"
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
	VariableName       string
	VariableNamePlural string
	PackageName        string
	ActionName         string
	ProjectName        string
	ColumnName         string
}

//go:embed stubs
var stubsFS embed.FS

// CmdMake 说明 cobra 命令.
var CmdMake = &cobra.Command{
	Use:   "make",
	Short: "Generate file and code",
}

func init() {
	CmdMake.AddCommand(
		CmdMakeCMD,
		CmdMakeModel,
		CmdMakeMigration,
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

	return model
}

// createFileFromStub 读取 stub 文件并进行变量替换.
// 最后一个选项可选，如若传参，应传 map[string]string 类型作为附加的变量替换.
func createFileFromStub(filePath, stubName string, model Model, variables ...any) {
	replaces := make(map[string]string)
	if len(variables) > 0 {
		if m, ok := variables[0].(map[string]string); ok {
			replaces = m
		}
	}

	// 目标文件已存在
	if file.Exists(filePath) {
		if strings.Contains(filePath, "models") {
			return
		}
		console.Exit(filePath + " already exists!")
	}

	// 读取 stub 模板文件
	modelData, err := stubsFS.ReadFile("stubs/" + stubName + ".stub")
	if err != nil {
		console.Exit(err.Error())
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

	for search, replace := range replaces {
		modelStub = strings.ReplaceAll(modelStub, search, replace)
	}

	if err := file.Put([]byte(modelStub), filePath); err != nil {
		console.Exit(err.Error())
	}

	console.Success(fmt.Sprintf("[%s] created.", filePath))
}
