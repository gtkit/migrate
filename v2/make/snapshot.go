package make

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// defaultSnapshotFields 是 create 模板在未指定 --from-model 时的基础字段与占位.
const defaultSnapshotFields = "\tID        int64          `gorm:\"column:id;primaryKey;autoIncrement;comment:主键编码\"`\n" +
	"\tCreatedAt time.Time      `gorm:\"column:created_at;type:datetime;index;comment:创建时间\"`\n" +
	"\tUpdatedAt time.Time      `gorm:\"column:updated_at;type:datetime;index;comment:最后更新时间\"`\n" +
	"\tDeletedAt gorm.DeletedAt `gorm:\"column:deleted_at;type:datetime;index;comment:删除时间\"`\n\n" +
	"\t// TODO: 在此补全建表字段（可对照初始 model 定义，但不要 import 业务 model）。"

// snapshotSource 是渲染进 create 模板的快照字段与额外 import 行.
type snapshotSource struct {
	fields  string
	imports string
}

// defaultSnapshot 返回未指定 --from-model 时的默认快照源（仅需 time）.
func defaultSnapshot() snapshotSource {
	return snapshotSource{fields: defaultSnapshotFields, imports: "\t\"time\"\n"}
}

// snapshotFromModel 在已注册的 DDL model 中找到表名为 tableName 的 model，
// 反射其字段生成快照 struct 的字段源码与 import 行.
// 只取导出字段；匿名嵌入 struct 展平；gorm:"-" 跳过；tag 原样保留.
// model 只在生成时读取，生成后的文件是唯一事实来源.
func snapshotFromModel(db *gorm.DB, models []any, tableName string) (snapshotSource, error) {
	targets, err := buildDDLTargets(db, models)
	if err != nil {
		return snapshotSource{}, err
	}
	known := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.TableName == tableName {
			return renderSnapshot(target.Type)
		}
		known = append(known, target.TableName)
	}
	return snapshotSource{}, fmt.Errorf("--from-model: no registered DDL model has table %q (registered: %s); register it with WithDDLModels or name the migration after the model's table", tableName, strings.Join(known, ", "))
}

func renderSnapshot(t reflect.Type) (snapshotSource, error) {
	var (
		fields  []string
		imports = map[string]string{} // path -> alias（与 path.Base 相同时为空）
	)
	var walk func(t reflect.Type) error
	walk = func(t reflect.Type) error {
		for f := range t.Fields() {
			if !f.IsExported() && !f.Anonymous {
				continue
			}
			if strings.TrimSpace(f.Tag.Get("gorm")) == "-" {
				continue
			}
			ft := f.Type
			if f.Anonymous {
				if ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct && ft.PkgPath() != "" && !isSingleColumnEmbed(ft) {
					if err := walk(ft); err != nil {
						return err
					}
					continue
				}
			}
			collectImports(ft, imports)
			line := "\t" + f.Name + " " + typeString(ft)
			if f.Tag != "" {
				line += " " + tagLiteral(string(f.Tag))
			}
			fields = append(fields, line)
		}
		return nil
	}
	if t.Kind() != reflect.Struct {
		return snapshotSource{}, errors.New("--from-model: model must be a struct")
	}
	if err := walk(t); err != nil {
		return snapshotSource{}, err
	}
	if len(fields) == 0 {
		return snapshotSource{}, errors.New("--from-model: model has no exported fields")
	}

	delete(imports, "gorm.io/gorm") // 模板固定 import
	paths := slices.Sorted(maps.Keys(imports))
	// 标准库与第三方分组，与 gofmt 惯例一致.
	var std, third strings.Builder
	for _, p := range paths {
		b := &third
		if first, _, _ := strings.Cut(p, "/"); !strings.Contains(first, ".") {
			b = &std
		}
		b.WriteString("\t")
		if alias := imports[p]; alias != "" {
			b.WriteString(alias + " ")
		}
		b.WriteString(strconv.Quote(p) + "\n")
	}
	group := std.String()
	if third.Len() > 0 {
		if group != "" {
			group += "\n"
		}
		group += third.String()
	}
	return snapshotSource{fields: strings.Join(fields, "\n"), imports: group}, nil
}

// typeString 渲染字段类型源码；reflect 把 []byte 打印为 []uint8，按惯例还原.
func typeString(t reflect.Type) string {
	if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 && t.Elem().PkgPath() == "" {
		return "[]byte"
	}
	return t.String()
}

// isSingleColumnEmbed 判断匿名嵌入 struct 是否按 GORM 语义视为单列而非展平：
// 实现 driver.Valuer 的类型（如自定义时间/JSON 包装）是一个列，GORM 不会展平它.
func isSingleColumnEmbed(t reflect.Type) bool {
	return t.Implements(valuerType) || reflect.PointerTo(t).Implements(valuerType)
}

var valuerType = reflect.TypeFor[driver.Valuer]()

// collectImports 收集类型（穿透指针/切片/数组/map）涉及的命名类型包路径与别名.
func collectImports(t reflect.Type, imports map[string]string) {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			t = t.Elem()
			continue
		case reflect.Map:
			collectImports(t.Key(), imports)
			t = t.Elem()
			continue
		}
		break
	}
	pkgPath := t.PkgPath()
	if pkgPath == "" {
		return
	}
	// reflect 的 String() 形如 "pkgname.Type"，包名与路径末段不一致时需要别名.
	pkgName, _, _ := strings.Cut(t.String(), ".")
	alias := ""
	if pkgName != path.Base(pkgPath) {
		alias = pkgName
	}
	imports[pkgPath] = alias
}

// tagLiteral 把 struct tag 渲染为 Go 源码字面量：含反引号时退回双引号字面量.
func tagLiteral(tag string) string {
	if strings.Contains(tag, "`") {
		return strconv.Quote(tag)
	}
	return "`" + tag + "`"
}
