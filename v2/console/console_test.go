package console

import "testing"

// TestConsoleOutputsDoNotPanic 覆盖彩色输出辅助函数（Exit/ExitIf 因调用 os.Exit 不在此测）.
func TestConsoleOutputsDoNotPanic(t *testing.T) {
	Success("ok")
	Error("bad")
	Warning("warn")
}
