package ibft

import (
	"encoding/hex"
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
		}

		return i.buildBlock(parentHeader)
	}

	t.Run("Test smart contract", func(t *testing.T) {
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

		// sc deploy
		deploySCInput, err := hex.DecodeString("608060405234801561001057600080fd5b50610518806100206000396000f3fe608060405234801561001057600080fd5b50600436106100625760003560e01c806327e235e31461006757806361bc221a1461009757806366e7ea0f146100b557806381da0287146100d1578063aba00859146100ed578063ad7a672f14610109575b600080fd5b610081600480360381019061007c9190610336565b610127565b60405161008e919061037c565b60405180910390f35b61009f61013f565b6040516100ac919061037c565b60405180910390f35b6100cf60048036038101906100ca91906103c3565b610145565b005b6100eb60048036038101906100e69190610336565b6101b6565b005b610107600480360381019061010291906103c3565b610211565b005b6101116102cd565b60405161011e919061037c565b60405180910390f35b60026020528060005260406000206000915090505481565b60005481565b80600260008473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200190815260200160002060008282546101949190610432565b925050819055506000808154809291906101ad90610466565b91905055505050565b600260008273ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200190815260200160002054600160008282546102079190610432565b9250508190555050565b6000600260008473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020016000205490508181101561026257600080fd5b818161026e91906104ae565b600260008573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff168152602001908152602001600020819055506000808154809291906102c390610466565b9190505550505050565b60015481565b600080fd5b600073ffffffffffffffffffffffffffffffffffffffff82169050919050565b6000610303826102d8565b9050919050565b610313816102f8565b811461031e57600080fd5b50565b6000813590506103308161030a565b92915050565b60006020828403121561034c5761034b6102d3565b5b600061035a84828501610321565b91505092915050565b6000819050919050565b61037681610363565b82525050565b6000602082019050610391600083018461036d565b92915050565b6103a081610363565b81146103ab57600080fd5b50565b6000813590506103bd81610397565b92915050565b600080604083850312156103da576103d96102d3565b5b60006103e885828601610321565b92505060206103f9858286016103ae565b9150509250929050565b7f4e487b7100000000000000000000000000000000000000000000000000000000600052601160045260246000fd5b600061043d82610363565b915061044883610363565b92508282019050808211156104605761045f610403565b5b92915050565b600061047182610363565b91507fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff82036104a3576104a2610403565b5b600182019050919050565b60006104b982610363565b91506104c483610363565b92508282039050818111156104dc576104db610403565b5b9291505056fea26469706673582212204d37d27c6b771a6ce6f364880b2af42fe92c0532782adedbe14566b617c066c764736f6c63430008130033")
		require.NoError(t, err)

		deployTx, err := txSigner.SignTx(&types.Transaction{
			Nonce:    0,
			From:     keys[0].Address(),
			To:       nil,
			Value:    big.NewInt(0),
			Gas:      1000000,
			GasPrice: big.NewInt(10000),
			Input:    deploySCInput,
		}, keys[0].PrivateKey())
		require.NoError(t, err)

		deployTxResult, err := tran.Apply(deployTx)
		require.NoError(t, err)
		require.NoError(t, deployTxResult.Err)

		_, rootTrie, err := tran.Commit()
		require.NoError(t, err)

		scAddress := deployTxResult.Address
		parentBlockHeader.StateRoot = rootTrie
		txs := [4]*types.Transaction{}
		inputs := [4][]byte{}

		// incBalance 2222222222222222222222222222222222222222
		inputs[0], err = hex.DecodeString("66e7ea0f000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000003e8")
		require.NoError(t, err)
		// incBalance 1122222222222222222222222222222222222222
		inputs[1], err = hex.DecodeString("66e7ea0f000000000000000000000000112222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000003e8")
		require.NoError(t, err)
		// decBalance 2222222222222222222222222222222222222222
		inputs[2], err = hex.DecodeString("aba00859000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000001ff")
		require.NoError(t, err)
		// updateTotalBalance 2222222222222222222222222222222222222211
		inputs[3], err = hex.DecodeString("81da02870000000000000000000000001122222222222222222222222222222222222222")
		require.NoError(t, err)

		for i, inp := range inputs {
			txs[i], err = txSigner.SignTx(&types.Transaction{
				Nonce:    0,
				From:     keys[i+1].Address(),
				To:       &scAddress,
				Value:    big.NewInt(0),
				Gas:      1000000,
				GasPrice: big.NewInt(10000),
				Input:    inp,
			}, keys[i+1].PrivateKey())
			require.NoError(t, err)
		}

		txPool := &txPoolMock{}
		txPool.On("Prepare", mock.Anything).Run(func(args mock.Arguments) {})
		txPool.On("Length", mock.Anything).Return(uint64(0))

		for _, tx := range txs {
			txPool.On("Peek").Return(tx).Once()
			txPool.On("Pop", mock.Anything).Run(func(args mock.Arguments) {}).Once()
		}

		txPool.On("Peek").Return((*types.Transaction)(nil)).Once()

		block, receipts, err := buildBlock(parentBlockHeader, testBlockchain, executor, txPool)

		require.NoError(t, err)
		require.NotNil(t, block)
		require.NotNil(t, receipts)
		require.Equal(t, parentBlockHeader.Number+1, block.Header.Number)
		require.Equal(t, parentBlockHeader.Hash.String(), block.Header.ParentHash.String())
		require.NoError(t, parentExtraData.UnmarshalRLP(block.Header.ExtraData[signer.IstanbulExtraVanity:]))
		assert.Equal(t, [][]uint64{{}, {0}, {1}, {1}}, parentExtraData.TxDependency)

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

		slot := make([]byte, 32)
		slot[31] = 2

		// balances[1122...]
		slotHash := crypto.Keccak256(append(addr1, slot...))
		value := tran.GetStorage(scAddress, types.Hash(slotHash))
		assert.Equal(t, big.NewInt(1000), new(big.Int).SetBytes(value.Bytes()))
		// balances[222...]
		slotHash = crypto.Keccak256(append(addr2, slot...))
		value = tran.GetStorage(scAddress, types.Hash(slotHash))
		assert.Equal(t, big.NewInt(489), new(big.Int).SetBytes(value.Bytes()))
		// total balance
		slot[31] = 1
		value = tran.GetStorage(scAddress, types.Hash(slot))
		assert.Equal(t, big.NewInt(1000), new(big.Int).SetBytes(value.Bytes()))
		// counter
		slot[31] = 0
		value = tran.GetStorage(scAddress, types.Hash(slot))
		assert.Equal(t, big.NewInt(3), new(big.Int).SetBytes(value.Bytes()))
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

		txPool.On("Peek").Return((*types.Transaction)(nil)).Once()

		block, receipts, err := buildBlock(parentBlockHeader, testBlockchain, executor, txPool)

		require.NoError(t, err)
		require.NotNil(t, block)
		require.NotNil(t, receipts)
		require.Equal(t, parentBlockHeader.Number+1, block.Header.Number)
		require.Equal(t, parentBlockHeader.Hash.String(), block.Header.ParentHash.String())
		require.NoError(t, parentExtraData.UnmarshalRLP(block.Header.ExtraData[signer.IstanbulExtraVanity:]))
		assert.Equal(t, [][]uint64{{}, {}, {0}, {1}, {2}}, parentExtraData.TxDependency)

		// check values directly
		tran, err = executor.BeginTxn(block.Header.StateRoot, block.Header, types.ZeroAddress)
		require.NoError(t, err)

		// wei balance receiver[0]
		require.Equal(t, big.NewInt(600), tran.GetBalance(receivers[0]))
		require.Equal(t, big.NewInt(150), tran.GetBalance(receivers[1]))
		require.Equal(t, big.NewInt(250), tran.GetBalance(receivers[2]))
	})
}
