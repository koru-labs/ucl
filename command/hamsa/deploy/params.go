package hamsadeploy

import (
	"bytes"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/0xPolygon/polygon-edge/command/helper"
	sidechainHelper "github.com/0xPolygon/polygon-edge/command/sidechain"
)

const (
	jsonRPCFlag = "json-rpc"
	chainIDFlag = "chain-id"
	repoUrlFlag = "repo-url"
	networkFlag = "network"
)

type deployParams struct {
	accountDir    string
	accountConfig string
	jsonRPC       string
	chainID       uint64
	repoURL       string
	network       string
}

func (v *deployParams) validateFlags() (err error) {
	if _, err = helper.ParseJSONRPCAddress(v.jsonRPC); err != nil {
		return fmt.Errorf("failed to parse json rpc address. Error: %w", err)
	}

	if !slices.Contains([]string{"dev", "qa", "prod"}, v.network) {
		return fmt.Errorf("invalid network name: %s", v.network)
	}

	u, err := url.Parse(v.repoURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.HasSuffix(v.repoURL, ".git") {
		return fmt.Errorf("invalid repo URL: %s", v.repoURL)
	}

	return sidechainHelper.ValidateSecretFlags(v.accountDir, v.accountConfig)
}

type deployResult struct {
	ContractAddress string `json:"contractAddress"`
}

func (dr deployResult) GetOutput() string {
	var buffer bytes.Buffer

	buffer.WriteString("\n[DEPLOY]\n")

	vals := []string{
		fmt.Sprintf("Contract Address|%s", dr.ContractAddress),
	}

	buffer.WriteString(helper.FormatKV(vals))
	buffer.WriteString("\n")

	return buffer.String()
}
