// Package make 鍛戒护琛岀殑 make 鍛戒护.
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

// Model 鍙傛暟瑙ｉ噴
//
// 鍗曚釜璇嶏紝浠?User 妯″瀷涓轰緥锛?
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
// 涓や釜璇嶄互涓婏紝浠?TopicComment 妯″瀷涓轰緥锛?
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

// CmdMake 璇存槑 cobra 鍛戒护.
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

// makeModelFromString 鏍煎紡鍖栫敤鎴疯緭鍏ョ殑鍐呭.
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

// createFileFromStub 璇诲彇 stub 鏂囦欢骞惰繘琛屽彉閲忔浛鎹?
// 鏈€鍚庝竴涓€夐」鍙€夛紝濡傝嫢浼犲弬锛屽簲浼?map[string]string 绫诲瀷浣滀负闄勫姞鐨勫彉閲忔浛鎹?
func createFileFromStub(filePath, stubName string, model Model, variables ...any) {
	replaces := make(map[string]string)
	if len(variables) > 0 {
		if m, ok := variables[0].(map[string]string); ok {
			replaces = m
		}
	}

	// 鐩爣鏂囦欢宸插瓨鍦?
	if file.Exists(filePath) {
		if strings.Contains(filePath, "models") {
			return
		}
		console.Exit(filePath + " already exists!")
	}

	// 璇诲彇 stub 妯℃澘鏂囦欢
	modelData, err := stubsFS.ReadFile("stubs/" + stubName + ".stub")
	if err != nil {
		console.Exit(err.Error())
	}

	modelStub := string(modelData)

	// 榛樿鏇挎崲鍙橀噺
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
