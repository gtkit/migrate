package make

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gtkit/migrate/console"
	"github.com/gtkit/migrate/internal/snapshot"
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
	RunE:  runMakeMigration,
	Args:  cobra.ExactArgs(1),
}

func init() {
	// --after 仅对 add 操作生效：指定后改用 raw SQL stub，并把新列放在该列之后
	// （MySQL AFTER 语义；GORM Migrator().AddColumn 无法指定列顺序）。
	CmdMakeMigration.Flags().String("after", "", "For `add_*` migrations: place the new column AFTER this column (generates a raw SQL ALTER stub)")
	CmdMakeMigration.Flags().Bool("from-model", false, "For `create_*` migrations: build a snapshot from a registered GORM model")
	CmdMakeMigration.Flags().Bool("unique", false, "For `add_index_*` migrations: create a UNIQUE index")
	CmdMakeMigration.Flags().String("index-name", "", "For `add_index_*` migrations: override the index name")
	CmdMakeMigration.Flags().String("columns", "", "For `add_index_*` migrations: comma-separated columns of a composite index")
}

var migrationIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func runMakeMigration(cmd *cobra.Command, args []string) error {
	var tableName, newTableName, columnName, objectName string
	arg := args[0]
	action, rest, ok := strings.Cut(arg, "_")
	body, suffix := strings.CutSuffix(rest, "_table")
	fromModel, err := cmd.Flags().GetBool("from-model")
	if err != nil {
		return err
	}
	if fromModel && (action != "create" || !suffix || !migrationIdentifier.MatchString(body)) {
		return fmt.Errorf("--from-model requires create_<table>_table with a valid table name: %s", arg)
	}
	if !ok || !suffix {
		console.Error("Invalid migration name: " + arg)
		return nil
	}

	switch action {
	case "create", "update":
		tableName = body
	case "rename":
		var found bool
		tableName, newTableName, found = strings.Cut(body, "_to_")
		if !found || strings.LastIndex(body, "_to_") != len(tableName) {
			console.Error("Expected a single _to_ separator in rename migration name: " + arg)
			return nil
		}
		if !migrationIdentifier.MatchString(newTableName) || tableName == newTableName {
			console.Error("Rename requires distinct valid table names: " + arg)
			return nil
		}
	case "add":
		columnName, tableName, _ = strings.Cut(body, "_to_")
		objectName = "column"
		if indexColumn, isIndex := strings.CutPrefix(columnName, "index_"); isIndex {
			columnName = indexColumn
			objectName = "index"
		}
	case "drop":
		target, table, found := strings.Cut(body, "_from_")
		if !found {
			tableName = body
			break
		}
		tableName = table
		switch {
		case strings.HasPrefix(target, "column_"):
			objectName = "column"
			columnName = strings.TrimPrefix(target, "column_")
		case strings.HasPrefix(target, "index_"):
			objectName = "index"
			columnName = strings.TrimPrefix(target, "index_")
		default:
			console.Error("Expected drop_column or drop_index in: " + arg)
			return nil
		}
	default:
		console.Error("Invalid migration action: " + action)
		return nil
	}

	if !migrationIdentifier.MatchString(tableName) ||
		(objectName != "" && !migrationIdentifier.MatchString(columnName)) {
		console.Error("Invalid table, column or index name in: " + arg)
		return nil
	}

	after, _ := cmd.Flags().GetString("after")
	if after != "" && (action != "add" || objectName != "column" || !migrationIdentifier.MatchString(after)) {
		if fromModel {
			return errors.New("--after does not apply to create --from-model")
		}
		console.Error("--after requires an add migration and a valid column name")
		return nil
	}
	if action != "add" || objectName != "index" {
		for _, name := range []string{"unique", "index-name", "columns"} {
			if cmd.Flags().Changed(name) {
				return fmt.Errorf("--%s only applies to add_index migrations", name)
			}
		}
	}

	fmt.Printf("Table: %s, Action: %s", tableName, action)
	if columnName != "" {
		fmt.Printf(", Column: %s", columnName)
	}

	// 使用 UTC 时间，避免时区硬编码
	timeStr := time.Now().UTC().Format("2006_01_02_150405")
	model := makeModelFromString(projectName, action, tableName, columnName)
	model.TableName = tableName

	// create 操作同时生成 model 和 repository 文件
	if action == "create" && !fromModel {
		repositoryDir := fmt.Sprintf("internal/repository/%s/", model.PackageName)
		if err := os.MkdirAll(repositoryDir, os.ModePerm); err != nil {
			console.Error("Failed to create repository directory: " + err.Error())
			return nil
		}
		createFileFromStub("internal/models/model.go", "model/base", model)
		createFileFromStub("internal/models/"+model.PackageName+".go", "model/model", model)
		createFileFromStub(repositoryDir+model.PackageName+"_i.go", "model/i", model)
		createFileFromStub(repositoryDir+model.PackageName+"_util.go", "model/model_util", model)
	}

	fileName := timeStr + "_" + arg
	filePath := fmt.Sprintf("database/migrations/%s.go", fileName)

	extra := map[string]string{
		"{{FileName}}":     fileName,
		"{{NewTableName}}": newTableName,
	}
	stubName := migrationStubName(action, objectName)
	if stubName == "addindex" {
		if err := addIndexReplacements(cmd, tableName, columnName, extra); err != nil {
			return err
		}
	}
	if fromModel {
		source, err := snapshot.FromModel(tableName)
		if err != nil {
			return err
		}
		stubName = "migration_create_from_model"
		extra["{{SnapshotImports}}"] = source.Imports
		extra["{{SnapshotFields}}"] = source.Fields
	}
	// add 操作指定了 --after 时改用 raw SQL stub，以支持 MySQL 的 AFTER 列定位。
	if after != "" {
		stubName = "migration_add_raw"
		extra["{{AfterColumnName}}"] = after
	}
	createFileFromStub(filePath, stubName, model, extra)

	if action == "rename" || fromModel || stubName == "addindex" {
		console.Success("Migration file created. Use `migrate up` to run.")
		return nil
	}
	console.Success("Migration file created. After modifying it, use `migrate up` to run.")
	return nil
}

