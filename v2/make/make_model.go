package make

import (
	"github.com/spf13/cobra"
)

// CmdMakeModel 生成 model 与 repository 脚手架（make model）.
var CmdMakeModel = &cobra.Command{
	Use:   "model",
	Short: "Create model file, example: make model user",
	RunE:  runMakeModel,
	Args:  cobra.ExactArgs(1),
}

func runMakeModel(cmd *cobra.Command, args []string) error {
	cfg := resolveConfig(cmd)
	name, err := resolveProjectName(cfg)
	if err != nil {
		return err
	}
	cfg.ProjectName = name
	return generateModelScaffold(cfg, newModel(cfg, args[0], ""))
}

// generateModelScaffold 生成 model 包基础文件与该实体的 model/repository；均已存在则跳过，不覆盖.
func generateModelScaffold(cfg Config, model Model) error {
	for _, f := range []struct{ path, stub string }{
		{modelBaseFilePath(cfg), "model/base"},
		{modelDocFilePath(cfg), "model/doc"},
		{modelFilePath(cfg, model), "model/model"},
		{repositoryFilePath(cfg, model), "model/repository"},
		{repositoryUtilFilePath(cfg, model), "model/repository_util"},
	} {
		if err := createFileFromStub(f.path, f.stub, model, writeSkipIfExists, nil); err != nil {
			return err
		}
	}
	return nil
}
