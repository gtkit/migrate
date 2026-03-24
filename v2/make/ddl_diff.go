package make

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// ErrDDLDiffFound 表示当前模型生成的 DDL 与已落盘文件存在漂移。
var ErrDDLDiffFound = errors.New("ddl drift detected")

type ddlDiff struct {
	Target   ddlTarget
	FilePath string
	Missing  bool
	Diff     string
}

var CmdMakeDDLDiff = &cobra.Command{
	Use:   "diff [model ...]",
	Short: "Compare generated strict DDL with existing DDL files",
	Args:  cobra.ArbitraryArgs,
	RunE:  runMakeDDLDiff,
}

func init() {
	CmdMakeDDLDiff.Flags().Bool("all", false, "Diff all registered models")
	CmdMakeDDL.AddCommand(CmdMakeDDLDiff)
}

func runMakeDDLDiff(cmd *cobra.Command, args []string) error {
	cfg := resolveConfig(cmd)
	if cfg.DB == nil {
		return fmt.Errorf("make ddl diff requires a database connection; call migrate.Setup with a valid *gorm.DB")
	}
	if len(cfg.DDLModels) == 0 {
		return fmt.Errorf("no DDL models registered; use migrate.WithDDLModels(...) during Setup")
	}

	all, err := cmd.Flags().GetBool("all")
	if err != nil {
		return err
	}

	targets, err := resolveDDLTargets(cfg.DB, cfg.DDLModels, args, all)
	if err != nil {
		return err
	}

	diffs, err := diffDDLTargets(cfg, targets)
	if err != nil {
		return err
	}
	if len(diffs) == 0 {
		cmd.Println("DDL diff passed. Existing SQL files are up to date.")
		return nil
	}

	for _, diff := range diffs {
		cmd.Printf("DDL drift detected for %s (%s)\n", diff.Target.StructName, diff.Target.TableName)
		cmd.Printf("File: %s\n", diff.FilePath)
		if diff.Missing {
			cmd.Println("Status: missing DDL file")
		}
		cmd.Println(diff.Diff)
		cmd.Println()
	}

	return fmt.Errorf("%w: %d file(s) differ", ErrDDLDiffFound, len(diffs))
}

func diffDDLTargets(cfg Config, targets []ddlTarget) ([]ddlDiff, error) {
	diffs := make([]ddlDiff, 0, len(targets))
	for _, target := range targets {
		expected, err := GenerateCreateTableDDL(cfg.DB, target.Value)
		if err != nil {
			return nil, fmt.Errorf("generate ddl for %s: %w", target.StructName, err)
		}

		filePath := ddlFilePath(cfg, target.TableName)
		current, err := os.ReadFile(filePath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				diffs = append(diffs, ddlDiff{
					Target:   target,
					FilePath: filePath,
					Missing:  true,
					Diff:     renderDDLTextDiff("", expected),
				})
				continue
			}
			return nil, fmt.Errorf("read existing ddl %s: %w", filePath, err)
		}

		if normalizeDDLText(string(current)) == normalizeDDLText(expected) {
			continue
		}

		diffs = append(diffs, ddlDiff{
			Target:   target,
			FilePath: filePath,
			Diff:     renderDDLTextDiff(string(current), expected),
		})
	}

	return diffs, nil
}

func normalizeDDLText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return text + "\n"
}

func renderDDLTextDiff(existing, generated string) string {
	existing = normalizeDDLText(existing)
	generated = normalizeDDLText(generated)

	existingLines := splitDiffLines(existing)
	generatedLines := splitDiffLines(generated)
	ops := diffLines(existingLines, generatedLines)

	var builder strings.Builder
	builder.WriteString("--- existing\n")
	builder.WriteString("+++ generated\n")
	for _, op := range ops {
		builder.WriteString(op.prefix)
		builder.WriteString(op.line)
		builder.WriteString("\n")
	}
	return strings.TrimRight(builder.String(), "\n")
}

type diffOp struct {
	prefix string
	line   string
}

func splitDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func diffLines(existing, generated []string) []diffOp {
	dp := make([][]int, len(existing)+1)
	for i := range dp {
		dp[i] = make([]int, len(generated)+1)
	}

	for i := len(existing) - 1; i >= 0; i-- {
		for j := len(generated) - 1; j >= 0; j-- {
			if existing[i] == generated[j] {
				dp[i][j] = dp[i+1][j+1] + 1
				continue
			}
			dp[i][j] = max(dp[i+1][j], dp[i][j+1])
		}
	}

	ops := make([]diffOp, 0, len(existing)+len(generated))
	i, j := 0, 0
	for i < len(existing) && j < len(generated) {
		switch {
		case existing[i] == generated[j]:
			ops = append(ops, diffOp{prefix: " ", line: existing[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{prefix: "-", line: existing[i]})
			i++
		default:
			ops = append(ops, diffOp{prefix: "+", line: generated[j]})
			j++
		}
	}

	for ; i < len(existing); i++ {
		ops = append(ops, diffOp{prefix: "-", line: existing[i]})
	}
	for ; j < len(generated); j++ {
		ops = append(ops, diffOp{prefix: "+", line: generated[j]})
	}

	return ops
}
