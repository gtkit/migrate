package migration

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	// ErrLintFailed 表示 lint 发现了至少一个错误级问题。
	ErrLintFailed = errors.New("migration lint failed")

	migrationNamePattern = regexp.MustCompile(`^\d{4}_\d{2}_\d{2}_\d{6}_.+$`)
)

// LintSeverity 表示 lint 问题级别。
type LintSeverity string

const (
	LintSeverityError   LintSeverity = "error"
	LintSeverityWarning LintSeverity = "warning"
)

// LintIssue 表示一条 lint 结果。
type LintIssue struct {
	Severity LintSeverity
	Code     string
	Name     string
	Message  string
}

// LintOptions 控制 lint 行为。
type LintOptions struct {
	SkipDatabase bool
}

// LintReport 是 migration lint 的完整结果。
type LintReport struct {
	Issues []LintIssue
}

// ErrorCount 返回 error 级问题数量。
func (r LintReport) ErrorCount() int {
	count := 0
	for _, issue := range r.Issues {
		if issue.Severity == LintSeverityError {
			count++
		}
	}
	return count
}

// WarningCount 返回 warning 级问题数量。
func (r LintReport) WarningCount() int {
	count := 0
	for _, issue := range r.Issues {
		if issue.Severity == LintSeverityWarning {
			count++
		}
	}
	return count
}

// HasErrors 返回是否存在 error 级问题。
func (r LintReport) HasErrors() bool {
	return r.ErrorCount() > 0
}

// HasWarnings 返回是否存在 warning 级问题。
func (r LintReport) HasWarnings() bool {
	return r.WarningCount() > 0
}

type diskMigrationFile struct {
	Name      string
	Path      string
	Timestamp string
	Content   string
}

// Lint 检查 migration 目录、registry 与数据库记录的一致性。
func (m *Migrator) Lint(ctx context.Context, opts LintOptions) (LintReport, error) {
	diskFiles, err := m.readDiskMigrationFiles()
	if err != nil {
		return LintReport{}, err
	}

	report := LintReport{}
	diskMap := make(map[string]diskMigrationFile, len(diskFiles))
	timestampMap := make(map[string][]string)
	for _, file := range diskFiles {
		diskMap[file.Name] = file
		timestampMap[file.Timestamp] = append(timestampMap[file.Timestamp], file.Name)
	}

	registryFiles := m.registry.All()
	registryMap := make(map[string]MigrationFile, len(registryFiles))
	for _, file := range registryFiles {
		registryMap[file.FileName] = file
	}

	for _, file := range diskFiles {
		if _, ok := registryMap[file.Name]; !ok {
			report.Issues = append(report.Issues, LintIssue{
				Severity: LintSeverityError,
				Code:     "unregistered_file",
				Name:     file.Name,
				Message:  "migration file exists on disk but is not registered; did you forget to build/import it?",
			})
		}
		if strings.Contains(file.Content, "Irreversible(") {
			report.Issues = append(report.Issues, LintIssue{
				Severity: LintSeverityWarning,
				Code:     "irreversible_migration",
				Name:     file.Name,
				Message:  "migration declares manual down logic is required",
			})
		}
		// ALGORITHM/LOCK 在线 DDL 子句是 MySQL 专属语法，其他方言不适用、不检查.
		report.Issues = append(report.Issues, lintFileContent(file, m.dbType == DBTypeMySQL)...)
	}

	for _, file := range registryFiles {
		if _, ok := diskMap[file.FileName]; !ok {
			report.Issues = append(report.Issues, LintIssue{
				Severity: LintSeverityError,
				Code:     "missing_file",
				Name:     file.FileName,
				Message:  "migration is registered in memory but the file is missing on disk",
			})
		}
		if file.Up == nil {
			report.Issues = append(report.Issues, LintIssue{
				Severity: LintSeverityError,
				Code:     "missing_up",
				Name:     file.FileName,
				Message:  "migration has no up function",
			})
		}
		if file.Down == nil {
			report.Issues = append(report.Issues, LintIssue{
				Severity: LintSeverityWarning,
				Code:     "missing_down",
				Name:     file.FileName,
				Message:  "migration has no down function; rollback is impossible",
			})
		}
	}

	for _, name := range m.registry.Duplicates() {
		report.Issues = append(report.Issues, LintIssue{
			Severity: LintSeverityError,
			Code:     "duplicate_registration",
			Name:     name,
			Message:  "migration name registered more than once; two packages may register the same name (first registration wins at runtime)",
		})
	}

	for timestamp, names := range timestampMap {
		if len(names) < 2 {
			continue
		}
		slices.Sort(names)
		report.Issues = append(report.Issues, LintIssue{
			Severity: LintSeverityWarning,
			Code:     "duplicate_timestamp",
			Name:     strings.Join(names, ", "),
			Message:  fmt.Sprintf("multiple migrations share timestamp prefix %s; ordering depends on suffix", timestamp),
		})
	}

	if !opts.SkipDatabase && m.DB != nil {
		issues, err := m.lintDatabase(ctx, diskMap, registryMap)
		if err != nil {
			return LintReport{}, err
		}
		report.Issues = append(report.Issues, issues...)
	}

	sortLintIssues(report.Issues)
	return report, nil
}

