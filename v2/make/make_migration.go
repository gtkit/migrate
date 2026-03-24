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

	stubName := migrationStubName(action, objectName)
	if err := createFileFromStub(filePath, stubName, model, writeFailIfExists, map[string]string{"{{FileName}}": fileName}); err != nil {
		return err
	}

	console.Success("Migration file created. After modifying it, use `migrate up` to run.")
	return nil
}

func ensureMigrationSupportFiles(cfg Config, model Model) error {
	return createFileFromStub(migrationDocFilePath(cfg), "migration_doc", model, writeSkipIfExists)
}

func parseMigrationName(arg string) (action, objectName, tableName, columnName string, err error) {
	index := strings.Index(arg, "_")
	if index < 0 {
		return "", "", "", "", fmt.Errorf("invalid migration name: %s", arg)
	}

	action = arg[:index]
	validActions := map[string]bool{
		"add": true, "drop": true, "update": true, "create": true,
	}
	if !validActions[action] {
		return "", "", "", "", fmt.Errorf("invalid action: %s (expected: add, drop, update, create)", action)
	}

	lastIndex := strings.LastIndex(arg, "_")
	if lastIndex < 0 || arg[lastIndex+1:] != "table" {
		return "", "", "", "", fmt.Errorf("invalid migration suffix in %s (expected: *_table)", arg)
	}

	switch action {
	case "add":
		toIndex := strings.Index(arg, "_to_")
		if toIndex < 0 {
			return "", "", "", "", fmt.Errorf("invalid add migration name: %s (expected: add_<column>_to_<table>_table)", arg)
		}
		columnName = arg[index+1 : toIndex]
		tableName = arg[toIndex+4 : lastIndex]
		objectName = "column"
	case "drop":
		fromIndex := strings.Index(arg, "_from_")
		if fromIndex < 0 {
			tableName = arg[index+1 : lastIndex]
			objectName = "table"
			break
		}
		tableName = arg[fromIndex+6 : lastIndex]
		switch {
		case strings.HasPrefix(arg[index+1:fromIndex], "index_"):
			columnName = strings.TrimPrefix(arg[index+1:fromIndex], "index_")
			objectName = "index"
		case strings.HasPrefix(arg[index+1:fromIndex], "column_"):
			columnName = strings.TrimPrefix(arg[index+1:fromIndex], "column_")
			objectName = "column"
		default:
			return "", "", "", "", fmt.Errorf("invalid drop migration name: %s (expected drop_column_* or drop_index_*)", arg)
		}
	case "create", "update":
		tableName = arg[index+1 : lastIndex]
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
