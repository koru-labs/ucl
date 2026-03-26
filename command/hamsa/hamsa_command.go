package hamsa

import (
	hamsadeploy "github.com/0xPolygon/polygon-edge/command/hamsa/deploy"
	"github.com/spf13/cobra"
)

func GetCommand() *cobra.Command {
	hamsaCmd := &cobra.Command{
		Use:   "hamsa",
		Short: "Hamsa command",
	}

	hamsaCmd.AddCommand(
		hamsadeploy.GetCommand(),
	)

	return hamsaCmd
}
