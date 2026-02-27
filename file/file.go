// Package file 文件操作辅助函数
package file

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// Put 将数据存入文件.
func Put(data []byte, to string) error {
	err := os.WriteFile(to, data, 0o644)
	if err != nil {
		return err
	}

	return nil
}

// Exists 判断文件是否存在.
func Exists(fileToCheck string) bool {
	if _, err := os.Stat(fileToCheck); os.IsNotExist(err) {
		return false
	}

	return true
}

// DirExists @function: DirExists
// @description: 文件目录是否存在
// @param: path string
// @return: bool, error.
func DirExists(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err == nil {
		if fi.IsDir() {
			return true, nil
		}

		return false, errors.New("存在同名文件")
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, err
}

func FileNameWithoutExtension(fileName string) string {
	return strings.TrimSuffix(fileName, filepath.Ext(fileName))
}

func Position(file *os.File) (int64, error) {
	if file == nil {
		return 0, errors.New("null fd when retrieving file position")
	}

	return file.Seek(0, io.SeekCurrent)
}

func CreateDirIfNotExists(dirname string, perm ...os.FileMode) error {
	if _, err := os.Stat(dirname); err != nil {
		if os.IsNotExist(err) {
			if len(perm) == 0 {
				perm = []os.FileMode{os.ModePerm}
			}
			return os.MkdirAll(dirname, perm[0])
		} else {
			return err
		}
	}

	return nil
}

func Del(filePath string) error {
	return os.RemoveAll(filePath)
}

// Move @description: 文件移动
// @param: src string, dst string(src: 源位置,绝对路径or相对路径, dst: 目标位置,绝对路径or相对路径,必须为文件夹)
// @return: err error.
func Move(src string, dst string) (err error) {
	if dst == "" {
		return nil
	}

	src, err = filepath.Abs(src)
	if err != nil {
		return err
	}

	dst, err = filepath.Abs(dst)
	if err != nil {
		return err
	}

	revoke := false
	dir := filepath.Dir(dst)
Redirect:
	_, err = os.Stat(dir)

	if err != nil {
		err = os.MkdirAll(dir, 0o755)
		if err != nil {
			return err
		}

		if !revoke {
			revoke = true

			goto Redirect
		}
	}

	return os.Rename(src, dst)
}
