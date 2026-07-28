package console

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureOutput 临时替换 stdout/stderr，返回两条流上产生的内容.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	origOut, origErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()

	fn()

	_ = outW.Close()
	_ = errW.Close()
	outBytes, _ := io.ReadAll(outR)
	errBytes, _ := io.ReadAll(errR)
	return string(outBytes), string(errBytes)
}

// TestSuccessAndWarningGoToStdout 验证正常消息输出到 stdout.
func TestSuccessAndWarningGoToStdout(t *testing.T) {
	stdout, stderr := captureOutput(t, func() {
		Success("ok")
		Warning("careful")
	})
	if !strings.Contains(stdout, "ok") || !strings.Contains(stdout, "careful") {
		t.Fatalf("success/warning should go to stdout, got stdout=%q", stdout)
	}
	if stderr != "" {
		t.Fatalf("success/warning should not write to stderr, got %q", stderr)
	}
}

// TestErrorGoesToStderr 验证错误消息输出到 stderr（Exit/ExitIf 因调用 os.Exit 不在此测）.
func TestErrorGoesToStderr(t *testing.T) {
	stdout, stderr := captureOutput(t, func() {
		Error("boom")
	})
	if !strings.Contains(stderr, "boom") {
		t.Fatalf("error should go to stderr, got stderr=%q", stderr)
	}
	if stdout != "" {
		t.Fatalf("error should not write to stdout, got %q", stdout)
	}
}