func (m *Migrator) lintDatabase(ctx context.Context, diskMap map[string]diskMigrationFile, registryMap map[string]MigrationFile) ([]LintIssue, error) {
	if !m.DB.WithContext(ctx).Migrator().HasTable(m.tableName) {
		return nil, nil
	}

	var records []Migration
	if err := m.records(ctx).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("query migration records: %w", err)
	}

	issues := make([]LintIssue, 0, len(records))
	for _, record := range records {
		if _, ok := diskMap[record.Migration]; !ok {
			issues = append(issues, LintIssue{
				Severity: LintSeverityError,
				Code:     "applied_missing_file",
				Name:     record.Migration,
				Message:  "migration was applied in database but no matching file exists on disk",
			})
		}
		if _, ok := registryMap[record.Migration]; !ok {
			issues = append(issues, LintIssue{
				Severity: LintSeverityError,
				Code:     "applied_unregistered",
				Name:     record.Migration,
				Message:  "migration was applied in database but is not registered in the current binary",
			})
		}
	}

	return issues, nil
}

func (m *Migrator) readDiskMigrationFiles() ([]diskMigrationFile, error) {
	entries, err := os.ReadDir(m.Folder)
	if err != nil {
		return nil, fmt.Errorf("read migration dir %s: %w", m.Folder, err)
	}

	result := make([]diskMigrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".go" {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".go")
		if !migrationNamePattern.MatchString(name) {
			continue
		}

		path := filepath.Join(m.Folder, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read migration file %s: %w", path, err)
		}

		result = append(result, diskMigrationFile{
			Name:      name,
			Path:      path,
			Timestamp: name[:17],
			Content:   string(content),
		})
	}

	slices.SortFunc(result, func(a, b diskMigrationFile) int {
		return strings.Compare(a.Name, b.Name)
	})
	return result, nil
}

// lintFileContent 对单个迁移文件做内容级检查：结构性问题报 error，风险性问题报 warning.
// checkOnlineDDL 为 true 时才检查在线 DDL 策略（MySQL 专属规则）.
func lintFileContent(file diskMigrationFile, checkOnlineDDL bool) []LintIssue {
	var issues []LintIssue

	if strings.Contains(file.Content, "TODO") {
		issues = append(issues, LintIssue{
			Severity: LintSeverityError,
			Code:     "unfilled_placeholder",
			Name:     file.Name,
			Message:  "migration still contains a TODO placeholder; complete it before running",
		})
	}

	if strings.Contains(file.Content, "AutoMigrate(") {
		issues = append(issues, LintIssue{
			Severity: LintSeverityError,
			Code:     "automigrate_used",
			Name:     file.Name,
			Message:  "migration uses AutoMigrate; use explicit, self-contained SQL instead",
		})
	}

	for _, imp := range migrationImports(file.Content) {
		if !isSelfContainedImport(imp) {
			issues = append(issues, LintIssue{
				Severity: LintSeverityError,
				Code:     "non_self_contained",
				Name:     file.Name,
				Message:  fmt.Sprintf("migration imports %q; migrations must be self-contained (only stdlib, gorm, and the migrate package)", imp),
			})
		}
	}

	// 在线 DDL / 危险 DDL 只看真实 SQL 字符串字面量，排除注释干扰
	// （注释里的 ALGORITHM/LOCK 关键词不能算作已标注在线 DDL 策略）.
	missingOnlineDDL, destructive := lintMigrationSQL(file.Content)
	if missingOnlineDDL && checkOnlineDDL {
		issues = append(issues, LintIssue{
			Severity: LintSeverityWarning,
			Code:     "missing_online_ddl",
			Name:     file.Name,
			Message:  "raw ALTER TABLE without an online-DDL strategy (needs both ALGORITHM and LOCK); large tables may block",
		})
	}
	if destructive {
		issues = append(issues, LintIssue{
			Severity: LintSeverityWarning,
			Code:     "destructive_migration",
			Name:     file.Name,
			Message:  "migration contains destructive raw DDL (DROP TABLE/DROP DATABASE/TRUNCATE)",
		})
	}

	return issues
}

