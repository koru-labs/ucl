package ibft

import (
	"context"
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/consensus"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/hook"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
	"github.com/0xPolygon/polygon-edge/state/statetesthelper"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/validators"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestIBFTBackend_CalculateHeaderTimestamp verifies that the header timestamp
// is successfully calculated
func TestIBFTBackend_CalculateHeaderTimestamp(t *testing.T) {
	t.Parallel()

	// Reference time
	now := time.Unix(time.Now().UTC().Unix(), 0) // Round down

	testTable := []struct {
		name            string
		parentTimestamp int64
		currentTime     time.Time
		blockTime       uint64

		expectedTimestamp time.Time
	}{
		{
			"Valid clock block timestamp",
			now.Add(time.Duration(-1) * time.Second).Unix(), // 1s before
			now,
			1,
			now, // 1s after
		},
		{
			"Next multiple block clock",
			now.Add(time.Duration(-4) * time.Second).Unix(), // 4s before
			now,
			3,
			now, // now
		},
	}

	for _, testCase := range testTable {
		testCase := testCase

		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			i := &backendIBFT{
				blockTime: time.Duration(testCase.blockTime) * time.Second,
			}

			assert.Equal(
				t,
				testCase.expectedTimestamp.Unix(),
				i.calcHeaderTimestamp(
					uint64(testCase.parentTimestamp),
					testCase.currentTime,
				).Unix(),
			)
		})
	}
}

func TestIBFTBackend_GetVotingPowers(t *testing.T) {
	t.Parallel()

	validators := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(types.StringToAddress("1")),
		validators.NewECDSAValidator(types.StringToAddress("2")),
	)

	forkManagerMock := &forkManagerMock{}
	forkManagerMock.On("GetValidators", mock.Anything).Return(validators)

	i := &backendIBFT{
		forkManager: forkManagerMock,
	}

	result, err := i.GetVotingPowers(1)
	assert.NoError(t, err)
	assert.Equal(t, big.NewInt(1), result[types.AddressToString(validators.At(0).Addr())])
	assert.Equal(t, big.NewInt(1), result[types.AddressToString(validators.At(1).Addr())])
}

