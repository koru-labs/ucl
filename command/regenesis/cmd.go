package regenesis

import (
	"github.com/spf13/cobra"
)

var (
	params = &regenesisParams{}
)

type regenesisParams struct {
	SrcDBPath string
	DstDBPath string
	TrieRoot  string
	SrcDBType string
	DstDBType string
}

func GetCommand() *cobra.Command {
	genesisCMD := RegenesisCMD()
	genesisCMD.AddCommand(GetRootCMD())
	genesisCMD.AddCommand(HistoryTestCmd())

	return genesisCMD
}
