package command

import (
	"github.com/gtkit/migrate/v2"
	"github.com/gtkit/migrate/v2/make"
	"github.com/spf13/cobra"
)

// Commands 杩斿洖鎵€鏈夊彲鐢ㄧ殑 cobra 鍛戒护.
func Commands() []*cobra.Command {
	return []*cobra.Command{
		make.CmdMake,
		migrate.CmdMigrate,
	}
}
