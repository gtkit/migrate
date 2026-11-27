// Package regularfile reads ordinary files relative to a directory.
package regularfile

import (
	"fmt"
	"os"
)

// Read reads a regular file in root and rejects symbolic links.
func Read(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return root.ReadFile(name)
}
