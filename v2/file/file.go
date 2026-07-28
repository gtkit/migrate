// Package file 文件操作辅助函数.
package file

import (
	"errors"
	"io/fs"
	"os"
)

// Put 将数据存入文件.
func Put(data []byte, to string) error {
	return os.WriteFile(to, data, 0o644)
}

// Exists 判断文件是否存在.
func Exists(fileToCheck string) bool {
	_, err := os.Stat(fileToCheck)
	return !errors.Is(err, fs.ErrNotExist)
}

// CreateDirIfNotExists 创建目录（若不存在）.
func CreateDirIfNotExists(dirname string) error {
	if _, err := os.Stat(dirname); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return os.MkdirAll(dirname, os.ModePerm)
		}
		return err
	}
	return nil
}
