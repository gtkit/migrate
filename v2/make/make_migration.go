package make

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gtkit/migrate/v2/console"
	"github.com/gtkit/stringx"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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
	CmdMakeMigration.Flags().String("type", "", "Column type for `add_*`/`modify_*`/`drop_column_*` migrations, e.g. 'VARCHAR(128)'; generates a complete definition instead of a TODO placeholder")
	CmdMakeMigration.Flags().Bool("not-null", false, "With --type: add NOT NULL to the column definition")
	CmdMakeMigration.Flags().String("default", "", "With --type: DEFAULT expression, e.g. \"''\" or 0")
	CmdMakeMigration.Flags().String("comment", "", "With --type: column COMMENT")
	CmdMakeMigration.Flags().Bool("from-model", false, "For `create_*` migrations: build the snapshot struct from the registered DDL model with the same table name (skips model/repository scaffolding)")
	CmdMakeMigration.Flags().Bool("unique", false, "For `add_index_*` migrations: create a UNIQUE index")
	CmdMakeMigration.Flags().String("table", "", "Table name, required when the migration name contains `_to_`/`_from_`/`_of_` more than once and the split would be ambiguous")
	CmdMakeMigration.Flags().String("index-name", "", "For `add_index_*`/`drop_index_*` migrations: index name (default idx_<table>_<columns>)")
	CmdMakeMigration.Flags().String("columns", "", "For `add_index_*`/`drop_index_*` migrations: comma-separated columns of a composite index (overrides the column parsed from the migration name)")
}

