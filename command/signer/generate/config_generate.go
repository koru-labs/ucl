package generate

import (
	"github.com/0xPolygon/polygon-edge/command"
	"github.com/0xPolygon/polygon-edge/command/helper"
	"github.com/spf13/cobra"
)

func GetCommand() *cobra.Command {
	signerGenerateCmd := &cobra.Command{
		Use:   "generate-config",
		Short: "Generates a signer configuration file for KMS or HSM backend.",
	}

	// Persistent flag available to both subcommands
	signerGenerateCmd.PersistentFlags().StringVar(
		&params.dir,
		dirFlag,
		defaultConfigFileName,
		"the output path for the signer configuration file",
	)

	signerGenerateCmd.AddCommand(
		getKMSCommand(),
		getHSMCommand(),
	)

	return signerGenerateCmd
}

func getKMSCommand() *cobra.Command {
	kmsCmd := &cobra.Command{
		Use:   "kms",
		Short: "Generates a signer configuration file for AWS KMS backend.",
		Run:   runKMSCommand,
	}

	setKMSFlags(kmsCmd)
	helper.SetRequiredFlags(kmsCmd, params.getKMSRequiredFlags())

	return kmsCmd
}

func getHSMCommand() *cobra.Command {
	hsmCmd := &cobra.Command{
		Use:   "hsm",
		Short: "Generates a signer configuration file for HSM backend.",
		Run:   runHSMCommand,
	}

	setHSMFlags(hsmCmd)
	helper.SetRequiredFlags(hsmCmd, params.getHSMRequiredFlags())

	return hsmCmd
}

func setKMSFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&params.kmsKeyID,
		kmsKeyIDFlag,
		"",
		"the KMS key ARN or alias",
	)

	cmd.Flags().StringVar(
		&params.kmsRegion,
		kmsRegionFlag,
		"",
		"the AWS region",
	)

	cmd.Flags().StringVar(
		&params.kmsAccessKey,
		kmsAccessKeyFlag,
		"",
		"the AWS access key ID (leave empty to use instance role)",
	)

	cmd.Flags().StringVar(
		&params.kmsSecretKey,
		kmsKeyFlag,
		"",
		"the AWS secret access key (leave empty to use instance role)",
	)

	cmd.Flags().StringVar(
		&params.kmsRoleARN,
		kmsRoleARNFlag,
		"",
		"the IAM role ARN to assume for cross-account keys",
	)

	cmd.Flags().StringVar(
		&params.kmsEndpoint,
		kmsEndpointFlag,
		"",
		"custom endpoint override for LocalStack or testing",
	)
}

func setHSMFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&params.hsmPin,
		hsmPinFlag,
		"",
		"the HSM PIN",
	)

	cmd.Flags().StringVar(
		&params.hsmKeyLabel,
		hsmKeyLabelFlag,
		"",
		"the label of the pub key or key pair if privKeyLabel not provided",
	)

	cmd.Flags().StringVar(
		&params.hsmPrivKeyLabel,
		hsmPrivKeyLabelFlag,
		"",
		"the label of the priv key",
	)

	cmd.Flags().StringVar(
		&params.hsmLibPath,
		hsmLibPathFlag,
		"",
		"the path to the PKCS#11 library",
	)

	cmd.Flags().StringVar(
		&params.hsmTokenLabel,
		hsmLabelFlag,
		"",
		"the HSM token label",
	)

	cmd.Flags().StringVar(
		&params.hsmClusterID,
		hsmClusterIDFlag,
		"",
		"the CloudHSM cluster ID",
	)

	cmd.Flags().IntVar(
		&params.hsmMaxSessions,
		hsmMaxSessionsFlag,
		0,
		"the maximum number of HSM sessions",
	)

	cmd.Flags().IntVar(
		&params.hsmSessionTimeout,
		hsmSessionTimeoutFlag,
		0,
		"the HSM session timeout in seconds",
	)
}

func runKMSCommand(cmd *cobra.Command, _ []string) {
	params.backend = "kms"

	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	if err := params.writeSignerConfig(); err != nil {
		outputter.SetError(err)

		return
	}

	outputter.SetCommandResult(params.getResult())
}

func runHSMCommand(cmd *cobra.Command, _ []string) {
	params.backend = "hsm"

	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	if err := params.writeSignerConfig(); err != nil {
		outputter.SetError(err)

		return
	}

	outputter.SetCommandResult(params.getResult())
}
