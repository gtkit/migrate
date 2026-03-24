package make

import (
	"fmt"

	"github.com/gtkit/migrate/v2/console"
	"github.com/spf13/cobra"
)

var CmdMakeCMD = &cobra.Command{
	Use:   "cmd",
	Short: "Create a command, should be snake_case, example: make cmd backup_database",
	RunE:  runMakeCMD,
	Args:  cobra.ExactArgs(1),
}

func runMakeCMD(cmd *cobra.Command, args []string) error {
	cfg := resolveConfig(cmd)
	model := enrichModel(cfg, makeModelFromString(cfg.ProjectName, "cmd", args[0], ""))
	filePath := fmt.Sprintf("cmd/%s.go", model.PackageName)

	if err := createFileFromStub(filePath, "cmd", model, writeFailIfExists); err != nil {
		return err
	}

	console.Success("command name: " + model.PackageName)
	console.Success("command variable name: cmd.Cmd" + model.StructName)
	console.Warning("please edit main.go's app.Commands slice to register command")
	return nil
}
