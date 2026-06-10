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
		deploySCInput, err := hex.DecodeString("608060405234801561001057600080fd5b50610472806100206000396000f3fe608060405234801561001057600080fd5b50600436106100575760003560e01c806327e235e31461005c57806366e7ea0f1461008c57806381da0287146100a8578063aba00859146100c4578063ad7a672f146100e0575b600080fd5b610076600480360381019061007191906102d8565b6100fe565b604051610083919061031e565b60405180910390f35b6100a660048036038101906100a19190610365565b610116565b005b6100c260048036038101906100bd91906102d8565b610170565b005b6100de60048036038101906100d99190610365565b6101ca565b005b6100e861026f565b6040516100f5919061031e565b60405180910390f35b60016020528060005260406000206000915090505481565b80600160008473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff168152602001908152602001600020600082825461016591906103d4565b925050819055505050565b600160008273ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff168152602001908152602001600020546000808282546101c091906103d4565b9250508190555050565b6000600160008473ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff1681526020019081526020016000205490508181101561021b57600080fd5b81816102279190610408565b600160008573ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff16815260200190815260200160002081905550505050565b60005481565b600080fd5b600073ffffffffffffffffffffffffffffffffffffffff82169050919050565b60006102a58261027a565b9050919050565b6102b58161029a565b81146102c057600080fd5b50565b6000813590506102d2816102ac565b92915050565b6000602082840312156102ee576102ed610275565b5b60006102fc848285016102c3565b91505092915050565b6000819050919050565b61031881610305565b82525050565b6000602082019050610333600083018461030f565b92915050565b61034281610305565b811461034d57600080fd5b50565b60008135905061035f81610339565b92915050565b6000806040838503121561037c5761037b610275565b5b600061038a858286016102c3565b925050602061039b85828601610350565b9150509250929050565b7f4e487b7100000000000000000000000000000000000000000000000000000000600052601160045260246000fd5b60006103df82610305565b91506103ea83610305565b9250828201905080821115610402576104016103a5565b5b92915050565b600061041382610305565b915061041e83610305565b9250828203905081811115610436576104356103a5565b5b9291505056fea26469706673582212205cce55ae40a73cbf2c29f6f4a4e3d60a1c556e8fbfabb4b79c2588dafcea67f364736f6c63430008130033")
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
		txs := [5]*types.Transaction{}
		inputs := [5][]byte{}

		// incBalance 2222222222222222222222222222222222222222
		inputs[0], err = hex.DecodeString("66e7ea0f000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000003e8")
		require.NoError(t, err)
		// incBalance 1122222222222222222222222222222222222222
		inputs[1], err = hex.DecodeString("66e7ea0f000000000000000000000000112222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000003e8")
		require.NoError(t, err)
		// decBalance 2222222222222222222222222222222222222222
		inputs[2], err = hex.DecodeString("aba00859000000000000000000000000222222222222222222222222222222222222222200000000000000000000000000000000000000000000000000000000000001ff")
		require.NoError(t, err)
		// updateTotalBalance 1122222222222222222222222222222222222222
		inputs[3], err = hex.DecodeString("81da02870000000000000000000000001122222222222222222222222222222222222222")
		require.NoError(t, err)
		// updateTotalBalance 1112222222222222222222222222222222222222
		inputs[4], err = hex.DecodeString("81da02870000000000000000000000001112222222222222222222222222222222222222")
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
		assert.Equal(t, [][]uint64{{}, {}, {0}, {1}, {3}}, parentExtraData.TxDependency)

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
		slot[31] = 1

		// balances[1122...]
		slotHash := crypto.Keccak256(append(addr1, slot...))
		value := tran.GetStorage(scAddress, types.Hash(slotHash))
		assert.Equal(t, big.NewInt(1000), new(big.Int).SetBytes(value.Bytes()))
		// balances[222...]
		slotHash = crypto.Keccak256(append(addr2, slot...))
		value = tran.GetStorage(scAddress, types.Hash(slotHash))
		assert.Equal(t, big.NewInt(489), new(big.Int).SetBytes(value.Bytes()))
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
