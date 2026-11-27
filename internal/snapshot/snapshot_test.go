package snapshot

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type registryModel struct {
	ID int64 `gorm:"primaryKey"`
}

type explicitRegistryModel struct {
	Name string
}

func (explicitRegistryModel) TableName() string { return "physical_model" }

func TestFromModelMatchesGORMTableName(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	SetNamingStrategy(&gorm.DB{Config: &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "app_", SingularTable: true},
	}})
	Register(nil, &registryModel{}, &explicitRegistryModel{})
	for table, field := range map[string]string{"app_registry_model": "ID int64", "physical_model": "Name string"} {
		source, err := FromModel(table)
		if err != nil || !strings.Contains(source.Fields, field) {
			t.Errorf("snapshot %s: %+v, %v", table, source, err)
		}
	}
	if _, err := FromModel("registry_models"); err == nil || !strings.Contains(err.Error(), "app_registry_model") {
		t.Fatalf("expected mismatch with registered table, got %v", err)
	}
	Reset()
	SetNamingStrategy(nil)
	Register(&registryModel{})
	if _, err := FromModel("registry_models"); err != nil {
		t.Fatal(err)
	}
	Register(42)
	if _, err := FromModel("unknown"); err == nil || !strings.Contains(err.Error(), "parse DDL model") {
		t.Fatalf("expected an invalid model error, got %v", err)
	}
}

type EmbeddedFields struct {
	Code string `gorm:"column:code"`
}

func TestRenderSnapshotKeepsTaggedFields(t *testing.T) {
	t.Parallel()
	model := struct {
		EmbeddedFields
		time.Time       `gorm:"column:at;type:datetime"`
		*sql.NullString `gorm:"column:nullable"`
		Contact         *EmbeddedFields `gorm:"embedded;embeddedPrefix:contact_"`
		Command         *cobra.Command
		Updated         gorm.DeletedAt
		Bytes           []byte
		Lookup          map[time.Time][2]*sql.NullInt64
		Object          any
		Quoted          string "gorm:\"comment:`quoted`\""
		Ignore          string `gorm:"-"`
		hidden          string
	}{}
	source, err := renderSnapshot(reflect.TypeOf(model))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`Code string`, `gorm:"column:code"`, `NullString *sql.NullString`, `Contact *struct`,
		`gorm:"embedded;embeddedPrefix:contact_"`, `[]byte`, `map[time.Time][2]*sql.NullInt64`,
		`Time time.Time`, `Updated gorm.DeletedAt`, `comment:` + "`quoted`",
	} {
		if !strings.Contains(source.Fields, want) {
			t.Errorf("missing %q in %s", want, source.Fields)
		}
	}
	for _, pkg := range []string{"database/sql", "time", "github.com/spf13/cobra"} {
		if !strings.Contains(source.Imports, pkg) {
			t.Errorf("missing import %q in %s", pkg, source.Imports)
		}
	}
	if strings.Contains(source.Fields, "Ignore") || strings.Contains(source.Fields, "hidden") ||
		strings.Contains(source.Imports, "gorm.io/gorm") {
		t.Errorf("unexpected snapshot content: %+v", source)
	}
}

type (
	inaccessible string
	Recurring    struct{ *Recurring }
	Repeated     struct{ Code string }
	Box[T any]   struct{ Value T }
)

func TestRenderSnapshotRejectsUnrepresentableTypes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		model any
		want  string
	}{
		{"non struct", 0, "must be a struct"},
		{"empty", struct{ ignored int }{}, "no exported fields"},
		{"private element", struct{ States []*inaccessible }{}, "cannot be rendered"},
		{"private map key", struct{ States map[inaccessible]int }{}, "cannot be rendered"},
		{"anonymous function", struct{ Callback func() }{}, "unsupported"},
		{"generic named type", struct{ Box Box[int] }{}, "cannot be rendered"},
		{"recursive embedding", Recurring{}, "recursive embedded type"},
		{"duplicate field", struct {
			EmbeddedFields
			Repeated
		}{}, "duplicate snapshot field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := renderSnapshot(reflect.TypeOf(tc.model)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}
