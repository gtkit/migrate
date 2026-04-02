package make

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gtkit/migrate/console"
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
		columnName = arg[index+1 : toIndex]
		tableName = arg[toIndex+4 : lastIndex]
		objectName = "column"
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

	// create 操作同时生成 model 和 repository 文件
	if action == "create" {
		repositoryDir := fmt.Sprintf("internal/repository/%s/", model.PackageName)
		if err := os.MkdirAll(repositoryDir, os.ModePerm); err != nil {
			console.Error("Failed to create repository directory: " + err.Error())
			return
		}
		createFileFromStub("internal/models/model.go", "model/base", model)
		createFileFromStub("internal/models/"+model.PackageName+".go", "model/model", model)
		createFileFromStub(repositoryDir+model.PackageName+"_i.go", "model/i", model)
		createFileFromStub(repositoryDir+model.PackageName+"_util.go", "model/model_util", model)
	}

	fileName := timeStr + "_" + arg
	filePath := fmt.Sprintf("database/migrations/%s.go", fileName)

	stubName := migrationStubName(action, objectName)
	createFileFromStub(filePath, stubName, model, map[string]string{"{{FileName}}": fileName})

	console.Success("Migration file created. After modifying it, use `migrate up` to run.")
}

func migrationStubName(action, objectName string) string {
	switch action {
	case "create":
		return "migration_create"
	case "add":
		return "migration_add"
	case "update":
		return "migration_update"
	case "drop":
		switch objectName {
		case "index":
			return "dropindex"
		case "column":
			return "dropcolumn"
		default:
			return "migration_drop"
		}
	default:
		return "migration_update"
	}
}