func migrationStubName(action, objectName string) string {
	switch action {
	case "create":
		return "migration_create"
	case "add":
		if objectName == "index" {
			return "addindex"
		}
		return "migration_add"
	case "update":
		return "migration_update"
	case "rename":
		return "migration_rename"
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

func addIndexReplacements(cmd *cobra.Command, table, column string, extra map[string]string) error {
	flags := cmd.Flags()
	columns := []string{column}
	if raw, _ := flags.GetString("columns"); strings.TrimSpace(raw) != "" {
		columns = strings.Split(raw, ",")
	}
	quotedSQL := make([]string, len(columns))
	quotedGo := make([]string, len(columns))
	for i, column := range columns {
		column = strings.TrimSpace(column)
		if !migrationIdentifier.MatchString(column) || len(column) > 64 {
			return fmt.Errorf("invalid index column %q", column)
		}
		columns[i] = column
		quotedSQL[i] = "`" + column + "`"
		quotedGo[i] = strconv.Quote(column)
	}
	name, _ := flags.GetString("index-name")
	if name = strings.TrimSpace(name); name == "" {
		name = "idx_" + table + "_" + strings.Join(columns, "_")
	}
	if !migrationIdentifier.MatchString(name) || len(name) > 64 || len(table) > 64 {
		return fmt.Errorf("invalid MySQL table or index name %q/%q", table, name)
	}
	unique, _ := flags.GetBool("unique")
	kind := "INDEX"
	if unique {
		kind = "UNIQUE INDEX"
	}
	const online = ", ALGORITHM=INPLACE, LOCK=NONE"
	extra["{{IndexMatch}}"] = fmt.Sprintf("migration.MySQLIndexMatches(db, %q, %q, %t, %s)", table, name, unique, strings.Join(quotedGo, ", "))
	extra["{{AddIndexSQL}}"] = strconv.Quote(fmt.Sprintf("ALTER TABLE `%s` ADD %s `%s` (%s)%s", table, kind, name, strings.Join(quotedSQL, ", "), online))
	extra["{{DropIndexSQL}}"] = strconv.Quote(fmt.Sprintf("ALTER TABLE `%s` DROP INDEX `%s`%s", table, name, online))
	return nil
}
