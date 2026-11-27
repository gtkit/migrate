package make

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"go/token"
	"maps"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
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
// 跳过未导出和 gorm:"-" 字段，保留 GORM 标签及嵌入字段的配置.
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
	if t.Kind() != reflect.Struct {
		return snapshotSource{}, errors.New("--from-model: model must be a struct")
	}
	imports := map[string]string{} // path -> alias（与 path.Base 相同时为空）
	var walk func(t reflect.Type) ([]string, error)
	walk = func(t reflect.Type) ([]string, error) {
		var fields []string
		for f := range t.Fields() {
			if !f.IsExported() {
				continue
			}
			gormTag := strings.TrimSpace(f.Tag.Get("gorm"))
			if gormTag == "-" {
				continue
			}
			ft := f.Type
			embedded := ft
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			_, explicitEmbed := schema.ParseTagSetting(gormTag, ";")["EMBEDDED"]
			isEmbedded := embedded.Kind() == reflect.Struct &&
				(explicitEmbed || (f.Anonymous && !isSingleColumnEmbed(embedded)))
			if isEmbedded {
				nested, err := walk(embedded)
				if err != nil {
					return nil, err
				}
				if gormTag == "" {
					fields = append(fields, nested...)
					continue
				}
				tag := string(f.Tag)
				if !explicitEmbed {
					tag = withEmbeddedSetting(tag)
				}
				structType := "struct"
				if ft.Kind() == reflect.Pointer {
					structType = "*struct"
				}
				fields = append(fields, "\t"+f.Name+" "+structType+" {\n"+strings.Join(nested, "\n")+"\n\t} "+tagLiteral(tag))
				continue
			}
			if err := validateSnapshotType(ft); err != nil {
				return nil, fmt.Errorf("--from-model: model %s field %s: %w", t, f.Name, err)
			}
			collectImports(ft, imports)
			line := "\t" + f.Name + " " + typeString(ft)
			if f.Tag != "" {
				line += " " + tagLiteral(string(f.Tag))
			}
			fields = append(fields, line)
		}
		return fields, nil
	}
	fields, err := walk(t)
	if err != nil {
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

// withEmbeddedSetting 为内联的命名字段保留原匿名字段的嵌入语义.
func withEmbeddedSetting(tag string) string {
	const prefix = `gorm:"`
	if after, ok := strings.CutPrefix(tag, prefix); ok {
		return prefix + "embedded;" + after
	}
	return strings.Replace(tag, " "+prefix, " "+prefix+"embedded;", 1)
}

// typeString 渲染字段类型源码；reflect 把 []byte 打印为 []uint8，按惯例还原.
func typeString(t reflect.Type) string {
	if t == reflect.TypeFor[[]byte]() {
		return "[]byte"
	}
	return t.String()
}

// isSingleColumnEmbed 判断匿名嵌入 struct 是否按 GORM 语义视为单列而非展平：
// 时间类型和实现 driver.Valuer 的类型对应单列.
func isSingleColumnEmbed(t reflect.Type) bool {
	return t.ConvertibleTo(schema.TimeReflectType) || t.Implements(valuerType) || reflect.PointerTo(t).Implements(valuerType)
}

var valuerType = reflect.TypeFor[driver.Valuer]()

func validateSnapshotType(t reflect.Type) error {
	if name := t.Name(); name != "" {
		if t.PkgPath() != "" && (!token.IsExported(name) || strings.Contains(name, "[")) {
			return fmt.Errorf("type %s cannot be rendered as a portable snapshot field", t)
		}
		return nil
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return validateSnapshotType(t.Elem())
	case reflect.Map:
		if err := validateSnapshotType(t.Key()); err != nil {
			return err
		}
		return validateSnapshotType(t.Elem())
	case reflect.Interface:
		if t.NumMethod() == 0 {
			return nil
		}
	}
	return fmt.Errorf("unsupported anonymous snapshot field type %s", t)
}

// collectImports 收集类型源码实际引用的包路径与别名。
func collectImports(t reflect.Type, imports map[string]string) {
	for t.Name() == "" {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			t = t.Elem()
		case reflect.Map:
			collectImports(t.Key(), imports)
			t = t.Elem()
		default:
			return
		}
	}
	pkgPath := t.PkgPath()
	if pkgPath == "" {
		return
	}
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
