package regenesis

import (
	"github.com/0xPolygon/polygon-edge/command/regenesis/growstatefile"
	"github.com/spf13/cobra"
)

var (
	params = &regenesisParams{}
)

type regenesisParams struct {
	TrieDBPath         string
	SnapshotTrieDBPath string
	TrieRoot           string
}

func GetCommand() *cobra.Command {
	genesisCMD := RegenesisCMD()
	genesisCMD.AddCommand(GetRootCMD())
	genesisCMD.AddCommand(HistoryTestCmd())
	genesisCMD.AddCommand(growstatefile.GetCommand())

	return genesisCMD
}