func TestIBFTBackend_BuildBlock(t *testing.T) {
	t.Parallel()

	keys := [20]*crypto.ECDSAKey{}
	receivers := [20]types.Address{}

	for i := range keys {
		key, err := crypto.GenerateECDSAKey()
		require.NoError(t, err)

		keys[i] = key
	}

	for i := range receivers {
		receivers[i] = types.Address{byte((i + 1) & 255), byte((i + 100) & 255)}
	}

	mySigner := signer.NewSigner(
		signer.NewECDSAKeyManagerFromKey(keys[0].PrivateKey()),
		signer.NewECDSAKeyManagerFromKey(keys[0].PrivateKey()),
	)
	round := uint64(0)
	validatorsSet := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(keys[0].Address()),
		validators.NewECDSAValidator(types.StringToAddress("1")),
		validators.NewECDSAValidator(types.StringToAddress("2")),
		validators.NewECDSAValidator(types.StringToAddress("3")),
	)
	parentExtraData := &signer.IstanbulExtra{
		Validators:           validatorsSet,
		ParentCommittedSeals: &signer.SerializedSeal{},
		CommittedSeals:       &signer.AggregatedSeal{},
		RoundNumber:          &round,
		TxDependency:         [][]uint64{{1, 2, 5}, {}, {4}, {}, {3}},
	}

	forkManagerMock := &forkManagerMock{}
	forkManagerMock.On("GetValidators", mock.Anything).Return(validatorsSet)
	forkManagerMock.On("GetSigner", mock.Anything).Return(mySigner)
	forkManagerMock.On("GetHooks", mock.Anything).Return(&hook.Hooks{})

	defParentBlockHeader := &types.Header{
		Number:     2,
		Hash:       types.Hash{0, 1, 2, 3, 4, 5},
		ParentHash: types.Hash{1, 3},
		GasLimit:   1_000_000_000_000,
		ExtraData:  append(make([]byte, signer.IstanbulExtraVanity), parentExtraData.MarshalRLPTo(nil)...),
	}
	chainParams := &chain.Params{
		ChainID:      100,
		Forks:        chain.AllForksEnabled,
		BurnContract: map[uint64]types.Address{0: types.ZeroAddress},
	}
	txSigner := crypto.NewFrontierSigner(true)

	getBlockchain := func(blockHeader *types.Header) *blockchain.Blockchain {
		return blockchain.NewTestBlockchain(t, []*types.Header{
			{Number: 1, Hash: types.Hash{1, 3}}, blockHeader,
		})
	}

	getExecutor := func(bc *blockchain.Blockchain) *state.Executor {
		executor := state.NewExecutor(chainParams, itrie.NewState(itrie.NewMemoryStorage()), hclog.NewNullLogger())
		executor.GetHash = bc.GetHashHelper

		return executor
	}

	buildBlock := func(
		parentHeader *types.Header, bc *blockchain.Blockchain, executor *state.Executor, txPool txPoolInterface,
	) (*types.Block, []*types.Receipt, error) {
		i := &backendIBFT{
			forkManager: forkManagerMock,
			blockchain:  bc,
			executor:    executor,
			logger:      hclog.NewNullLogger(),
			txpool:      txPool,
			blockTime:   1 * time.Second,
			config: &consensus.Config{
				Params: chainParams,
			},
		}

		return i.buildBlock(context.TODO(), parentHeader)
	}

	checkExtraData := func(t *testing.T, extraDataBytes []byte, expected [][]uint64) {
		t.Helper()

		extraData := &signer.IstanbulExtra{
			Validators: validators.NewECDSAValidatorSet(), CommittedSeals: &signer.SerializedSeal{},
		}

		require.NoError(t, extraData.UnmarshalRLP(extraDataBytes[signer.IstanbulExtraVanity:]))
		assert.Equal(t, expected, extraData.TxDependency)
	}

	t.Run("Test smart contract", func(t *testing.T) {
		t.Parallel()

		alloc, deployTx, txs, _, _ := statetesthelper.SetupParallelVerificationData(t)

		parentBlockHeader := defParentBlockHeader.Copy()
		testBlockchain := getBlockchain(parentBlockHeader)
		executor := getExecutor(testBlockchain)

		tran, err := executor.BeginTxn(types.ZeroHash, parentBlockHeader, types.ZeroAddress)
		require.NoError(t, err)

		// account must have balance
		for addr, val := range alloc {
			require.NoError(t, tran.SetAccountDirectly(addr, &chain.GenesisAccount{
				Balance: val.Balance,
				Nonce:   0,
			}))
		}

		deployTxResult, err := tran.Apply(deployTx)
		require.NoError(t, err)
		require.NoError(t, deployTxResult.Err)

		_, rootTrie, err := tran.Commit()
		require.NoError(t, err)

		scAddress := deployTxResult.Address
		parentBlockHeader.StateRoot = rootTrie

		txPool := &txPoolMock{}
		txPool.On("Prepare", mock.Anything).Run(func(args mock.Arguments) {})
		txPool.On("Length", mock.Anything).Return(uint64(0))

		for _, tx := range txs {
			txPool.On("Peek").Return(tx).Once()
			txPool.On("Pop", mock.Anything).Run(func(args mock.Arguments) {}).Once()
		}

		// STM pulls a whole candidate batch via repeated Peek() calls and may probe once more
		// after the pool is empty (harmless in production - the batch just comes back empty), so
		// this must tolerate any number of further calls rather than exactly one.
		txPool.On("Peek").Return((*types.Transaction)(nil))

		block, receipts, err := buildBlock(parentBlockHeader, testBlockchain, executor, txPool)

		require.NoError(t, err)
		require.NotNil(t, block)
		require.NotNil(t, receipts)
		require.Equal(t, parentBlockHeader.Number+1, block.Header.Number)
		require.Equal(t, parentBlockHeader.Hash.String(), block.Header.ParentHash.String())

		checkExtraData(t, block.Header.ExtraData, [][]uint64{{}, {}, {0}, {1}, {3}, {}, {}, {5, 6}})

		// check values directly
		tran, err = executor.BeginTxn(block.Header.StateRoot, block.Header, types.ZeroAddress)
		require.NoError(t, err)

		acc, exists := tran.Txn().GetAccount(scAddress)
		require.True(t, exists)
		assert.Len(t, acc.CodeHash, 32)

		addr1, err := hex.DecodeString("0000000000000000000000001122222222222222222222222222222222222222")
		require.NoError(t, err)

		addr2, err := hex.DecodeString("0000000000000000000000002222222222222222222222222222222222222222")
		require.NoError(t, err)

		addr3, err := hex.DecodeString("000000000000000000000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		require.NoError(t, err)

		addr4, err := hex.DecodeString("000000000000000000000000ffffffffffffffffffffffffffffffffffffffff")
		require.NoError(t, err)

		slot := make([]byte, 32)
		slot[31] = 1

		// balances[1122...]
		slotHash := crypto.Keccak256(append(addr1, slot...))
		value := tran.GetStorage(scAddress, types.Hash(slotHash))
		assert.Equal(t, big.NewInt(1000), new(big.Int).SetBytes(value.Bytes()))
		// balances[222...]
		slotHash = crypto.Keccak256(append(addr2, slot...))
		value = tran.GetStorage(scAddress, types.Hash(slotHash))
		assert.Equal(t, big.NewInt(489), new(big.Int).SetBytes(value.Bytes()))
		// balances[ffffffffffffffffffffffffffffffffffffffff]
		slotHash = crypto.Keccak256(append(addr3, slot...))
		value = tran.GetStorage(scAddress, types.Hash(slotHash))
		assert.Equal(t, big.NewInt(9900), new(big.Int).SetBytes(value.Bytes()))
		// balances[aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa]
		slotHash = crypto.Keccak256(append(addr4, slot...))
		value = tran.GetStorage(scAddress, types.Hash(slotHash))
		assert.Equal(t, big.NewInt(10100), new(big.Int).SetBytes(value.Bytes()))
		// total balance
		slot[31] = 0
		value = tran.GetStorage(scAddress, types.Hash(slot))
		assert.Equal(t, big.NewInt(1000), new(big.Int).SetBytes(value.Bytes()))
	})

	t.Run("Test EOA", func(t *testing.T) {
		t.Parallel()

		parentBlockHeader := defParentBlockHeader.Copy()
		testBlockchain := getBlockchain(parentBlockHeader)
		executor := getExecutor(testBlockchain)

		tran, err := executor.BeginTxn(types.ZeroHash, parentBlockHeader, types.ZeroAddress)
		require.NoError(t, err)

		// account must have balance
		for _, k := range keys {
			require.NoError(t, tran.SetAccountDirectly(k.Address(), &chain.GenesisAccount{
				Balance: big.NewInt(1_000_000_000_000),
				Nonce:   0,
			}))
		}

		_, rootTrie, err := tran.Commit()
		require.NoError(t, err)

		parentBlockHeader.StateRoot = rootTrie
		txs := [5]*types.Transaction{}
		txs[0], err = txSigner.SignTx(&types.Transaction{
			Nonce:    0,
			From:     keys[9].Address(),
			To:       &receivers[0],
			Value:    big.NewInt(100),
			Gas:      21000,
			GasPrice: big.NewInt(10000),
			Input:    []byte{},
		}, keys[9].PrivateKey())
		require.NoError(t, err)

		txs[1], err = txSigner.SignTx(&types.Transaction{
			Nonce:    0,
			From:     keys[1].Address(),
			To:       &receivers[1],
			Value:    big.NewInt(150),
			Gas:      21000,
			GasPrice: big.NewInt(10000),
			Input:    []byte{},
		}, keys[1].PrivateKey())
		require.NoError(t, err)

		txs[2], err = txSigner.SignTx(&types.Transaction{
			Nonce:    0,
			From:     keys[2].Address(),
			To:       &receivers[0],
			Value:    big.NewInt(200),
			Gas:      21000,
			GasPrice: big.NewInt(10000),
			Input:    []byte{},
		}, keys[2].PrivateKey())
		require.NoError(t, err)

		txs[3], err = txSigner.SignTx(&types.Transaction{
			Nonce:    1,
			From:     keys[1].Address(),
			To:       &receivers[2],
			Value:    big.NewInt(250),
			Gas:      21000,
			GasPrice: big.NewInt(10000),
			Input:    []byte{},
		}, keys[1].PrivateKey())
		require.NoError(t, err)

		txs[4], err = txSigner.SignTx(&types.Transaction{
			Nonce:    0,
			From:     keys[3].Address(),
			To:       &receivers[0],
			Value:    big.NewInt(300),
			Gas:      21000,
			GasPrice: big.NewInt(10000),
			Input:    []byte{},
		}, keys[3].PrivateKey())
		require.NoError(t, err)

		txPool := &txPoolMock{}
		txPool.On("Prepare", mock.Anything).Run(func(args mock.Arguments) {})
		txPool.On("Length", mock.Anything).Return(uint64(0))

		for _, tx := range txs {
			txPool.On("Peek").Return(tx).Once()
			txPool.On("Pop", mock.Anything).Run(func(args mock.Arguments) {}).Once()
		}

		// STM pulls a whole candidate batch via repeated Peek() calls and may probe once more
		// after the pool is empty (harmless in production - the batch just comes back empty), so
		// this must tolerate any number of further calls rather than exactly one.
		txPool.On("Peek").Return((*types.Transaction)(nil))

		block, receipts, err := buildBlock(parentBlockHeader, testBlockchain, executor, txPool)

		require.NoError(t, err)
		require.NotNil(t, block)
		require.NotNil(t, receipts)
		require.Equal(t, parentBlockHeader.Number+1, block.Header.Number)
		require.Equal(t, parentBlockHeader.Hash.String(), block.Header.ParentHash.String())

		checkExtraData(t, block.Header.ExtraData, [][]uint64{{}, {}, {0}, {1}, {2}})

		// check values directly
		tran, err = executor.BeginTxn(block.Header.StateRoot, block.Header, types.ZeroAddress)
		require.NoError(t, err)

		// wei balance receiver[0]
		require.Equal(t, big.NewInt(600), tran.GetBalance(receivers[0]))
		require.Equal(t, big.NewInt(150), tran.GetBalance(receivers[1]))
		require.Equal(t, big.NewInt(250), tran.GetBalance(receivers[2]))
	})
}
