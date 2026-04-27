package addresshsm

import (
	"github.com/0xPolygon/polygon-edge/command"
	"github.com/spf13/cobra"
)

func GetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hsm-validator-address",
		Short: "Derives the Ethereum validator address from the public key stored in an HSM.",
		Run:   runCommand,
	}

	cmd.Flags().StringVar(
		&params.hsmConfigPath,
		hsmConfigPathFlag,
		"",
		"path to the signer config JSON file (backend must be \"hsm\")",
	)

	_ = cmd.MarkFlagRequired(hsmConfigPathFlag)

	return cmd
}

func runCommand(cmd *cobra.Command, _ []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	hsmCfg, err := loadHSMConfig(params.hsmConfigPath)
	if err != nil {
		outputter.SetError(err)

		return
	}

	addr, err := deriveValidatorAddress(hsmCfg)
	if err != nil {
		outputter.SetError(err)

		return
	}

	outputter.SetCommandResult(&addressResult{Address: addr.String()})
}
