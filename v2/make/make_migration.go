package make

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gtkit/migrate/v2/console"
	"github.com/gtkit/stringx"
	"github.com/spf13/cobra"
)

// CmdMakeMigration 生成迁移文件（make migration），create 动作同时生成 model 脚手架.
var CmdMakeMigration = &cobra.Command{
	Use:   "migration",
	Short: "Create a migration file, example: make migration add_users_table",
	RunE:  runMakeMigration,
	Args:  cobra.ExactArgs(1),
}

func init() {
	// --after 仅对 add 操作生效：在生成的 raw SQL 中追加 AFTER 子句，把新列放在该列之后（MySQL AFTER 语义）。
	CmdMakeMigration.Flags().String("after", "", "For `add_*` migrations: place the new column AFTER this column (MySQL AFTER clause)")
	CmdMakeMigration.Flags().String("type", "", "For `add_*` migrations: column type, e.g. 'VARCHAR(128)'; generates a complete ADD COLUMN instead of a TODO placeholder")
	CmdMakeMigration.Flags().Bool("not-null", false, "For `add_*` migrations with --type: add NOT NULL")
	CmdMakeMigration.Flags().String("default", "", "For `add_*` migrations with --type: DEFAULT expression, e.g. \"''\" or 0")
	CmdMakeMigration.Flags().String("comment", "", "For `add_*` migrations with --type: column COMMENT")
	CmdMakeMigration.Flags().Bool("from-model", false, "For `create_*` migrations: build the snapshot struct from the registered DDL model with the same table name (skips model/repository scaffolding)")
}

func runMakeMigration(cmd *cobra.Command, args []string) error {
	cfg := resolveConfig(cmd)
	action, objectName, tableName, columnName, err := parseMigrationName(args[0])
	if err != nil {
		return err
	}
	fromModel, err := cmd.Flags().GetBool("from-model")
	if err != nil {
		return err
	}

	timeStr := time.Now().UTC().Format("2006_01_02_150405")
	fileName := timeStr + "_" + args[0]
	extra := map[string]string{"{{FileName}}": fileName}

	// 所有校验与源码生成先于任何落盘，失败不留孤儿文件.
	switch action {
	case "create":
		// 自包含的表结构快照（不引用业务 model），需要一个唯一的快照 struct 名。
		extra["{{SnapshotStruct}}"] = stringx.Singular(stringx.ToCamel(tableName)) + "V" + strings.ReplaceAll(timeStr, "_", "")
		snapshot := defaultSnapshot()
		if fromModel {
			if snapshot, err = snapshotFromModel(cfg.DB, cfg.DDLModels, tableName); err != nil {
				return err
			}
		} else if cfg.ProjectName, err = resolveProjectName(cfg); err != nil {
			return err // 脚手架的 import 前缀需要项目名
		}
		extra["{{SnapshotImports}}"] = snapshot.imports
		extra["{{SnapshotFields}}"] = snapshot.fields
	case "add":
		sql, todo, err := addColumnSQL(cmd, tableName, columnName)
		if err != nil {
			return err
		}
		extra["{{AddColumnSQL}}"] = strconv.Quote(sql)
		extra["{{AddColumnTodo}}"] = todo
	}

	model := newModel(cfg, tableName, columnName)
	if action == "create" && !fromModel {
		if err := generateModelScaffold(cfg, model); err != nil {
			return err
		}
	}
	if err := createFileFromStub(migrationDocFilePath(cfg), "migration_doc", model, writeSkipIfExists, nil); err != nil {
		return err
	}
	if err := createFileFromStub(migrationFilePath(cfg, fileName), migrationStubName(action, objectName), model, writeFailIfExists, extra); err != nil {
		return err
	}

	console.Success("Migration file created. After modifying it, use `migrate up` to run.")
	return nil
}

// addColumnTodo 是 add 模板在未给出 --type 时保留的补全提示.
const addColumnTodo = "\t\t// TODO: 补全列定义（类型/约束/注释）。大表建议标注在线 DDL 策略，例如：\n" +
	"\t\t//   ADD COLUMN `col` VARCHAR(64) NOT NULL DEFAULT '' COMMENT '说明', ALGORITHM=INPLACE, LOCK=NONE\n"

// addColumnSQL 由 --type/--not-null/--default/--comment/--after 拼出 ADD COLUMN 语句；
// 未给出 --type 时保留 TODO 占位并返回提示注释（lint 会拦截未补全的迁移）.
func addColumnSQL(cmd *cobra.Command, tableName, columnName string) (sql, todo string, err error) {
	flags := cmd.Flags()
	typ, _ := flags.GetString("type")
	notNull, _ := flags.GetBool("not-null")
	def, _ := flags.GetString("default")
	comment, _ := flags.GetString("comment")
	after, _ := flags.GetString("after")

	definition := "/* TODO: 列定义 */"
	if typ = strings.TrimSpace(typ); typ != "" {
		definition = typ
		if notNull {
			definition += " NOT NULL"
		}
		if flags.Changed("default") {
			definition += " DEFAULT " + def
		}
		if comment != "" {
			definition += " COMMENT '" + strings.ReplaceAll(comment, "'", "''") + "'"
		}
	} else {
		if notNull || flags.Changed("default") || comment != "" {
			return "", "", fmt.Errorf("--not-null, --default and --comment require --type")
		}
		todo = addColumnTodo
	}

	sql = "ALTER TABLE `" + tableName + "` ADD COLUMN `" + columnName + "` " + definition
	if after != "" {
		sql += " AFTER `" + after + "`"
	}
	return sql, todo, nil
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

// migrationStubName 按 action 与对象类型选择模板；action 已由 parseMigrationName 校验.
func migrationStubName(action, objectName string) string {
	switch action {
	case "create":
		return "migration_create"
	case "add":
		return "migration_add"
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