func runMakeMigration(cmd *cobra.Command, args []string) error {
	cfg := resolveConfig(cmd)
	explicitTable, _ := cmd.Flags().GetString("table")
	action, objectName, tableName, columnName, err := parseMigrationName(args[0], strings.TrimSpace(explicitTable))
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
		if objectName == "index" {
			spec, err := resolveIndexSpec(cmd, "add_index_*", tableName, columnName)
			if err != nil {
				return err
			}
			extra["{{IndexName}}"] = spec.Name
			extra["{{AddIndexSQL}}"] = strconv.Quote(spec.AddSQL)
			extra["{{DropIndexSQL}}"] = strconv.Quote(spec.DropSQL)
			break
		}
		sql, todo, err := addColumnSQL(cmd, tableName, columnName)
		if err != nil {
			return err
		}
		extra["{{AddColumnSQL}}"] = strconv.Quote(sql)
		extra["{{AddColumnTodo}}"] = todo
		extra["{{DropColumnSQL}}"] = strconv.Quote(dropColumnSQL(tableName, columnName))
	case "modify":
		sql, todo, err := modifyColumnSQL(cmd, tableName, columnName)
		if err != nil {
			return err
		}
		extra["{{ModifyColumnSQL}}"] = strconv.Quote(sql)
		extra["{{ModifyColumnTodo}}"] = todo
	case "drop":
		switch objectName {
		case "column":
			down, err := dropColumnDown(cmd, tableName, columnName, fileName)
			if err != nil {
				return err
			}
			extra["{{DropColumnSQL}}"] = strconv.Quote(dropColumnSQL(tableName, columnName))
			extra["{{DownBody}}"] = down
		case "index":
			spec, err := resolveIndexSpec(cmd, "drop_index_*", tableName, columnName)
			if err != nil {
				return err
			}
			extra["{{IndexName}}"] = spec.Name
			extra["{{DropIndexSQL}}"] = strconv.Quote(spec.DropSQL)
			extra["{{DownBody}}"] = dropIndexDown(spec, tableName, fileName)
		}
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

// onlineDDLClause 是加列、删列、加删索引这些已确认支持 INPLACE 的语句默认带的在线 DDL 策略.
// LOCK=NONE 让 MySQL 在无法不阻塞完成变更时直接报错，而不是悄悄锁表阻塞业务写入.
// 改列（MODIFY COLUMN）与 update 骨架不预填：它们的可行算法取决于具体变更，
// 预填一个多数场景会报错的值比不填更糟，交由使用者决定、由 lint 提醒.
const onlineDDLClause = ", ALGORITHM=INPLACE, LOCK=NONE"

// columnTodo 是未给出 --type 时保留在生成文件里的补全提示.
const columnTodo = "\t\t// TODO: 补全列定义（类型/约束/注释）。大表建议标注在线 DDL 策略，例如：\n" +
	"\t\t//   `col` VARCHAR(64) NOT NULL DEFAULT '' COMMENT '说明', ALGORITHM=INPLACE, LOCK=NONE\n"

// indexSpec 是一条索引的完整定义，加索引与删索引共用：两者的 up/down 互为反向.
type indexSpec struct {
	Name         string
	AddSQL       string
	DropSQL      string
	ColumnsGiven bool // 是否显式给出了 --columns（删索引据此决定 down 能否重建）
}

// resolveIndexSpec 由 --unique/--index-name/--columns 解析索引定义，并拼出成对的
// ADD INDEX / DROP INDEX 语句.默认标注在线 DDL 策略：LOCK=NONE 在无法不阻塞完成时
// 让 MySQL 直接报错，而非悄悄锁表.
func resolveIndexSpec(cmd *cobra.Command, kindLabel, tableName, columnName string) (indexSpec, error) {
	flags := cmd.Flags()
	if err := rejectFlags(flags, kindLabel, "type", "not-null", "default", "comment", "after"); err != nil {
		return indexSpec{}, err
	}

	spec := indexSpec{}
	columns := []string{columnName}
	if raw, _ := flags.GetString("columns"); strings.TrimSpace(raw) != "" {
		columns = nil
		for c := range strings.SplitSeq(raw, ",") {
			if c = strings.TrimSpace(c); c != "" {
				columns = append(columns, c)
			}
		}
		if len(columns) == 0 {
			return indexSpec{}, errors.New("--columns must list at least one column")
		}
		spec.ColumnsGiven = true
	}
	for _, c := range columns {
		if err := validateIdentifier("column name", c); err != nil {
			return indexSpec{}, err
		}
	}

	name, _ := flags.GetString("index-name")
	if spec.Name = strings.TrimSpace(name); spec.Name == "" {
		// 与 GORM 默认命名策略一致：从 model tag 建表与本迁移产出的索引名可以对上.
		spec.Name = "idx_" + tableName + "_" + strings.Join(columns, "_")
	}
	if err := validateIdentifier("index name", spec.Name); err != nil {
		return indexSpec{}, fmt.Errorf("%w; pass --index-name to override it", err)
	}

	kind := "INDEX"
	if unique, _ := flags.GetBool("unique"); unique {
		kind = "UNIQUE INDEX"
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = "`" + c + "`"
	}

	spec.AddSQL = fmt.Sprintf("ALTER TABLE `%s` ADD %s `%s` (%s)%s",
		tableName, kind, spec.Name, strings.Join(quoted, ", "), onlineDDLClause)
	spec.DropSQL = fmt.Sprintf("ALTER TABLE `%s` DROP INDEX `%s`%s", tableName, spec.Name, onlineDDLClause)
	return spec, nil
}

// dropColumnDown 生成删列迁移的 down：给出 --type 时重建列结构，否则标记不可逆.
// 重建的只是结构，被删除的数据不会恢复——生成的文件里必须写明这一点.
func dropColumnDown(cmd *cobra.Command, tableName, columnName, fileName string) (string, error) {
	if typ, _ := cmd.Flags().GetString("type"); strings.TrimSpace(typ) == "" {
		if err := rejectFlags(cmd.Flags(), "drop_column_* without --type", "not-null", "default", "comment", "after"); err != nil {
			return "", err
		}
		return irreversibleDown("column", fileName), nil
	}
	sql, _, err := addColumnSQL(cmd, tableName, columnName)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`		// 重建的只是列结构，本迁移删除的数据不会恢复。
		if db.Migrator().HasColumn(%q, %q) {
			return nil
		}
		return db.Exec(%s).Error
`, tableName, columnName, strconv.Quote(sql)), nil
}

// dropIndexDown 生成删索引迁移的 down：给出 --columns 时重建索引，否则标记不可逆.
// 索引不承载数据，重建即完全还原，无需数据丢失提示.
func dropIndexDown(spec indexSpec, tableName, fileName string) string {
	if !spec.ColumnsGiven {
		return irreversibleDown("index", fileName)
	}
	return fmt.Sprintf(`		if db.Migrator().HasIndex(%q, %q) {
			return nil
		}
		return db.Exec(%s).Error
`, tableName, spec.Name, strconv.Quote(spec.AddSQL))
}

