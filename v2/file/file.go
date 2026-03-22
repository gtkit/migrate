// Package file 文件操作辅助函数.
package file

import (
	"os"
	"path/filepath"
	"strings"
)

// Put 将数据存入文件.
func Put(data []byte, to string) error {
	return os.WriteFile(to, data, 0o644)
}

// Exists 判断文件是否存在.
func Exists(fileToCheck string) bool {
	_, err := os.Stat(fileToCheck)
	return !os.IsNotExist(err)
}

// FileNameWithoutExtension 去除文件扩展名.
func FileNameWithoutExtension(fileName string) string {
	return strings.TrimSuffix(fileName, filepath.Ext(fileName))
}

// CreateDirIfNotExists 创建目录（若不存在）.
func CreateDirIfNotExists(dirname string, perm ...os.FileMode) error {
	if _, err := os.Stat(dirname); err != nil {
		if os.IsNotExist(err) {
			p := os.ModePerm
			if len(perm) > 0 {
				p = perm[0]
			}
			return os.MkdirAll(dirname, p)
		}
		return err
	}
	return nil
}
