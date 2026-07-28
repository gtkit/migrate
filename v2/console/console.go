// Package console 命令行辅助方法.
package console

import (
	"fmt"
	"io"
	"os"

	"github.com/mgutz/ansi"
)

// Success 打印一条成功消息，绿色输出到 stdout.
func Success(msg string) {
	colorOut(os.Stdout, msg, "green")
}

// Error 打印一条报错消息，红色输出到 stderr.
func Error(msg string) {
	colorOut(os.Stderr, msg, "red")
}

// Warning 打印一条提示消息，黄色输出到 stdout.
func Warning(msg string) {
	colorOut(os.Stdout, msg, "yellow")
}

// Exit 打印一条报错消息，并退出 os.Exit(1).
// 注意：仅限 CLI 入口使用，库代码不应调用此方法.
func Exit(msg string) {
	Error(msg)
	os.Exit(1)
}

// ExitIf 语法糖，自带 err != nil 判断.
// 注意：仅限 CLI 入口使用，库代码不应调用此方法.
func ExitIf(err error) {
	if err != nil {
		Exit(err.Error())
	}
}

// colorOut 内部使用，设置高亮颜色.
func colorOut(w io.Writer, message, color string) {
	_, _ = fmt.Fprintln(w, ansi.Color(message, color))
}
