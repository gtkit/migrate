package make

import (
	"fmt"
	"strings"
	"time"

	"github.com/gtkit/migrate/v2/console"
	"github.com/spf13/cobra"
)

// projectName 用于代码生成的项目名称.
// 通过 SetProjectName 设置.
var projectName = "project_name"

// SetProjectName 设置项目名称（由 migrate.Setup 调用）.
func SetProjectName(name string) {
	if name != "" {
		projectName = name
	}
}

var CmdMakeMigration = &cobra.Command{
	Use:   "migration",
	Short: "Create a migration file, example: make migration add_users_table",
	Run:   runMakeMigration,
	Args:  cobra.ExactArgs(1),
}

func runMakeMigration(_ *cobra.Command, args []string) {
	var tableName, columnName, objectName string
	arg := args[0]

	index := strings.Index(arg, "_")
	if index < 0 {
		console.Error("Invalid migration name: " + arg)
		return
	}

	action := arg[:index]
	validActions := map[string]bool{
		"add": true, "drop": true, "update": true, "create": true,
	}
	if !validActions[action] {
		console.Error(fmt.Sprintf("Invalid action: %s (expected: add, drop, update, create)", action))
		return
	}

	lastIndex := strings.LastIndex(arg, "_")
	suffix := arg[lastIndex+1:]
	if suffix != "table" {
		console.Error(fmt.Sprintf("Invalid suffix: %s (expected: table)", suffix))
		return
	}

	if action == "add" && strings.Contains(arg, "_to_") {
		toIndex := strings.Index(arg, "_to_")
		tableName = arg[toIndex+4 : lastIndex]
	} else if action == "drop" && strings.Contains(arg, "_from_") {
		toIndex := strings.Index(arg, "_from_")
		tableName = arg[toIndex+6 : lastIndex]
		if idx := strings.Index(arg, "_index_"); idx > 0 {
			columnName = arg[idx+7 : toIndex]
			objectName = "index"
		}
		if idx := strings.Index(arg, "_column_"); idx > 0 {
			columnName = arg[idx+8 : toIndex]
			objectName = "column"
		}
	} else {
		tableName = arg[index+1 : lastIndex]
	}

	if tableName == "" {
		console.Error("Could not parse table name from: " + arg)
		return
	}

	fmt.Printf("Table: %s, Action: %s", tableName, action)
	if columnName != "" {
		fmt.Printf(", Column: %s", columnName)
	}

	// 使用 UTC 时间，避免时区硬编码
	timeStr := time.Now().UTC().Format("2006_01_02_150405")
	model := makeModelFromString(projectName, action, tableName, columnName)

	// create 操作同时生成 model 文件
	if action == "create" {
		createFileFromStub("internal/models/"+model.PackageName+".go", "model/model", model)
	}

	fileName := timeStr + "_" + arg
	filePath := fmt.Sprintf("database/migrations/%s.go", fileName)

	switch objectName {
	case "index":
		createFileFromStub(filePath, "dropindex", model, map[string]string{"{{FileName}}": fileName})
	case "column":
		createFileFromStub(filePath, "dropcolumn", model, map[string]string{"{{FileName}}": fileName})
	default:
		createFileFromStub(filePath, "migration", model, map[string]string{"{{FileName}}": fileName})
	}

	console.Success("Migration file created. After modifying it, use `migrate up` to run.")
}
