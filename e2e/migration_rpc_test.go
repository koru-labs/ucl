package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Ethernal-Tech/ethgo/abi"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/e2e/framework"
	"github.com/0xPolygon/polygon-edge/migration/export"
	"github.com/0xPolygon/polygon-edge/types"
)

// The ucl to pitau migration's JSON-RPC vectors (pitau docs/plans/ucl-migration.md §8, §7.2): a real
// IBFT network of four validators and one observer, built and run as on mainnet, goes through the
// cases the migration must reproduce (transfers of both transaction types, contract deployment,
// storage writes and deletes, logs, the validator registry, a contract destroyed with its storage, a
// contract destroyed and recreated in one block) and halts at the cutover block the attestation
// contract names. The observer's own JSON-RPC answers, for every block, transaction, receipt and
// changed account and slot, and its export endpoint's records, are written to
// migration/testdata/rpc.json. pitau's tests serve the same blocks from the records and compare
// their answers with these, and its relay sentries' cutover reads them as it reads a ucl node.
//
// The network's keys are fresh on every run, so the file is written only when asked:
// UPDATE_MIGRATION_VECTORS=1 EDGE_BINARY=<a fresh build of this tree> go test ./e2e -run TestMigrationRPCVectors

const migrationPrefix = "ucl-migration-"

var migrationGasPrice = new(big.Int).Mul(big.NewInt(100), big.NewInt(1_000_000_000))

type rpcCall struct {
	Method string          `json:"method"`
	Params []interface{}   `json:"params"`
	Result json.RawMessage `json:"result"`
}

type rpcVectors struct {
	Genesis      json.RawMessage `json:"genesis"`
	Validators   []types.Address `json:"validators"`
	Attestations types.Address   `json:"attestations"`
	Registry     types.Address   `json:"registry"`
	RegistryKey  string          `json:"registry_key"`
	Cutover      uint64          `json:"cutover"`
	Records      []string        `json:"records"`
	Calls        []rpcCall       `json:"calls"`
	Proofs       []rpcProof      `json:"proofs"`
}

// rpcProof is eth_getProof's answer for an account at a block, with every slot the network ever
// wrote for it: a relay sentry proves the state its cutover reads (§7.2), and pitau's tests serve
// it the slots it asks for from these.
type rpcProof struct {
	Block  uint64          `json:"block"`
	Result json.RawMessage `json:"result"`
}

type rpcClient struct {
	t     *testing.T
	url   string
	calls []rpcCall
}

// call asks the node and returns its raw answer; recorded ones go into the vectors.
func (c *rpcClient) call(record bool, method string, params ...interface{}) json.RawMessage {
	c.t.Helper()

	if params == nil {
		params = []interface{}{}
	}

	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	require.NoError(c.t, err)

	resp, err := http.Post(c.url, "application/json", bytes.NewReader(body))
	require.NoError(c.t, err)

	defer resp.Body.Close()

	var answer struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	require.NoError(c.t, json.NewDecoder(resp.Body).Decode(&answer))
	require.Empty(c.t, string(answer.Error), "%s", method)

	if record {
		c.calls = append(c.calls, rpcCall{Method: method, Params: params, Result: answer.Result})
	}

	return answer.Result
}

func (c *rpcClient) quantity(method string, params ...interface{}) uint64 {
	c.t.Helper()

	var hexValue string
	require.NoError(c.t, json.Unmarshal(c.call(false, method, params...), &hexValue))

	value, ok := new(big.Int).SetString(strings.TrimPrefix(hexValue, "0x"), 16)
	require.True(c.t, ok, hexValue)

	return value.Uint64()
}

type migrationAccount struct {
	key   *ecdsa.PrivateKey
	addr  types.Address
	nonce uint64
}

