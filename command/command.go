package command

import (
	"github.com/gtkit/migrate"
	"github.com/gtkit/migrate/make"
	"github.com/spf13/cobra"
)

func Commands() []*cobra.Command {
	return []*cobra.Command{
		make.CmdMake,
		migrate.CmdMigrate,
	}
}
