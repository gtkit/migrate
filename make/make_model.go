package make

import (
	"fmt"
	"os"

	"github.com/gtkit/migrate/console"
	"github.com/spf13/cobra"
)

var CmdMakeModel = &cobra.Command{
	Use:   "model",
	Short: "Create model file, example: make model user",
	Run:   runMakeModel,
	Args:  cobra.ExactArgs(1),
}

func runMakeModel(_ *cobra.Command, args []string) {
	model := makeModelFromString(projectName, "", args[0], "")

	repositoryDir := fmt.Sprintf("internal/repository/%s/", model.PackageName)
	if err := os.MkdirAll(repositoryDir, os.ModePerm); err != nil {
		console.Error("Failed to create repository directory: " + err.Error())
		return
	}

	createFileFromStub("internal/models/"+model.PackageName+".go", "model/model", model)
	createFileFromStub(repositoryDir+model.PackageName+"_util.go", "model/model_util", model)
	createFileFromStub(repositoryDir+model.PackageName+"_i.go", "model/i", model)
}
