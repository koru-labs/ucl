package ibft

import (
	"encoding/json"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/helper/common"
	"github.com/0xPolygon/polygon-edge/types"
)

const (
	ConsensusName = "ibft"
)

// IBFTConfig is the configuration file for the Polybft consensus protocol.
type IBFTConfig struct {
	// EpochSize is size of epoch
	EpochSize uint64 `json:"epochSize"`

	// BlockTime is target frequency of blocks production
	BlockTime common.Duration `json:"blockTime"`

	InitialTrieRoot types.Hash `json:"initialTrieRoot"`
}

// LoadIBFTConfig loads chain config from provided path and unmarshals PolyBFTConfig
func LoadIBFTConfig(chainConfigFile string) (IBFTConfig, error) {
	chainCfg, err := chain.ImportFromFile(chainConfigFile)
	if err != nil {
		return IBFTConfig{}, err
	}

	polybftConfig, err := GetIBFTConfig(chainCfg)
	if err != nil {
		return IBFTConfig{}, err
	}

	return polybftConfig, err
}

// GetIBFTConfig deserializes provided chain config and returns PolyBFTConfig
func GetIBFTConfig(chainConfig *chain.Chain) (IBFTConfig, error) {
	consensusConfigJSON, err := json.Marshal(chainConfig.Params.Engine["ibft"])
	if err != nil {
		return IBFTConfig{}, err
	}

	var polyBFTConfig IBFTConfig
	if err = json.Unmarshal(consensusConfigJSON, &polyBFTConfig); err != nil {
		return IBFTConfig{}, err
	}

	return polyBFTConfig, nil
}
