package command

import "testing"

// TestCommandsReturnsAll 覆盖 Commands 聚合函数.
func TestCommandsReturnsAll(t *testing.T) {
	cmds := Commands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands (make, migrate), got %d", len(cmds))
	}
	for _, c := range cmds {
		if c == nil || c.Use == "" {
			t.Fatalf("invalid command in aggregated set: %#v", c)
		}
	}
}
