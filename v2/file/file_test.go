package file

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPutAndExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")

	if Exists(path) {
		t.Fatalf("file should not exist before Put")
	}
	if err := Put([]byte("hello"), path); err != nil {
		t.Fatalf("put: %v", err)
	}
	if !Exists(path) {
		t.Fatalf("file should exist after Put")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("unexpected content %q", data)
	}
}

func TestCreateDirIfNotExists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")

	if err := CreateDirIfNotExists(dir); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("dir should exist after create, err=%v", err)
	}

	// 已存在时应幂等成功.
	if err := CreateDirIfNotExists(dir); err != nil {
		t.Fatalf("create existing dir should succeed: %v", err)
	}
}
