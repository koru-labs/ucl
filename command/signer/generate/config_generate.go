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
		Run:   runCommand,
	}

	setFlags(signerGenerateCmd)
	helper.SetRequiredFlags(signerGenerateCmd, params.getRequiredFlags())

	return signerGenerateCmd
}

func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&params.dir,
		dirFlag,
		defaultConfigFileName,
		"the output path for the signer configuration file",
	)

	cmd.Flags().StringVar(
		&params.backend,
		backendFlag,
		"",
		"the signer backend type (kms, hsm)",
	)

	// KMS flags
	cmd.Flags().StringVar(
		&params.kmsKeyID,
		kmsKeyIDFlag,
		"",
		"[kms] the KMS key ARN or alias",
	)

	cmd.Flags().StringVar(
		&params.kmsRegion,
		kmsRegionFlag,
		"",
		"[kms] the AWS region",
	)

	cmd.Flags().StringVar(
		&params.kmsAccessKey,
		kmsAccessKeyFlag,
		"",
		"[kms] the AWS access key ID (leave empty to use instance role)",
	)

	cmd.Flags().StringVar(
		&params.kmsSecretKey,
		kmsKeyFlag,
		"",
		"[kms] the AWS secret access key (leave empty to use instance role)",
	)

	cmd.Flags().StringVar(
		&params.kmsRoleARN,
		kmsRoleARNFlag,
		"",
		"[kms] the IAM role ARN to assume for cross-account keys",
	)

	cmd.Flags().StringVar(
		&params.kmsEndpoint,
		kmsEndpointFlag,
		"",
		"[kms] custom endpoint override for LocalStack or testing",
	)

	// HSM flags
	cmd.Flags().StringVar(
		&params.hsmPin,
		hsmPinFlag,
		"",
		"[hsm] the HSM PIN",
	)

	cmd.Flags().StringVar(
		&params.hsmKeyLabel,
		hsmKeyLabelFlag,
		"",
		"[hsm] the label of the key pair in the HSM",
	)

	cmd.Flags().StringVar(
		&params.hsmLibPath,
		hsmLibPathFlag,
		"",
		"[hsm] the path to the PKCS#11 library",
	)

	cmd.Flags().StringVar(
		&params.hsmTokenLabel,
		hsmLabelFlag,
		"",
		"[hsm] the HSM token label",
	)

	cmd.Flags().StringVar(
		&params.hsmClusterID,
		hsmClusterIDFlag,
		"",
		"[hsm] the CloudHSM cluster ID",
	)

	cmd.Flags().IntVar(
		&params.hsmMaxSessions,
		hsmMaxSessionsFlag,
		0,
		"[hsm] the maximum number of HSM sessions",
	)

	cmd.Flags().IntVar(
		&params.hsmSessionTimeout,
		hsmSessionTimeoutFlag,
		0,
		"[hsm] the HSM session timeout in seconds",
	)
}

func runCommand(cmd *cobra.Command, _ []string) {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	if err := params.writeSignerConfig(); err != nil {
		outputter.SetError(err)

		return
	}

	outputter.SetCommandResult(params.getResult())
}
