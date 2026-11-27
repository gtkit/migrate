package command

import (
	"slices"
	"testing"
)

func TestCommandsExposeMakeAndMigrate(t *testing.T) {
	t.Parallel()
	var names []string
	for _, cmd := range Commands() {
		names = append(names, cmd.Name())
	}
	if !slices.Equal(names, []string{"make", "migrate"}) {
		t.Fatalf("unexpected commands: %v", names)
	}
}
