package snapshot

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

var registration struct {
	sync.RWMutex
	models []any
	namer  schema.Namer
}

// Reset clears the registrations from the previous Setup.
func Reset() {
	registration.Lock()
	defer registration.Unlock()
	registration.models = nil
	registration.namer = nil
}

// Register adds models for the current Setup.
func Register(models ...any) {
	registration.Lock()
	defer registration.Unlock()
	registration.models = append(registration.models, models...)
}

// SetNamingStrategy configures the same table naming strategy as the migration DB.
func SetNamingStrategy(db *gorm.DB) {
	registration.Lock()
	defer registration.Unlock()
	if db != nil && db.Config != nil {
		registration.namer = db.NamingStrategy
	}
}

// FromModel renders a snapshot from the model with the requested physical table name.
func FromModel(table string) (Source, error) {
	registration.RLock()
	models, namer := slices.Clone(registration.models), registration.namer
	registration.RUnlock()
	if namer == nil {
		namer = schema.NamingStrategy{}
	}
	var cache sync.Map
	known := make([]string, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		parsed, err := schema.Parse(model, &cache, namer)
		if err != nil {
			return Source{}, fmt.Errorf("--from-model: parse DDL model: %w", err)
		}
		if parsed.Table == table {
			return renderSnapshot(parsed.ModelType)
		}
		known = append(known, parsed.Table)
	}
	return Source{}, fmt.Errorf("--from-model: no registered DDL model has table %q (registered: %s); register it with WithDDLModels", table, strings.Join(known, ", "))
}
