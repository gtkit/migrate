package make

import (
	"fmt"
	"strings"
	"time"

	"github.com/gtkit/migrate/v2/console"
	"github.com/spf13/cobra"
)

var CmdMakeMigration = &cobra.Command{
	Use:   "migration",
	Short: "Create a migration file, example: make migration add_users_table",
	RunE:  runMakeMigration,
	Args:  cobra.ExactArgs(1),
}

func init() {
	// --after 仅对 add 操作生效：在生成的 raw SQL 中追加 AFTER 子句，把新列放在该列之后（MySQL AFTER 语义）。
	CmdMakeMigration.Flags().String("after", "", "For `add_*` migrations: place the new column AFTER this column (MySQL AFTER clause)")
}

func runMakeMigration(cmd *cobra.Command, args []string) error {
	cfg := resolveConfig(cmd)
	action, objectName, tableName, columnName, err := parseMigrationName(args[0])
	if err != nil {
		return err
	}

	timeStr := time.Now().UTC().Format("2006_01_02_150405")
	model := enrichModel(cfg, makeModelFromString(cfg.ProjectName, action, tableName, columnName))

	if action == "create" {
		if err := generateModelScaffold(cfg, model); err != nil {
			return err
		}
	}

	if err := ensureMigrationSupportFiles(cfg, model); err != nil {
		return err
	}

	fileName := timeStr + "_" + args[0]
	filePath := migrationFilePath(cfg, fileName)

	extra := map[string]string{"{{FileName}}": fileName}
	stubName := migrationStubName(action, objectName)
	// create 迁移使用自包含的表结构快照（不引用业务 model），需要一个唯一的快照 struct 名。
	if action == "create" {
		compact := strings.ReplaceAll(timeStr, "_", "")
		extra["{{SnapshotStruct}}"] = model.StructName + "V" + compact
	}
	// add 统一使用 raw SQL 模板；--after 通过 AFTER 子句注入（MySQL 列定位）。
	if action == "add" {
		afterClause := ""
		if after, _ := cmd.Flags().GetString("after"); after != "" {
			afterClause = " AFTER `" + after + "`"
		}
		extra["{{AfterClause}}"] = afterClause
	}
	if err := createFileFromStub(filePath, stubName, model, writeFailIfExists, extra); err != nil {
		return err
	}

	console.Success("Migration file created. After modifying it, use `migrate up` to run.")
	return nil
}

func ensureMigrationSupportFiles(cfg Config, model Model) error {
	return createFileFromStub(migrationDocFilePath(cfg), "migration_doc", model, writeSkipIfExists)
}

func parseMigrationName(arg string) (action, objectName, tableName, columnName string, err error) {
	action, rest, found := strings.Cut(arg, "_")
	if !found {
		return "", "", "", "", fmt.Errorf("invalid migration name: %s", arg)
	}

	switch action {
	case "add", "drop", "update", "create":
	default:
		return "", "", "", "", fmt.Errorf("invalid action: %s (expected: add, drop, update, create)", action)
	}

	// rest 形如 <body>_table，先剥掉固定后缀.
	body, found := strings.CutSuffix(rest, "_table")
	if !found {
		return "", "", "", "", fmt.Errorf("invalid migration suffix in %s (expected: *_table)", arg)
	}

	switch action {
	case "add":
		column, table, found := strings.Cut(body, "_to_")
		if !found {
			return "", "", "", "", fmt.Errorf("invalid add migration name: %s (expected: add_<column>_to_<table>_table)", arg)
		}
		columnName = column
		tableName = table
		objectName = "column"
	case "drop":
		target, table, found := strings.Cut(body, "_from_")
		if !found {
			tableName = body
			objectName = "table"
			break
		}
		tableName = table
		switch {
		case strings.HasPrefix(target, "index_"):
			columnName = strings.TrimPrefix(target, "index_")
			objectName = "index"
		case strings.HasPrefix(target, "column_"):
			columnName = strings.TrimPrefix(target, "column_")
			objectName = "column"
		default:
			return "", "", "", "", fmt.Errorf("invalid drop migration name: %s (expected drop_column_* or drop_index_*)", arg)
		}
	case "create", "update":
		tableName = body
		objectName = "table"
	}

	if tableName == "" {
		return "", "", "", "", fmt.Errorf("could not parse table name from: %s", arg)
	}

	return action, objectName, tableName, columnName, nil
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
