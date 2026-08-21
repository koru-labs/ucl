package precompiled

import (
	"encoding/binary"
	"os"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/state/runtime"
	"github.com/0xPolygon/polygon-edge/types"
)

// nondeterministic writes os.Getpid() into its own storage so two processes
// executing the same transaction produce different state roots.
type nondeterministic struct{}

func (c *nondeterministic) gas(_ []byte, _ *chain.ForksInTime) uint64 {
	return 15
}

func (c *nondeterministic) run(_ []byte, caller types.Address, host runtime.Host) ([]byte, error) {
	out := make([]byte, 32)
	binary.BigEndian.PutUint64(out[24:], uint64(os.Getpid()))

	// Write on the caller, not 0x2060. 0x2060 has nonce=0, balance=0, empty
	// code, so EIP-158 treats it as empty and drops it at commit — the PID
	// never reaches the state root and validators agree.
	host.SetState(caller, types.ZeroHash, types.BytesToHash(out))

	return out, nil
}
