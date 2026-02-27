package make

import (
	"fmt"
	"strings"
	"time"

	"github.com/gtkit/logger"
	"github.com/gtkit/migrate"
	"github.com/gtkit/migrate/console"
	"github.com/spf13/cobra"
)

var CmdMakeMigration = &cobra.Command{
	Use:   "migration",
	Short: "Create a migration file, example: make migration add_users_table",
	Run:   runMakeMigration,
	Args:  cobra.ExactArgs(1), // 只允许且必须传 1 个参数
}

func runMakeMigration(cmd *cobra.Command, args []string) {
	var tableName, columnName, objectName string
	arg := args[0]
	index := strings.Index(arg, "_")

	action := arg[:index]
	if action != "add" && action != "drop" && action != "update" && action != "create" {
		logger.Errorf("Invalid action: %s", action)

		return
	}

	lastIndex := strings.LastIndex(arg, "_")

	sufffix := arg[lastIndex+1:]
	if sufffix != "table" {
		logger.Errorf("Invalid sufffix: %s", sufffix)

		return
	}

	if action == "add" && strings.Index(arg, "_to_") > 0 {
		toindex := strings.Index(arg, "_to_")
		tableName = arg[toindex+4 : lastIndex]
	} else if action == "drop" && strings.Index(arg, "_from_") > 0 {
		toindex := strings.Index(arg, "_from_")
		tableName = arg[toindex+6 : lastIndex]
		if strings.Contains(arg, "_index_") {
			columnName = arg[strings.Index(arg, "_index_")+7 : toindex]
			objectName = "index"
		}
		if strings.Contains(arg, "_column_") {
			columnName = arg[strings.Index(arg, "_column_")+8 : toindex]
			objectName = "column"
		}
	} else {
		tableName = arg[index+1 : lastIndex]
	}
	fmt.Printf("Table name: %s, action: %s, column name: %s\n", tableName, action, columnName)

	if tableName == "" {
		logger.Errorf("Invalid table name: %s", tableName)

		return
	}

	// 日期格式化
	timeStr := TimenowInTimezone().Format("2006_01_02_150405")
	// 创建 model 对象
	model := makeModelFromString(migrate.ProjectName, action, tableName, columnName)

	// 创建 model 文件
	if action == "create" {
		createFileFromStub("internal/models/"+model.PackageName+".go", "model/model", model)
	}

	// 创建 migration 文件
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

	console.Success("Migration file created，after modify it, use `migrate up` to migrate database.")
}

// TimenowInTimezone 获取当前时间，支持时区.
func TimenowInTimezone() time.Time {
	chinaTimezone, _ := time.LoadLocation("Asia/Shanghai")

	return time.Now().In(chinaTimezone)
}
