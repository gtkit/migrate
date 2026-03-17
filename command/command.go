package command

import (
	"github.com/gtkit/migrate"
	"github.com/gtkit/migrate/make"
	"github.com/spf13/cobra"
)

// Commands 返回所有可用的 cobra 命令.
func Commands() []*cobra.Command {
	return []*cobra.Command{
		make.CmdMake,
		migrate.CmdMigrate,
	}
}