// send signs and submits a transaction without waiting for it.
func (c *rpcClient) send(signer crypto.TxSigner, chainID uint64, from *migrationAccount, to *types.Address,
	value *big.Int, input []byte, legacy bool, gas uint64) types.Hash {
	c.t.Helper()

	tx := &types.Transaction{Nonce: from.nonce, Gas: gas, To: to, Value: value, Input: input, From: from.addr}
	if legacy {
		tx.Type = types.LegacyTx
		tx.GasPrice = migrationGasPrice
	} else {
		tx.Type = types.DynamicFeeTx
		tx.ChainID = new(big.Int).SetUint64(chainID)
		tx.GasFeeCap = migrationGasPrice
		tx.GasTipCap = big.NewInt(1_000_000_000)
	}

	signed, err := signer.SignTx(tx, from.key)
	require.NoError(c.t, err)

	from.nonce++

	var hash types.Hash
	require.NoError(c.t, json.Unmarshal(c.call(false, "eth_sendRawTransaction", "0x"+hex.EncodeToString(signed.MarshalRLP())), &hash))

	return hash
}

// receipt waits for a transaction's receipt and returns its block number; it must have succeeded.
func (c *rpcClient) receipt(hash types.Hash) uint64 {
	c.t.Helper()

	deadline := time.Now().UTC().Add(60 * time.Second)

	for time.Now().UTC().Before(deadline) {
		var r *struct {
			Status      string `json:"status"`
			BlockNumber string `json:"blockNumber"`
		}

		require.NoError(c.t, json.Unmarshal(c.call(false, "eth_getTransactionReceipt", hash), &r))

		if r != nil {
			require.Equal(c.t, "0x1", r.Status, "transaction %s", hash)

			n, ok := new(big.Int).SetString(strings.TrimPrefix(r.BlockNumber, "0x"), 16)
			require.True(c.t, ok)

			return n.Uint64()
		}

		time.Sleep(200 * time.Millisecond)
	}

	c.t.Fatalf("no receipt for %s", hash)

	return 0
}

// initcode returns code that deploys runtime after running prefix.
func initcode(prefix, runtime []byte) []byte {
	size := byte(len(runtime))
	offset := byte(len(prefix) + 12)
	code := append(append([]byte{}, prefix...),
		0x60, size, 0x60, offset, 0x60, 0x00, 0x39, // CODECOPY(0, offset, size)
		0x60, size, 0x60, 0x00, 0xf3, // RETURN(0, size)
	)

	return append(code, runtime...)
}

func migrationWord(v uint64) []byte {
	return types.BytesToHash(new(big.Int).SetUint64(v).Bytes()).Bytes()
}

