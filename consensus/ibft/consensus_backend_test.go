package ibft

import (
	"math/big"
	"testing"
	"time"

	"github.com/0xPolygon/polygon-edge/blockchain"
	"github.com/0xPolygon/polygon-edge/chain"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/hook"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/state"
	itrie "github.com/0xPolygon/polygon-edge/state/immutable-trie"
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

	keys := [10]*crypto.ECDSAKey{}
	receivers := [10]types.Address{}

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
	validators := validators.NewECDSAValidatorSet(
		validators.NewECDSAValidator(keys[0].Address()),
		validators.NewECDSAValidator(types.StringToAddress("1")),
		validators.NewECDSAValidator(types.StringToAddress("2")),
		validators.NewECDSAValidator(types.StringToAddress("3")),
	)
	parentExtraData := &signer.IstanbulExtra{
		Validators:           validators,
		ParentCommittedSeals: &signer.SerializedSeal{},
		CommittedSeals:       &signer.AggregatedSeal{},
		RoundNumber:          &round,
		TxDependency:         [][]uint64{{1, 2, 5}, {}, {4}, {}, {3}},
	}

	forkManagerMock := &forkManagerMock{}
	forkManagerMock.On("GetValidators", mock.Anything).Return(validators)
	forkManagerMock.On("GetSigner", mock.Anything).Return(mySigner)
	forkManagerMock.On("GetHooks", mock.Anything).Return(&hook.Hooks{})

	parentBlockHeader := &types.Header{
		Number:     2,
		Hash:       types.Hash{0, 1, 2, 3, 4, 5},
		ParentHash: types.Hash{1, 3},
		GasLimit:   1_000_000_000_000,
		ExtraData:  append(make([]byte, signer.IstanbulExtraVanity), parentExtraData.MarshalRLPTo(nil)...),
	}
	testBlockChain := blockchain.NewTestBlockchain(t, []*types.Header{
		{Number: 1, Hash: types.Hash{1, 3}},
		parentBlockHeader,
	})
	forks := &chain.Forks{
		chain.Homestead: chain.NewFork(0),
		chain.Istanbul:  chain.NewFork(0),
		chain.London:    chain.NewFork(0),
	}
	chainParams := &chain.Params{
		ChainID:      100,
		Forks:        forks,
		BurnContract: map[uint64]types.Address{0: types.ZeroAddress},
	}
	txSigner := crypto.NewFrontierSigner(true)
	txs := [5]*types.Transaction{}
	err := error(nil)

	txs[0], err = txSigner.SignTx(&types.Transaction{
		Nonce:    0,
		From:     keys[9].Address(),
		To:       &receivers[0],
		Value:    big.NewInt(100),
		Gas:      1000000,
		GasPrice: big.NewInt(10000),
		Input:    []byte{},
	}, keys[9].PrivateKey())
	require.NoError(t, err)

	txs[1], err = txSigner.SignTx(&types.Transaction{
		Nonce:    0,
		From:     keys[1].Address(),
		To:       &receivers[1],
		Value:    big.NewInt(150),
		Gas:      1000000,
		GasPrice: big.NewInt(10000),
		Input:    []byte{},
	}, keys[1].PrivateKey())
	require.NoError(t, err)

	txs[2], err = txSigner.SignTx(&types.Transaction{
		Nonce:    0,
		From:     keys[2].Address(),
		To:       &receivers[0],
		Value:    big.NewInt(200),
		Gas:      1000000,
		GasPrice: big.NewInt(10000),
		Input:    []byte{},
	}, keys[2].PrivateKey())
	require.NoError(t, err)

	txs[3], err = txSigner.SignTx(&types.Transaction{
		Nonce:    1,
		From:     keys[1].Address(),
		To:       &receivers[2],
		Value:    big.NewInt(250),
		Gas:      1000000,
		GasPrice: big.NewInt(10000),
		Input:    []byte{},
	}, keys[1].PrivateKey())
	require.NoError(t, err)

	txs[4], err = txSigner.SignTx(&types.Transaction{
		Nonce:    0,
		From:     keys[3].Address(),
		To:       &receivers[0],
		Value:    big.NewInt(300),
		Gas:      1000000,
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

	txPool.On("Peek").Return((*types.Transaction)(nil)).Once()

	memStorage := itrie.NewMemoryStorage()

	executor := state.NewExecutor(chainParams, itrie.NewState(memStorage), hclog.NewNullLogger())
	executor.GetHash = testBlockChain.GetHashHelper

	tran, err := executor.BeginTxn(types.ZeroHash, parentBlockHeader, types.ZeroAddress)
	require.NoError(t, err)

	for _, k := range keys {
		require.NoError(t, tran.SetAccountDirectly(k.Address(), &chain.GenesisAccount{
			Balance: big.NewInt(1_000_000_000_000),
			Nonce:   0,
		}))
	}

	_, rootTrie, err := tran.Commit()
	require.NoError(t, err)

	parentBlockHeader.StateRoot = rootTrie

	i := &backendIBFT{
		forkManager: forkManagerMock,
		blockchain:  testBlockChain,
		executor:    executor,
		logger:      hclog.NewNullLogger(),
		txpool:      txPool,
		blockTime:   1 * time.Second,
	}

	block, receipts, err := i.buildBlock(parentBlockHeader)

	require.NoError(t, err)
	require.NotNil(t, block)
	require.NotNil(t, receipts)
	require.Equal(t, parentBlockHeader.Number+1, block.Header.Number)
	require.Equal(t, parentBlockHeader.Hash.String(), block.Header.ParentHash.String())
	require.NoError(t, parentExtraData.UnmarshalRLP(block.Header.ExtraData[signer.IstanbulExtraVanity:]))
	// why tx no 2 does not depend on tx 0?
	require.Equal(t, [][]uint64{{}, {}, {0}, {1}, {2}}, parentExtraData.TxDependency)
}