// rejectFlags 拒绝与当前迁移形态无关的 flag：写了却被静默忽略比报错危险.
func rejectFlags(flags *pflag.FlagSet, kind string, names ...string) error {
	for _, name := range names {
		if flags.Changed(name) {
			return fmt.Errorf("--%s does not apply to %s migrations", name, kind)
		}
	}
	return nil
}

// columnDefinition 由 --type/--not-null/--default/--comment 拼出列定义片段.
// 未给出 --type 时返回 TODO 占位与提示注释（lint 会拦截未补全的迁移）；
// 修饰参数必须与 --type 同时给出，否则它们会被静默忽略.
func columnDefinition(flags *pflag.FlagSet) (definition, todo string, err error) {
	typ, _ := flags.GetString("type")
	notNull, _ := flags.GetBool("not-null")
	def, _ := flags.GetString("default")
	comment, _ := flags.GetString("comment")

	if typ = strings.TrimSpace(typ); typ == "" {
		if notNull || flags.Changed("default") || comment != "" {
			return "", "", errors.New("--not-null, --default and --comment require --type")
		}
		return "/* TODO: 列定义 */", columnTodo, nil
	}

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
	return definition, "", nil
}

// addColumnSQL 拼出 ADD COLUMN 语句；--after 追加 MySQL 的列定位子句.
func addColumnSQL(cmd *cobra.Command, tableName, columnName string) (sql, todo string, err error) {
	flags := cmd.Flags()
	if err = rejectFlags(flags, "add_<column>_*", "unique", "index-name", "columns"); err != nil {
		return "", "", err
	}
	definition, todo, err := columnDefinition(flags)
	if err != nil {
		return "", "", err
	}

	sql = "ALTER TABLE `" + tableName + "` ADD COLUMN `" + columnName + "` " + definition
	if after, _ := flags.GetString("after"); after != "" {
		if err := validateIdentifier("column name", after); err != nil {
			return "", "", err
		}
		sql += " AFTER `" + after + "`"
	}
	return sql + onlineDDLClause, todo, nil
}

// modifyColumnSQL 拼出 MODIFY COLUMN 语句（整体替换列定义），带在线 DDL 策略.
func modifyColumnSQL(cmd *cobra.Command, tableName, columnName string) (sql, todo string, err error) {
	flags := cmd.Flags()
	if err = rejectFlags(flags, "modify_*", "unique", "index-name", "columns", "after"); err != nil {
		return "", "", err
	}
	definition, todo, err := columnDefinition(flags)
	if err != nil {
		return "", "", err
	}
	// 不预填在线 DDL 策略：MODIFY COLUMN 改类型时 ALGORITHM=INPLACE 多数不被支持
	// （缩短长度、跨类型转换都要求 COPY），预填会让生成的 SQL 直接报错.
	// 策略由使用者按实际变更决定，migrate lint 的 missing_online_ddl 会提醒.
	return fmt.Sprintf("ALTER TABLE `%s` MODIFY COLUMN `%s` %s",
		tableName, columnName, definition), todo, nil
}

// dropColumnSQL 拼出 DROP COLUMN 语句.
func dropColumnSQL(tableName, columnName string) string {
	return fmt.Sprintf("ALTER TABLE `%s` DROP COLUMN `%s`%s", tableName, columnName, onlineDDLClause)
}

// irreversibleDown 是删除类迁移未给出重建定义时的 down 实现.
func irreversibleDown(object, fileName string) string {
	return fmt.Sprintf("\t\treturn migration.Irreversible(\"fill in the %s recreation for %s manually\")\n", object, fileName)
}

