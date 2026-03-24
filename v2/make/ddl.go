package make

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gtkit/migrate/v2/console"
	"github.com/gtkit/stringx"
	"github.com/spf13/cobra"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var CmdMakeDDL = &cobra.Command{
	Use:   "ddl [model ...]",
	Short: "Generate strict create table DDL for registered GORM models",
	Args:  cobra.ArbitraryArgs,
	RunE:  runMakeDDL,
}

func init() {
	CmdMakeDDL.Flags().Bool("all", false, "Generate DDL for all registered models")
	CmdMakeDDL.Flags().Bool("force", false, "Overwrite existing DDL files")
}

func runMakeDDL(cmd *cobra.Command, args []string) error {
	cfg := resolveConfig(cmd)
	if cfg.DB == nil {
		return fmt.Errorf("make ddl requires a database connection; call migrate.Setup with a valid *gorm.DB")
	}
	if len(cfg.DDLModels) == 0 {
		return fmt.Errorf("no DDL models registered; use migrate.WithDDLModels(...) during Setup")
	}

	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}
	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return err
	}

	targets, err := resolveDDLTargets(cfg.DB, cfg.DDLModels, args, all)
	if err != nil {
		return err
	}

	mode := writeFailIfExists
	if force {
		mode = writeOverwrite
	}

	for _, target := range targets {
		sql, err := GenerateCreateTableDDL(cfg.DB, target.Value)
		if err != nil {
			return fmt.Errorf("generate ddl for %s: %w", target.StructName, err)
		}

		filePath := ddlFilePath(cfg, target.TableName)
		if err := writeGeneratedFile(filePath, []byte(sql), mode); err != nil {
			return err
		}
		console.Success(fmt.Sprintf("[%s] generated for %s (%s).", filepath.Clean(filePath), target.StructName, target.TableName))
	}

	return nil
}

// GenerateCreateTableDDL 基于 GORM dry-run 输出严格的建表 SQL。
func GenerateCreateTableDDL(db *gorm.DB, model any) (string, error) {
	if db == nil {
		return "", fmt.Errorf("database connection is required")
	}
	if model == nil {
		return "", fmt.Errorf("model is required")
	}

	capture := &ddlCaptureLogger{seen: make(map[string]struct{})}
	dryRunDB := db.Session(&gorm.Session{
		DryRun: true,
		Logger: capture,
	})

	if err := dryRunDB.Migrator().CreateTable(model); err != nil {
		return "", err
	}
	if len(capture.statements) == 0 {
		return "", fmt.Errorf("gorm did not emit any ddl statements")
	}

	return strings.Join(capture.statements, "\n\n"), nil
}

type ddlCaptureLogger struct {
	statements []string
	seen       map[string]struct{}
}

func (l *ddlCaptureLogger) LogMode(gormlogger.LogLevel) gormlogger.Interface {
	return l
}

func (l *ddlCaptureLogger) Info(context.Context, string, ...interface{})  {}
func (l *ddlCaptureLogger) Warn(context.Context, string, ...interface{})  {}
func (l *ddlCaptureLogger) Error(context.Context, string, ...interface{}) {}

func (l *ddlCaptureLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	sql = normalizeDDLStatement(sql)
	if sql == "" {
		return
	}
	if _, exists := l.seen[sql]; exists {
		return
	}
	l.seen[sql] = struct{}{}
	l.statements = append(l.statements, sql)
}

func normalizeDDLStatement(sql string) string {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return ""
	}

	upper := strings.ToUpper(sql)
	validPrefixes := []string{
		"CREATE ",
		"ALTER ",
		"COMMENT ",
	}
	if !slices.ContainsFunc(validPrefixes, func(prefix string) bool {
		return strings.HasPrefix(upper, prefix)
	}) {
		return ""
	}

	if !strings.HasSuffix(sql, ";") {
		sql += ";"
	}
	return sql
}

type ddlTarget struct {
	Aliases    []string
	StructName string
	TableName  string
	Value      any
}

func resolveDDLTargets(db *gorm.DB, models []any, names []string, all bool) ([]ddlTarget, error) {
	targets, err := buildDDLTargets(db, models)
	if err != nil {
		return nil, err
	}

	if all {
		return targets, nil
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("please specify at least one model name, or use --all")
	}

	lookup := make(map[string]ddlTarget, len(targets))
	for _, target := range targets {
		for _, alias := range target.Aliases {
			lookup[alias] = target
		}
	}

	selected := make([]ddlTarget, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		key := normalizeDDLName(name)
		target, ok := lookup[key]
		if !ok {
			return nil, fmt.Errorf("model %q is not registered for DDL generation", name)
		}
		identity := target.StructName + ":" + target.TableName
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		selected = append(selected, target)
	}

	slices.SortFunc(selected, func(a, b ddlTarget) int {
		return strings.Compare(a.TableName, b.TableName)
	})
	return selected, nil
}

func buildDDLTargets(db *gorm.DB, models []any) ([]ddlTarget, error) {
	targets := make([]ddlTarget, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}

		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(model); err != nil {
			return nil, fmt.Errorf("parse ddl model: %w", err)
		}

		structName := stmt.Schema.Name
		tableName := stmt.Schema.Table
		aliases := []string{
			normalizeDDLName(structName),
			normalizeDDLName(stringx.ToSnake(structName)),
			normalizeDDLName(tableName),
			normalizeDDLName(stringx.Singular(tableName)),
		}

		targets = append(targets, ddlTarget{
			Aliases:    compactStrings(aliases),
			StructName: structName,
			TableName:  tableName,
			Value:      model,
		})
	}

	slices.SortFunc(targets, func(a, b ddlTarget) int {
		return strings.Compare(a.TableName, b.TableName)
	})
	return targets, nil
}

func normalizeDDLName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "-", "_")
	name = strings.ReplaceAll(name, " ", "_")
	return strings.ToLower(name)
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func modelTypeName(value any) string {
	if value == nil {
		return ""
	}
	typ := reflect.TypeOf(value)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ.Name()
}
