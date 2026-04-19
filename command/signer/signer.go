package signer

import (
	"github.com/0xPolygon/polygon-edge/command/helper"
	"github.com/0xPolygon/polygon-edge/command/signer/generate"
	"github.com/spf13/cobra"
)

func GetCommand() *cobra.Command {
	signerCmd := &cobra.Command{
		Use: "signer",
		Short: "Top level signer command for managing node signing backend configuration." +
			"Only accepts subcommands.",
	}

	helper.RegisterGRPCAddressFlag(signerCmd)

	registerSubcommands(signerCmd)

	return signerCmd
}

func registerSubcommands(baseCmd *cobra.Command) {
	baseCmd.AddCommand(
		// signer config generate
		generate.GetCommand(),
	)
}
