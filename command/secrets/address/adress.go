package address

import (
	"github.com/0xPolygon/polygon-edge/command"
	"github.com/spf13/cobra"
)

func GetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pubkey-to-address",
		Short: "Derives the Ethereum address from a DER base64-encoded public key.",
		Run:   runCommand,
	}

	cmd.Flags().StringVar(
		&params.pubkeyB64,
		pubkeyFlag,
		"",
		"the DER base64-encoded public key (e.g. from aws kms get-public-key)",
	)

	_ = cmd.MarkFlagRequired(pubkeyFlag)

	return cmd
}

func runCommand(cmd *cobra.Command, _ []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	address, err := params.deriveAddress()
	if err != nil {
		outputter.SetError(err)

		return
	}

	outputter.SetCommandResult(&addressResult{Address: address})
}
