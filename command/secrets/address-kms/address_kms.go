package addresskms

import (
	"github.com/0xPolygon/polygon-edge/command"
	"github.com/spf13/cobra"
)

func GetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kms-validator-address",
		Short: "Derives the Ethereum validator address from the public key stored in AWS KMS.",
		Run:   runCommand,
	}

	cmd.Flags().StringVar(
		&params.kmsConfigPath,
		kmsConfigPathFlag,
		"",
		"path to the signer config JSON file (backend must be \"kms\")",
	)

	_ = cmd.MarkFlagRequired(kmsConfigPathFlag)

	return cmd
}

func runCommand(cmd *cobra.Command, _ []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	kmsCfg, err := loadKMSConfig(params.kmsConfigPath)
	if err != nil {
		outputter.SetError(err)

		return
	}

	addr, err := deriveValidatorAddress(kmsCfg)
	if err != nil {
		outputter.SetError(err)

		return
	}

	outputter.SetCommandResult(&addressResult{Address: addr.String()})
}