// lintMigrationSQL 用 AST 提取迁移文件中的字符串字面量（真实 SQL），逐条判定：
// 是否存在缺在线 DDL 策略的 ALTER TABLE，以及是否存在危险 DDL.
// 只看字符串字面量，注释里的关键词不参与判定；解析失败时退回全文文本（降级）.
func lintMigrationSQL(content string) (missingOnlineDDL, destructive bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", content, 0)
	if err != nil {
		return sqlDDLChecks(content)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		mo, d := sqlDDLChecks(s)
		missingOnlineDDL = missingOnlineDDL || mo
		destructive = destructive || d
		return true
	})
	return missingOnlineDDL, destructive
}

// sqlCommentPattern 匹配 SQL 注释：块注释 /* */、行注释 -- 与 MySQL # 到行尾.
var sqlCommentPattern = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*|#[^\n]*`)

// 在线 DDL / 危险 DDL 用真实子句/词界匹配，避免误判列名、字符串值或含下划线的标识符.
var (
	alterTablePattern      = regexp.MustCompile(`(?is)alter\s+table`)
	algorithmClausePattern = regexp.MustCompile(`(?i)algorithm\s*=`)
	lockClausePattern      = regexp.MustCompile(`(?i)lock\s*=`)
	dropTablePattern       = regexp.MustCompile(`(?i)drop\s+table`)
	dropDatabasePattern    = regexp.MustCompile(`(?i)drop\s+(database|schema)`)
	truncatePattern        = regexp.MustCompile(`(?i)\btruncate\b`)
)

// sqlStringPattern 匹配 SQL 字符串字面量：单引号（含成对单引号转义与反斜杠转义）与双引号.
var sqlStringPattern = regexp.MustCompile(`'(?:''|\\.|[^'])*'|"(?:""|\\.|[^"])*"`)

// stripSQLComments 去除 SQL 注释，避免注释里的 ALGORITHM/LOCK 等关键词误导判定.
func stripSQLComments(sql string) string {
	return sqlCommentPattern.ReplaceAllString(sql, " ")
}

// stripSQLStrings 屏蔽 SQL 字符串字面量内容，避免引号内的关键词被当成真实子句.
func stripSQLStrings(sql string) string {
	return sqlStringPattern.ReplaceAllString(sql, " ")
}

// sqlDDLChecks 对单段 SQL 文本判定在线 DDL 策略缺失与危险 DDL.
// 缺在线 DDL 策略：含 ALTER TABLE 但未同时标注 ALGORITHM 与 LOCK.
// 判定前先剥离 SQL 注释——注释中的关键词不算作已标注策略.
func sqlDDLChecks(sql string) (missingOnlineDDL, destructive bool) {
	cleaned := stripSQLStrings(stripSQLComments(sql))
	if alterTablePattern.MatchString(cleaned) &&
		(!algorithmClausePattern.MatchString(cleaned) || !lockClausePattern.MatchString(cleaned)) {
		missingOnlineDDL = true
	}
	if dropTablePattern.MatchString(cleaned) || dropDatabasePattern.MatchString(cleaned) || truncatePattern.MatchString(cleaned) {
		destructive = true
	}
	return missingOnlineDDL, destructive
}

// migrationImports 解析迁移文件的 import 路径；解析失败返回空（其余文本检查照常进行）.
func migrationImports(content string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", content, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(f.Imports))
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		paths = append(paths, p)
	}
	return paths
}

// isSelfContainedImport 判定 import 是否属于迁移允许的自包含范围：
// 标准库（首段不含域名点）、gorm.io/*、github.com/gtkit/migrate/*.
func isSelfContainedImport(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	if !strings.Contains(first, ".") {
		return true // 标准库
	}
	return strings.HasPrefix(path, "gorm.io/") || strings.HasPrefix(path, "github.com/gtkit/migrate/")
}

func sortLintIssues(issues []LintIssue) {
	weight := func(severity LintSeverity) int {
		switch severity {
		case LintSeverityError:
			return 0
		case LintSeverityWarning:
			return 1
		default:
			return 2
		}
	}

	slices.SortFunc(issues, func(a, b LintIssue) int {
		return cmp.Or(
			cmp.Compare(weight(a.Severity), weight(b.Severity)),
			strings.Compare(a.Name, b.Name),
			strings.Compare(a.Code, b.Code),
		)
	})
}