func TestMigrationRPCVectors(t *testing.T) {
	if os.Getenv("UPDATE_MIGRATION_VECTORS") == "" {
		t.Skip("writes migration/testdata/rpc.json: UPDATE_MIGRATION_VECTORS=1 EDGE_BINARY=<this tree's build>")
	}

	accounts := make([]*migrationAccount, 4)

	for i := range accounts {
		key, err := crypto.GenerateECDSAPrivateKey()
		require.NoError(t, err)

		accounts[i] = &migrationAccount{key: key, addr: crypto.PubKeyToAddress(&key.PublicKey)}
	}

	// The last account deploys the attestation stand-in as its first transaction, so its address is
	// known before the network starts and every node can be given it.
	attestations := crypto.CreateAddress(accounts[3].addr, 0)
	registry := crypto.CreateAddress(accounts[2].addr, 0)

	premine := new(big.Int).Mul(big.NewInt(1_000_000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	ibft := framework.NewIBFTServersManager(t, 4, migrationPrefix, func(_ int, config *framework.TestServerConfig) {
		for _, account := range accounts {
			config.Premine(account.addr, premine)
		}

		config.SetBlockTime(1)
		config.SetBlockLimit(30_000_000)
		config.NoBurnContract = true
		config.ExtraArgs = []string{"--migration-attestations", attestations.String()}
	})

	root := ibft.GetServer(0).Config.RootDir
	exportDir := t.TempDir()
	observer := framework.NewTestServer(t, root, func(config *framework.TestServerConfig) {
		config.SetConsensus(framework.ConsensusIBFT)
		config.SetIBFTDirPrefix(migrationPrefix)
		config.SetIBFTDir("observer")
		config.ExtraArgs = []string{
			"--seal=false",
			"--migration-attestations", attestations.String(),
			"--migration-export-dir", exportDir,
		}
	})

	_, err := observer.SecretsInit()
	require.NoError(t, err)

	t.Cleanup(observer.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ibft.StartServers(ctx)
	require.NoError(t, observer.Start(ctx))

	c := &rpcClient{t: t, url: "http://" + observer.JSONRPCAddr()}
	chainID := c.quantity("eth_chainId")
	signer := crypto.NewSigner(chain.AllForksEnabled.At(0), chainID)
	a0, a1, a2, a3 := accounts[0], accounts[1], accounts[2], accounts[3]
	fresh := types.StringToAddress("0xf7e5000000000000000000000000000000000001")

	// pitau's stub validator registry with one ML-DSA-65 key, deployed by a transaction as on
	// mainnet; transfers of both types; contracts that store, log, destroy themselves and create.
	registryKey := make([]byte, 1952)
	for i := range registryKey {
		registryKey[i] = byte(i*7 + 3)
	}

	raw, err := os.ReadFile(filepath.Join("..", "migration", "testdata", "validator-registry-initcode.hex"))
	require.NoError(t, err)

	registryCode, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(string(raw)), "0x"))
	require.NoError(t, err)

	args, err := abi.MustNewType(
		"tuple(uint256 maxValidators, uint256 keyTypeMask, tuple(uint8 keyType, bytes pubKey)[] initial)",
	).Encode(map[string]interface{}{
		"maxValidators": big.NewInt(50),
		"keyTypeMask":   big.NewInt(1 << 2),
		"initial":       []map[string]interface{}{{"keyType": uint8(2), "pubKey": registryKey}},
	})
	require.NoError(t, err)

	storeRuntime := []byte{
		0x60, 0x20, 0x35, 0x60, 0x00, 0x35, 0x55, 0x60, 0x00, 0x35, 0x60, 0x00, 0x60, 0x00, 0xa1, 0x00,
	}
	childRuntime := []byte{
		0x36, 0x15, 0x60, 0x0d, 0x57, 0x60, 0x20, 0x35, 0x60, 0x00, 0x35, 0x55, 0x00, 0x5b, 0x33, 0xff,
	}
	childInit := initcode([]byte{0x60, 0x01, 0x60, 0x00, 0x55}, childRuntime)
	factoryRuntime := append([]byte{
		0x60, byte(len(childInit)), 0x60, 0x11, 0x60, 0x00, 0x39,
		0x60, 0x00, 0x60, byte(len(childInit)), 0x60, 0x00, 0x60, 0x00, 0xf5, 0x00,
	}, childInit...)
	doomedInit := initcode([]byte{0x60, 0x11, 0x60, 0x01, 0x55, 0x60, 0x22, 0x60, 0x02, 0x55}, []byte{0x33, 0xff})

	wait := func(hashes ...types.Hash) uint64 {
		var last uint64
		for _, h := range hashes {
			last = c.receipt(h)
		}

		return last
	}

	wait(
		c.send(signer, chainID, a2, nil, big.NewInt(0), append(registryCode, args...), false, 8_000_000),
		c.send(signer, chainID, a0, &a1.addr, big.NewInt(1_000), nil, true, 21_000),
		c.send(signer, chainID, a1, &fresh, big.NewInt(7), nil, false, 21_000),
	)

	store := crypto.CreateAddress(a0.addr, a0.nonce)
	doomed := crypto.CreateAddress(a0.addr, a0.nonce+1)
	factory := crypto.CreateAddress(a0.addr, a0.nonce+2)
	wait(
		c.send(signer, chainID, a0, nil, big.NewInt(0), initcode(nil, storeRuntime), false, 200_000),
		c.send(signer, chainID, a0, nil, big.NewInt(0), doomedInit, true, 200_000),
		c.send(signer, chainID, a0, nil, big.NewInt(0), initcode(nil, factoryRuntime), false, 300_000),
	)

	input := func(slot, value uint64) []byte { return append(migrationWord(slot), migrationWord(value)...) }
	wait(
		c.send(signer, chainID, a0, &store, big.NewInt(0), input(1, 11), false, 100_000),
		c.send(signer, chainID, a1, &store, big.NewInt(0), input(2, 22), true, 100_000),
	)
	wait(
		c.send(signer, chainID, a0, &store, big.NewInt(0), input(1, 0), false, 100_000),
		c.send(signer, chainID, a1, &a2.addr, big.NewInt(5), []byte{0xfe}, false, 30_000),
	)

	child := crypto.CreateAddress2(factory, [32]byte{}, childInit)
	wait(c.send(signer, chainID, a0, &factory, big.NewInt(0), nil, false, 200_000))
	wait(
		c.send(signer, chainID, a0, &child, big.NewInt(0), input(5, 55), false, 100_000),
		c.send(signer, chainID, a1, &doomed, big.NewInt(0), nil, true, 100_000),
	)

	// The child destroys itself and is created again in one block: two transactions of one sender,
	// sent together. If a block boundary falls between them, the pair is sent again.
	resetBlock := uint64(0)

	for attempt := 0; attempt < 5 && resetBlock == 0; attempt++ {
		destroy := c.send(signer, chainID, a0, &child, big.NewInt(0), nil, false, 100_000)
		create := c.send(signer, chainID, a0, &factory, big.NewInt(0), nil, false, 200_000)

		if b := c.receipt(destroy); b == c.receipt(create) {
			resetBlock = b
		} else {
			// Recreated in the next block: write slot 5 again so the next attempt resets it.
			wait(c.send(signer, chainID, a0, &child, big.NewInt(0), input(5, 55), false, 100_000))
		}
	}

	require.NotZero(t, resetBlock, "the child was destroyed and recreated in one block")

	// The attestation stand-in: shouldHalt(h) is h >= K (PUSH1 4 CALLDATALOAD PUSH2 K GT ISZERO
	// PUSH1 0 MSTORE PUSH1 32 PUSH1 0 RETURN), with K a few blocks ahead.
	k := c.quantity("eth_blockNumber") + 4
	haltRuntime := []byte{
		0x60, 0x04, 0x35, 0x61, byte(k >> 8), byte(k), 0x11, 0x15, 0x60, 0x00, 0x52, 0x60, 0x20, 0x60, 0x00, 0xf3,
	}
	wait(c.send(signer, chainID, a3, nil, big.NewInt(0), initcode(nil, haltRuntime), false, 200_000))

	// ucl halts after the cutover block: the head stops moving.
	var cutover uint64

	for stable := 0; stable < 6; {
		head := c.quantity("eth_blockNumber")
		if head == cutover {
			stable++
		} else {
			cutover, stable = head, 0
		}

		time.Sleep(time.Second)
	}

	require.GreaterOrEqual(t, cutover, k, "ucl halted at the cutover block")

	// Every block, transaction and receipt, as the observer serves them.
	var blockHashes []types.Hash

	for n := uint64(0); n <= cutover; n++ {
		tag := fmt.Sprintf("0x%x", n)

		var block struct {
			Hash         types.Hash   `json:"hash"`
			Transactions []types.Hash `json:"transactions"`
		}

		require.NoError(t, json.Unmarshal(c.call(true, "eth_getBlockByNumber", tag, false), &block))
		c.call(true, "eth_getBlockByNumber", tag, true)
		c.call(true, "eth_getBlockByHash", block.Hash, false)
		c.call(true, "eth_getBlockTransactionCountByNumber", tag)

		blockHashes = append(blockHashes, block.Hash)

		for i, tx := range block.Transactions {
			c.call(true, "eth_getTransactionByHash", tx)
			c.call(true, "eth_getTransactionReceipt", tx)
			c.call(true, "eth_getTransactionByBlockNumberAndIndex", tag, fmt.Sprintf("0x%x", i))
		}
	}

	for _, emitter := range []types.Address{store, registry} {
		c.call(true, "eth_getLogs", map[string]interface{}{
			"fromBlock": "0x0", "toBlock": fmt.Sprintf("0x%x", cutover), "address": emitter,
		})
	}

	c.call(true, "eth_chainId")
	c.call(true, "eth_blockNumber")

	// The records, and through them every account and slot each block changed, before and after it.
	var records []string

	for next := uint64(1); next <= cutover; {
		var answer struct {
			Records []string `json:"records"`
			Next    string   `json:"next"`
		}

		raw := c.call(false, "ucl_exportBlocks", fmt.Sprintf("0x%x", next), fmt.Sprintf("0x%x", cutover))
		require.NoError(t, json.Unmarshal(raw, &answer))

		if len(answer.Records) == 0 {
			tag := fmt.Sprintf("0x%x", next)
			t.Logf("block %s: %s", tag, c.call(false, "eth_getBlockByNumber", tag, true))

			var block struct {
				Transactions []struct {
					Hash types.Hash `json:"hash"`
				} `json:"transactions"`
			}

			require.NoError(t, json.Unmarshal(c.call(false, "eth_getBlockByNumber", tag, true), &block))

			for _, tx := range block.Transactions {
				t.Logf("receipt: %s", c.call(false, "eth_getTransactionReceipt", tx.Hash))
			}
		}

		require.NotEmpty(t, answer.Records, "ucl_exportBlocks from %d: %.400s", next, raw)
		records = append(records, answer.Records...)
		next += uint64(len(answer.Records))
	}

	require.Len(t, records, int(cutover))

	sawReset := false
	// Every account the network touched, with every slot it wrote: proved at the end.
	written := map[types.Address]map[types.Hash]struct{}{}
	touch := func(address types.Address, slot *types.Hash) {
		if written[address] == nil {
			written[address] = map[types.Hash]struct{}{}
		}

		if slot != nil {
			written[address][*slot] = struct{}{}
		}
	}

	for i, frame := range records {
		n := uint64(i + 1)

		raw, err := base64.StdEncoding.DecodeString(frame)
		require.NoError(t, err)

		rlp, err := export.DecodeFrame(raw)
		require.NoError(t, err)

		var record export.Record
		require.NoError(t, record.UnmarshalRLP(rlp))
		require.Equal(t, blockHashes[n], record.Hash)

		for _, change := range record.Accounts {
			sawReset = sawReset || change.StorageReset

			touch(change.Address, nil)

			for _, slot := range change.Storage {
				touch(change.Address, &slot.Slot)
			}

			for _, at := range []uint64{n - 1, n} {
				tag := fmt.Sprintf("0x%x", at)
				c.call(true, "eth_getBalance", change.Address, tag)
				c.call(true, "eth_getTransactionCount", change.Address, tag)
				c.call(true, "eth_getCode", change.Address, tag)

				for _, slot := range change.Storage {
					c.call(true, "eth_getStorageAt", change.Address, slot.Slot, tag)
				}
			}
		}
	}

	require.True(t, sawReset, "a record resets a contract's storage")

	// The storage destroyed contracts held, before and after the blocks that removed it.
	for _, read := range []struct {
		address types.Address
		slots   []uint64
	}{{doomed, []uint64{1, 2}}, {child, []uint64{0, 5}}} {
		for n := uint64(1); n <= cutover; n++ {
			for _, slot := range read.slots {
				c.call(true, "eth_getStorageAt", read.address, types.BytesToHash(migrationWord(slot)), fmt.Sprintf("0x%x", n))
			}
		}
	}

	// What a relay sentry asks at the cutover (§7.2): the halt, and the registry at H.
	selector := func(signature string) string { return hex.EncodeToString(crypto.Keccak256([]byte(signature))[:4]) }
	shouldHalt := func(h uint64) map[string]interface{} {
		return map[string]interface{}{
			"to":   attestations,
			"data": "0x" + selector("shouldHalt(uint256)") + hex.EncodeToString(migrationWord(h)),
		}
	}
	c.call(true, "eth_call", shouldHalt(cutover), fmt.Sprintf("0x%x", cutover-1))
	c.call(true, "eth_call", shouldHalt(cutover-1), fmt.Sprintf("0x%x", cutover-2))

	for _, view := range []string{"version()", "validators()"} {
		c.call(true, "eth_call", map[string]interface{}{"to": registry, "data": "0x" + selector(view)}, fmt.Sprintf("0x%x", cutover))
	}

	genesis, err := os.ReadFile(filepath.Join(root, "genesis.json"))
	require.NoError(t, err)

	// The proofs of every account and slot at H - 1 and H, the states the relay sentry reads, and at
	// H - 2, where pitau's tests check that it refuses a false halt: the genesis allocations, the
	// accounts the blocks touched, the zero address (the caller of its view calls) and the blocks'
	// miners.
	var alloc struct {
		Genesis struct {
			Alloc map[types.Address]struct {
				Storage map[types.Hash]types.Hash `json:"storage"`
			} `json:"alloc"`
		} `json:"genesis"`
	}

	require.NoError(t, json.Unmarshal(genesis, &alloc))

	for address, account := range alloc.Genesis.Alloc {
		touch(address, nil)

		for slot := range account.Storage {
			touch(address, &slot)
		}
	}

	touch(types.ZeroAddress, nil)

	// The views read slots never written too, such as a counter still zero; the sentry asks for 64
	// consecutive slots at once.
	for _, contract := range []types.Address{registry, attestations} {
		for slot := uint64(0); slot < 64; slot++ {
			key := types.BytesToHash(migrationWord(slot))
			touch(contract, &key)
		}
	}

	// One proof per address at each of three blocks; each block may add its miner.
	proofs := make([]rpcProof, 0, 3*(len(written)+1))

	for _, at := range []uint64{cutover - 2, cutover - 1, cutover} {
		var block struct {
			Miner types.Address `json:"miner"`
		}

		require.NoError(t, json.Unmarshal(c.call(false, "eth_getBlockByNumber", fmt.Sprintf("0x%x", at), false), &block))
		touch(block.Miner, nil)

		addresses := make([]types.Address, 0, len(written))
		for address := range written {
			addresses = append(addresses, address)
		}

		sort.Slice(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 })

		for _, address := range addresses {
			keys := []types.Hash{}
			for slot := range written[address] {
				keys = append(keys, slot)
			}

			sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })

			proofs = append(proofs, rpcProof{
				Block:  at,
				Result: c.call(false, "eth_getProof", address, keys, fmt.Sprintf("0x%x", at)),
			})
		}
	}

	var validators []types.Address

	for i := 0; i < 4; i++ {
		srv := ibft.GetServer(i)
		key, err := os.ReadFile(filepath.Join(srv.Config.DataDir(), "consensus", "validator.key"))
		require.NoError(t, err)

		priv, err := crypto.BytesToECDSAPrivateKey(key)
		require.NoError(t, err)

		validators = append(validators, crypto.PubKeyToAddress(&priv.PublicKey))
	}

	out, err := json.MarshalIndent(rpcVectors{
		Genesis:      genesis,
		Validators:   validators,
		Attestations: attestations,
		Registry:     registry,
		RegistryKey:  hex.EncodeToString(registryKey),
		Cutover:      cutover,
		Records:      records,
		Calls:        c.calls,
		Proofs:       proofs,
	}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join("..", "migration", "testdata", "rpc.json"), append(out, '\n'), 0o600))
}
