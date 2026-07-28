// Package file 文件操作辅助函数.
package file

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Put 将数据存入文件.
func Put(data []byte, to string) error {
	return os.WriteFile(to, data, 0o644)
}

// Exists 判断文件是否存在.
// 注意：仅在能确认「不存在」时返回 false；无法确认（如权限错误）时返回 true.
func Exists(fileToCheck string) bool {
	_, err := os.Stat(fileToCheck)
	return !errors.Is(err, fs.ErrNotExist)
}

// FileNameWithoutExtension 去除文件扩展名.
//
// Deprecated: 本包内已无使用场景，仅为兼容保留；请直接使用
// strings.TrimSuffix(name, filepath.Ext(name)).
func FileNameWithoutExtension(fileName string) string {
	return strings.TrimSuffix(fileName, filepath.Ext(fileName))
}

// CreateDirIfNotExists 创建目录（若不存在），幂等.
// perm 可选，默认 os.ModePerm；仅第一个值生效.
func CreateDirIfNotExists(dirname string, perm ...os.FileMode) error {
	p := os.ModePerm
	if len(perm) > 0 {
		p = perm[0]
	}
	return os.MkdirAll(dirname, p)
}
