package hamsadeploy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/0xPolygon/polygon-edge/command"
	"github.com/0xPolygon/polygon-edge/command/helper"
	"github.com/0xPolygon/polygon-edge/command/polybftsecrets"
	sidechainHelper "github.com/0xPolygon/polygon-edge/command/sidechain"
	"github.com/0xPolygon/polygon-edge/txrelayer"
	"github.com/spf13/cobra"
)

var params deployParams

func GetCommand() *cobra.Command {
	deployCmd := &cobra.Command{
		Use:     "deploy",
		Short:   "Deploys the Hamsa contracts on the chain",
		PreRunE: runPreRun,
		RunE:    runCommand,
	}

	helper.RegisterJSONRPCFlag(deployCmd)
	setFlags(deployCmd)

	return deployCmd
}

func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&params.accountDir,
		polybftsecrets.AccountDirFlag,
		"",
		polybftsecrets.AccountDirFlagDesc,
	)

	cmd.Flags().StringVar(
		&params.accountConfig,
		polybftsecrets.AccountConfigFlag,
		"",
		polybftsecrets.AccountConfigFlagDesc,
	)

	cmd.Flags().StringVar(
		&params.jsonRPC,
		jsonRPCFlag,
		txrelayer.DefaultRPCAddress,
		"the JSON RPC endpoint",
	)

	cmd.Flags().Uint64Var(
		&params.chainID,
		chainIDFlag,
		command.DefaultChainID,
		"the ID of the chain",
	)

	cmd.Flags().StringVar(
		&params.repoURL,
		repoUrlFlag,
		"",
		"the URL of the repository containing the deployment scripts",
	)

	cmd.Flags().StringVar(
		&params.network,
		networkFlag,
		"dev",
		"hardhat network name (dev, qa, prod) — sets hre.network.name in the deploy scripts",
	)

	cmd.MarkFlagsMutuallyExclusive(polybftsecrets.AccountDirFlag, polybftsecrets.AccountConfigFlag)
}

func runPreRun(cmd *cobra.Command, _ []string) error {
	return params.validateFlags()
}

func runCommand(cmd *cobra.Command, _ []string) error {
	outputter := command.InitializeOutputter(cmd)
	defer outputter.WriteOutput()

	adminAccount, err := sidechainHelper.GetAccount(params.accountDir, params.accountConfig)
	if err != nil {
		return err
	}

	rawKey, err := adminAccount.Ecdsa.MarshallPrivateKey()
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	scriptDir, err := os.MkdirTemp("", "hamsa-deploy-contracts")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}

	defer os.RemoveAll(scriptDir)

	if _, err = execute(scriptDir, "git", "clone", "--quiet", params.repoURL); err != nil {
		return fmt.Errorf("failed to clone repository: %w", err)
	}

	// derive the cloned directory name from the repo URL (strip trailing slash and .git suffix if present)
	repoURL := strings.TrimRight(params.repoURL, "/")
	repoName := strings.TrimSuffix(repoURL[strings.LastIndex(repoURL, "/")+1:], ".git")
	scriptDir = filepath.Join(scriptDir, repoName)

	if _, err = execute(scriptDir, "npm", "install", "--silent"); err != nil {
		return fmt.Errorf("failed to install dependencies: %w", err)
	}

	privateKeyHex := hex.EncodeToString(rawKey)
	chainIDStr := fmt.Sprintf("%d", params.chainID)
	envPrefix := strings.ToUpper(params.network) + "_"
	argsDeployInfra := []string{
		"cross-env",
		"HARDHAT_NETWORK=" + params.network,
		envPrefix + "RPC_URL=" + params.jsonRPC,
		envPrefix + "CHAIN_ID=" + chainIDStr,
		envPrefix + "PRIVATE_KEY=" + privateKeyHex,
		"npm",
		"run",
		"deploy",
		"--silent",
		"--loglevel=error",
	}

	outputter.Write([]byte("Executing deployment infra script with the following command:\n"))
	outputter.Write([]byte(strings.Replace(strings.Join(argsDeployInfra, " "), privateKeyHex, "PRIVATE_KEY", 1)))
	outputter.Write([]byte("\n\n"))
	outputter.WriteOutput()

	_, err = execute(scriptDir, "npx", argsDeployInfra...)
	if err != nil {
		return fmt.Errorf("failed to deploy contracts: %w", err)
	}

	argsDeployToken := []string{
		"cross-env",
		"HARDHAT_NETWORK=" + params.network,
		envPrefix + "RPC_URL=" + params.jsonRPC,
		envPrefix + "CHAIN_ID=" + chainIDStr,
		envPrefix + "PRIVATE_KEY=" + privateKeyHex,
		"npm",
		"run",
		"deploy_token",
		"--silent",
		"--loglevel=error",
	}

	deploymentsDir := filepath.Join(scriptDir, "..", "deployments")

	if err = os.MkdirAll(deploymentsDir, 0750); err != nil {
		return fmt.Errorf("failed to create deployments directory: %w", err)
	}

	accJSON, err := json.MarshalIndent(newAccount(), "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize account: %w", err)
	}

	if err = os.WriteFile(filepath.Join(deploymentsDir, "account.json"), accJSON, 0600); err != nil {
		return fmt.Errorf("failed to write account.json: %w", err)
	}

	outputter.Write([]byte("Executing deployment token script with the following command:\n"))
	outputter.Write([]byte(strings.Replace(strings.Join(argsDeployToken, " "), privateKeyHex, "PRIVATE_KEY", 1)))
	outputter.Write([]byte("\n\n"))
	outputter.WriteOutput()

	deployCliOutput, err := execute(scriptDir, "npx", argsDeployToken...)
	if err != nil {
		return fmt.Errorf("failed to deploy contracts: %w", err)
	}

	contractAddress := parseContractAddress(deployCliOutput)
	if contractAddress == "" {
		return fmt.Errorf("failed to parse contract address from output")
	}

	outputter.WriteCommandResult(&deployResult{ContractAddress: contractAddress})

	return nil
}

func parseContractAddress(output string) string {
	const contractAddressPrefix = "PrivateUSDC initialized successfully at: "

	for line := range strings.SplitSeq(output, "\n") {
		if _, after, ok := strings.Cut(line, contractAddressPrefix); ok {
			return strings.TrimSpace(after)
		}
	}

	return ""
}

func execute(dir string, cmd string, args ...string) (string, error) {
	var (
		stdErr      bytes.Buffer
		stdOut      bytes.Buffer
		stdOutMulti = io.MultiWriter(os.Stdout, &stdOut)
	)

	cliCmd := exec.Command(cmd, args...) //nolint:gosec
	cliCmd.Stderr = &stdErr
	cliCmd.Stdout = stdOutMulti
	cliCmd.Dir = dir

	if err := cliCmd.Run(); err != nil {
		if stdErr.Len() > 0 {
			return "", fmt.Errorf("failed to execute command: %s", stdErr.String())
		}

		return "", fmt.Errorf("failed to execute command: %w", err)
	}

	if stdErr.Len() > 0 {
		return "", fmt.Errorf("error during command execution: %s", stdErr.String())
	}

	return stdOut.String(), nil
}
