package make

import (
	"fmt"

	"github.com/gtkit/migrate/v2/console"
	"github.com/spf13/cobra"
)

// CmdMakeCMD 生成 Cobra 命令脚手架文件（make cmd）.
var CmdMakeCMD = &cobra.Command{
	Use:   "cmd",
	Short: "Create a command, should be snake_case, example: make cmd backup_database",
	RunE:  runMakeCMD,
	Args:  cobra.ExactArgs(1),
}

func runMakeCMD(cmd *cobra.Command, args []string) error {
	model := newModel(resolveConfig(cmd), args[0], "")
	if err := createFileFromStub(fmt.Sprintf("cmd/%s.go", model.PackageName), "cmd", model, writeFailIfExists, nil); err != nil {
		return err
	}

	console.Success("command variable: cmd.Cmd" + model.StructName)
	console.Warning("register it explicitly: rootCmd.AddCommand(cmd.Cmd" + model.StructName + ")")
	return nil
}
