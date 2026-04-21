package growstatefile

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/spf13/cobra"
)

const (
	/*
		// SPDX-License-Identifier: MIT
		pragma solidity ^0.8.20;

		contract SimpleMapping {
			mapping(uint256 => uint256) public data;
			uint256 public countEntries;
		}
	*/
	contractCodeHex    = "6080604052348015600e575f5ffd5b506101608061001c5f395ff3fe608060405234801561000f575f5ffd5b5060043610610034575f3560e01c8063f0ba844014610038578063f99a4a9c14610068575b5f5ffd5b610052600480360381019061004d91906100d7565b610086565b60405161005f9190610111565b60405180910390f35b61007061009a565b60405161007d9190610111565b60405180910390f35b5f602052805f5260405f205f915090505481565b60015481565b5f5ffd5b5f819050919050565b6100b6816100a4565b81146100c0575f5ffd5b50565b5f813590506100d1816100ad565b92915050565b5f602082840312156100ec576100eb6100a0565b5b5f6100f9848285016100c3565b91505092915050565b61010b816100a4565b82525050565b5f6020820190506101245f830184610102565b9291505056fea2646970667358221220a172643bf0366f4662472ce7b8f49939483d5cb36f93a02581a1b3eab24032f764736f6c63430008220033" //nolint
	contractAddrPrefix = "0x1234567890ABCDEF0000000000000000%d"

	trieDirPathFlag       = "trie-dir-path"
	blockchainDirPathFlag = "blockchain-dir-path"
	genesisPathFlag       = "genesis-path"
	contractsCountsFlag   = "contracts-counts"
	hashChangesFlag       = "hash-changes-per-contract"
	createGenesisFlag     = "create-genesis-block"
	rootHashFlag          = "root-hash"
	useBlsValidatorsFlag  = "use-bls"

	defaultContractsCount   = 2
	defaultIterationsCnt    = 100
	defaultUseBlsValidators = false
	outputIterationsModuo   = 50
)

type growStateParams struct {
	trieDirPath            string
	blockchainDirPath      string
	genesisPath            string
	contractsCounts        int
	hashChangesPerContract int
	createGenesisBlock     bool
	rootHash               string
	useBlsValidators       bool
}

var params growStateParams

func (p *growStateParams) validate() error {
	if params.trieDirPath == "" {
		return fmt.Errorf("trie-dir-path is required")
	}

	if params.contractsCounts <= 0 {
		return fmt.Errorf("contracts-counts must be greater than 0")
	}

	if params.hashChangesPerContract <= 0 {
		return fmt.Errorf("hash-changes-per-contract must be greater than 0")
	}

	if params.rootHash == "" && params.blockchainDirPath == "" {
		return fmt.Errorf("either root-hash-str or blockchain-dir-path is required")
	}

	if params.createGenesisBlock && (params.genesisPath == "" || params.blockchainDirPath == "") {
		return fmt.Errorf("genesis-path and blockchain-dir-path are required when create-genesis-block is true")
	}

	return nil
}

func (p *growStateParams) getSigner() signer.Signer {
	var km signer.KeyManager
	if p.useBlsValidators {
		km = &signer.BLSKeyManager{}
	} else {
		km = &signer.ECDSAKeyManager{}
	}

	return signer.NewSigner(km, km)
}

func setFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(
		&params.trieDirPath,
		trieDirPathFlag,
		"",
		"path to trie directory")
	cmd.Flags().StringVar(
		&params.blockchainDirPath,
		blockchainDirPathFlag,
		"",
		"path to blockchain directory")
	cmd.Flags().StringVar(
		&params.genesisPath,
		genesisPathFlag,
		"",
		"path to genesis json")
	cmd.Flags().IntVar(
		&params.contractsCounts,
		contractsCountsFlag,
		defaultContractsCount,
		"number of contracts to deploy/update")
	cmd.Flags().IntVar(
		&params.hashChangesPerContract,
		hashChangesFlag,
		defaultIterationsCnt,
		"number of mapping updates per contract")
	cmd.Flags().BoolVar(
		&params.createGenesisBlock,
		createGenesisFlag,
		false,
		"create genesis block with resulting state root")
	cmd.Flags().StringVar(
		&params.rootHash,
		rootHashFlag,
		"",
		"snapshot root hash string, use 0x to read from current head")
	cmd.Flags().BoolVar(
		&params.useBlsValidators,
		useBlsValidatorsFlag,
		defaultUseBlsValidators,
		"use BLS validators instead of ECDSA validators for signature verification (only for genesis block creation)")
}
