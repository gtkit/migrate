package migration

import "fmt"

// Logger 迁移日志接口.
// 默认使用 fmt 输出到 stdout，生产环境可注入 zerolog/zap 等结构化日志实现.
type Logger interface {
	// Info 记录普通信息.
	Info(msg string, keysAndValues ...any)
	// Warn 记录警告信息.
	Warn(msg string, keysAndValues ...any)
	// Error 记录错误信息.
	Error(msg string, keysAndValues ...any)
}

// defaultLogger 默认日志实现，输出到 stdout.
type defaultLogger struct{}

func (l *defaultLogger) Info(msg string, keysAndValues ...any) {
	if len(keysAndValues) > 0 {
		fmt.Printf("[INFO]  %-50s %s\n", msg, formatKV(keysAndValues))
	} else {
		fmt.Printf("[INFO]  %s\n", msg)
	}
}

func (l *defaultLogger) Warn(msg string, keysAndValues ...any) {
	if len(keysAndValues) > 0 {
		fmt.Printf("[WARN]  %-50s %s\n", msg, formatKV(keysAndValues))
	} else {
		fmt.Printf("[WARN]  %s\n", msg)
	}
}

func (l *defaultLogger) Error(msg string, keysAndValues ...any) {
	if len(keysAndValues) > 0 {
		fmt.Printf("[ERROR] %-50s %s\n", msg, formatKV(keysAndValues))
	} else {
		fmt.Printf("[ERROR] %s\n", msg)
	}
}

// formatKV 将 key-value 对格式化为字符串.
func formatKV(keysAndValues []any) string {
	if len(keysAndValues) == 0 {
		return ""
	}

	var result string
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		if i > 0 {
			result += " "
		}
		result += fmt.Sprintf("%v=%v", keysAndValues[i], keysAndValues[i+1])
	}
	// 奇数个参数，最后一个单独输出
	if len(keysAndValues)%2 != 0 {
		if result != "" {
			result += " "
		}
		result += fmt.Sprintf("%v", keysAndValues[len(keysAndValues)-1])
	}
	return result
}

// NopLogger 空日志实现，不输出任何内容.
// 适用于测试或不需要日志的场景.
type NopLogger struct{}

func (l *NopLogger) Info(_ string, _ ...any)  {}
func (l *NopLogger) Warn(_ string, _ ...any)  {}
func (l *NopLogger) Error(_ string, _ ...any) {}
