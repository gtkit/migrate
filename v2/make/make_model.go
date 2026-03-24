package make

import (
	"github.com/spf13/cobra"
)

var CmdMakeModel = &cobra.Command{
	Use:   "model",
	Short: "Create model file, example: make model user",
	RunE:  runMakeModel,
	Args:  cobra.ExactArgs(1),
}

func runMakeModel(cmd *cobra.Command, args []string) error {
	cfg := resolveConfig(cmd)
	model := enrichModel(cfg, makeModelFromString(cfg.ProjectName, "", args[0], ""))
	return generateModelScaffold(cfg, model)
}

func generateModelScaffold(cfg Config, model Model) error {
	if err := ensureModelSupportFiles(cfg, model); err != nil {
		return err
	}
	if err := createFileFromStub(modelFilePath(cfg, model), "model/model", model, writeSkipIfExists); err != nil {
		return err
	}
	if err := createFileFromStub(repositoryFilePath(cfg, model), "model/repository", model, writeFailIfExists); err != nil {
		return err
	}
	if err := createFileFromStub(repositoryUtilFilePath(cfg, model), "model/repository_util", model, writeFailIfExists); err != nil {
		return err
	}
	return nil
}

func ensureModelSupportFiles(cfg Config, model Model) error {
	if err := createFileFromStub(modelBaseFilePath(cfg), "model/base", model, writeSkipIfExists); err != nil {
		return err
	}
	if err := createFileFromStub(modelDocFilePath(cfg), "model/doc", model, writeSkipIfExists); err != nil {
		return err
	}
	return nil
}
