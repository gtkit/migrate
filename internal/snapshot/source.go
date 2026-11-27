package snapshot

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

	"gorm.io/gorm/schema"
)

// Source supplies the model fields and imports for a create migration.
type Source struct {
	Fields  string
	Imports string
}

func renderSnapshot(t reflect.Type) (Source, error) {
	if t.Kind() != reflect.Struct {
		return Source{}, errors.New("--from-model: model must be a struct")
	}
	imports := map[string]string{}
	ancestors := map[reflect.Type]bool{}
	var walk func(t reflect.Type, names map[string]bool) ([]string, error)
	walk = func(t reflect.Type, names map[string]bool) ([]string, error) {
		if ancestors[t] {
			return nil, fmt.Errorf("--from-model: recursive embedded type %s", t)
		}
		ancestors[t] = true
		defer delete(ancestors, t)
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
			if isEmbedded && gormTag == "" {
				nested, err := walk(embedded, names)
				if err != nil {
					return nil, err
				}
				fields = append(fields, nested...)
				continue
			}
			if names[f.Name] {
				return nil, fmt.Errorf("--from-model: duplicate snapshot field %s", f.Name)
			}
			names[f.Name] = true
			if isEmbedded {
				nested, err := walk(embedded, map[string]bool{})
				if err != nil {
					return nil, err
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
	fields, err := walk(t, map[string]bool{})
	if err != nil {
		return Source{}, err
	}
	if len(fields) == 0 {
		return Source{}, errors.New("--from-model: model has no exported fields")
	}

	delete(imports, "gorm.io/gorm") // 模板固定 import
	delete(imports, "github.com/gtkit/migrate/migration")
	paths := slices.Sorted(maps.Keys(imports))
	// 标准库与第三方分组，与 gofmt 惯例一致.
	var std, third strings.Builder
	packages := map[string]string{"gorm": "gorm.io/gorm", "migration": "github.com/gtkit/migrate/migration"}
	for _, p := range paths {
		name := imports[p]
		if name == "" {
			name = path.Base(p)
		}
		if prior, exists := packages[name]; exists {
			return Source{}, fmt.Errorf("--from-model: field imports %q and %q use the same package name %q", prior, p, name)
		}
		packages[name] = p
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
	return Source{Fields: strings.Join(fields, "\n"), Imports: group}, nil
}

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
// 时间类型和实现 driver.Valuer 的类型（如自定义 JSON 包装）对应单列.
func isSingleColumnEmbed(t reflect.Type) bool {
	return t.ConvertibleTo(schema.TimeReflectType) || t.Implements(valuerType) || reflect.PointerTo(t).Implements(valuerType)
}

var valuerType = reflect.TypeFor[driver.Valuer]()

func validateSnapshotType(t reflect.Type) error {
	if name := t.Name(); name != "" {
		if t.PkgPath() != "" && (!token.IsExported(name) || strings.Contains(name, "[") || strings.HasPrefix(t.String(), "main.")) {
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
