package migration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	db := m.DB.WithContext(ctx)
	if !db.Migrator().HasTable(&Migration{}) {
		return nil, nil
	}

	var records []Migration
	if err := db.Find(&records).Error; err != nil {
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
		if aw, bw := weight(a.Severity), weight(b.Severity); aw != bw {
			return aw - bw
		}
		if cmp := strings.Compare(a.Name, b.Name); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Code, b.Code)
	})
}