func parseMigrationName(arg, explicitTable string) (action, objectName, tableName, columnName string, err error) {
	action, rest, found := strings.Cut(arg, "_")
	if !found {
		return "", "", "", "", fmt.Errorf("invalid migration name: %s", arg)
	}

	switch action {
	case "add", "drop", "update", "create", "modify":
	default:
		return "", "", "", "", fmt.Errorf("invalid action: %s (expected: add, drop, update, create, modify)", action)
	}

	// rest 形如 <body>_table，先剥掉固定后缀.
	body, found := strings.CutSuffix(rest, "_table")
	if !found {
		return "", "", "", "", fmt.Errorf("invalid migration suffix in %s (expected: *_table)", arg)
	}

	switch action {
	case "add":
		target, table, err := cutSeparator(body, "_to_", arg, explicitTable)
		if err != nil {
			return "", "", "", "", err
		}
		tableName = table
		switch {
		case strings.HasPrefix(target, "unique_index_"):
			// 与 add_index_* 表达同一件事，不提供第二种写法；报错指引而非静默当成列名.
			return "", "", "", "", fmt.Errorf(
				"invalid add migration name: %s (use add_index_<column>_to_<table>_table with --unique for a unique index)", arg)
		case strings.HasPrefix(target, "index_"):
			columnName = strings.TrimPrefix(target, "index_")
			objectName = "index"
		default:
			columnName = target
			objectName = "column"
		}
		if columnName == "" {
			return "", "", "", "", fmt.Errorf("could not parse column name from: %s", arg)
		}
	case "drop":
		if !strings.Contains(body, "_from_") {
			tableName = body
			objectName = "table"
			break
		}
		target, table, err := cutSeparator(body, "_from_", arg, explicitTable)
		if err != nil {
			return "", "", "", "", err
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
	case "modify":
		column, table, err := cutSeparator(body, "_of_", arg, explicitTable)
		if err != nil {
			return "", "", "", "", err
		}
		columnName = column
		tableName = table
		objectName = "column"
	case "create", "update":
		tableName = body
		objectName = "table"
	}

	if err := validateIdentifier("table name", tableName); err != nil {
		return "", "", "", "", fmt.Errorf("%s in %s", err, arg)
	}
	// create/update/drop table 没有列名；其余形态的列名此前已校验非空.
	if columnName != "" {
		if err := validateIdentifier("column name", columnName); err != nil {
			return "", "", "", "", fmt.Errorf("%s in %s", err, arg)
		}
	}

	return action, objectName, tableName, columnName, nil
}

// cutSeparator 按 sep 把迁移名主体切成"目标 + 表名".
// sep 出现多次时无法判断哪一段是表名——列名可能自带分隔符（reply_to_id），
// 表名同样可能（order_to_shipment）——任何一种猜法都会在另一种场景静默切错，
// 故直接报错并指引用 --table 显式消歧；给了 --table 就按它剥离后缀，不再猜.
func cutSeparator(body, sep, arg, explicitTable string) (target, table string, err error) {
	if explicitTable != "" {
		prefix, ok := strings.CutSuffix(body, sep+explicitTable)
		if !ok {
			return "", "", fmt.Errorf(
				"migration name %s does not end with %q before the table suffix; --table %s does not match", arg, sep, explicitTable)
		}
		if prefix == "" {
			return "", "", fmt.Errorf("could not parse the target name from: %s", arg)
		}
		return prefix, explicitTable, nil
	}

	switch strings.Count(body, sep) {
	case 0:
		return "", "", fmt.Errorf("invalid migration name: %s (expected <action>_<name>%s<table>_table)", arg, sep)
	case 1:
		target, table, _ = strings.Cut(body, sep)
		return target, table, nil
	default:
		return "", "", fmt.Errorf(
			"ambiguous migration name %s: %q appears more than once, so the table name cannot be inferred; pass --table <table> to disambiguate", arg, sep)
	}
}

// identifierPattern 是拼入 SQL 的表名、列名、索引名白名单.
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// maxIdentifierLen 是 MySQL 标识符长度上限；超长的名字生成出来也执行不了，生成期即拒绝.
const maxIdentifierLen = 64

// validateIdentifier 校验将被拼入 SQL 的标识符：非法字符（空格、反引号、分号等）
// 会产出编译不过或执行必错的迁移文件，生成期直接报错优于静默落盘.
func validateIdentifier(kind, name string) error {
	if !identifierPattern.MatchString(name) {
		return fmt.Errorf("invalid %s %q: only letters, digits and underscores are allowed, and it must not start with a digit", kind, name)
	}
	if len(name) > maxIdentifierLen {
		return fmt.Errorf("invalid %s %q: %d chars exceeds the MySQL identifier limit of %d", kind, name, len(name), maxIdentifierLen)
	}
	return nil
}

// migrationStubName 按 action 与对象类型选择模板；action 已由 parseMigrationName 校验.
func migrationStubName(action, objectName string) string {
	switch action {
	case "create":
		return "migration_create"
	case "add":
		if objectName == "index" {
			return "addindex"
		}
		return "migration_add"
	case "modify":
		return "modifycolumn"
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
