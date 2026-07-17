package migrate

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestRequireForce 验证 requireForce 的两个分支.
func TestRequireForce(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().Bool("force", false, "")

	if err := requireForce(cmd, "danger"); err == nil {
		t.Fatalf("expected error when --force is absent")
	}

	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatalf("set force: %v", err)
	}
	if err := requireForce(cmd, "danger"); err != nil {
		t.Fatalf("expected no error when --force is set, got %v", err)
	}
}

// TestDestructiveRunFuncsRequireForce 验证 reset/refresh/fresh 未传 --force 时
// 直接返回错误并在触达数据库之前拒绝执行（app 未初始化仍不 panic，
// 证明 requireForce 在 newContext/newMigrator 之前短路）.
func TestDestructiveRunFuncsRequireForce(t *testing.T) {
	funcs := map[string]func(*cobra.Command, []string) error{
		"reset":   runReset,
		"refresh": runRefresh,
		"fresh":   runFresh,
	}

	for name, fn := range funcs {
		t.Run(name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().Bool("force", false, "")

			err := fn(cmd, nil)
			if err == nil {
				t.Fatalf("%s without --force should return an error", name)
			}
			if !strings.Contains(err.Error(), "--force") {
				t.Fatalf("%s error should mention --force, got: %v", name, err)
			}
		})
	}
}
