package file

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileOperations(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "migration.test.go")
	if Exists(path) {
		t.Fatal("file should be absent")
	}
	if err := Put([]byte("migration"), path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing directory error, got %v", err)
	}
	if err := CreateDirIfNotExists(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CreateDirIfNotExists(dir); err != nil {
		t.Fatal(err)
	}
	if err := CreateDirIfNotExists(filepath.Join(dir, "default")); err != nil {
		t.Fatal(err)
	}
	if err := Put([]byte("migration"), path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "migration" || !Exists(path) {
		t.Fatalf("generated file content=%q err=%v", content, err)
	}
	if got := FileNameWithoutExtension(path); got != filepath.Join(dir, "migration.test") {
		t.Fatalf("unexpected basename: %s", got)
	}
	if err := CreateDirIfNotExists(filepath.Join(path, "child")); err == nil {
		t.Fatal("cannot create a directory inside a regular file")
	}
}
