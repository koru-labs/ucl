package txrelayerv2

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/Ethernal-Tech/ethgo"
)

const (
	defaultGasPrice            = 1879048192 // 0x70000000
	DefaultGasLimit            = 5242880    // 0x500000
	DefaultRPCAddress          = "http://127.0.0.1:8545"
	defaultNumRetries          = 1000
	gasLimitIncreasePercentage = 100
	feeIncreasePercentage      = 100
)

var (
	errNoAccounts     = errors.New("no accounts registered")
	errMethodNotFound = errors.New("method not found")

	// dynamicFeeTxFallbackErrs represents known errors which are the reason to fallback
	// from sending dynamic fee tx to legacy tx
	dynamicFeeTxFallbackErrs = []error{types.ErrTxTypeNotSupported, errMethodNotFound}
)

type TxRelayer interface {
	// Call executes a message call immediately without creating a transaction on the blockchain
	Call(from types.Address, to types.Address, input []byte) (string, error)
	// SendTransaction signs given transaction by provided key and sends it to the blockchain
	SendTransaction(txn *types.Transaction, key crypto.Key) (*ethgo.Receipt, error)
	// SendTransactionLocal sends non-signed transaction
	// (this function is meant only for testing purposes and is about to be removed at some point)
	SendTransactionLocal(txn *types.Transaction) (*ethgo.Receipt, error)
	// Client returns jsonrpc client
	Client() *jsonrpc.EthClient
	// GetTxnHashes returns hashes of sent transactions
	GetTxnHashes() []types.Hash
}

var _ TxRelayer = (*TxRelayerImpl)(nil)

type TxRelayerImpl struct {
	ipAddress           string
	client              *jsonrpc.EthClient
	receiptTimeout      time.Duration
	numRetries          int
	estimateGasFallback bool
	collectTxnHashes    bool
	txnHashes           []types.Hash

	nonceGet bool

	lock sync.Mutex

	writer io.Writer
}

func NewTxRelayer(opts ...TxRelayerOption) (TxRelayer, error) {
	t := &TxRelayerImpl{
		ipAddress:      DefaultRPCAddress,
		receiptTimeout: 50 * time.Millisecond,
		numRetries:     defaultNumRetries,
		nonceGet:       true,
	}
	for _, opt := range opts {
		opt(t)
	}

	if t.client == nil {
		client, err := jsonrpc.NewEthClient(t.ipAddress)
		if err != nil {
			return nil, err
		}

		t.client = client
	}

	return t, nil
}

// GetTxnHashes returns hashes of sent transactions
func (t *TxRelayerImpl) GetTxnHashes() []types.Hash {
	return t.txnHashes
}

// Call executes a message call immediately without creating a transaction on the blockchain
func (t *TxRelayerImpl) Call(from types.Address, to types.Address, input []byte) (string, error) {
	callMsg := &jsonrpc.CallMsg{
		From: from,
		To:   &to,
		Data: input,
	}

	return t.client.Call(callMsg, jsonrpc.PendingBlockNumber, nil)
}

// SendTransaction signs given transaction by provided key and sends it to the blockchain
func (t *TxRelayerImpl) SendTransaction(txn *types.Transaction, key crypto.Key) (*ethgo.Receipt, error) {
	txnHash, err := t.sendTransactionLocked(txn, key)
	if err != nil {
		if txn.Type != types.LegacyTx {
			for _, fallbackErr := range dynamicFeeTxFallbackErrs {
				if strings.Contains(
					strings.ToLower(err.Error()),
					strings.ToLower(fallbackErr.Error())) {
					// "downgrade" transaction to the legacy tx type and resend it
					txn.Type = types.LegacyTx
					txn.GasPrice = big.NewInt(0)

					return t.SendTransaction(txn, key)
				}
			}
		}

		return nil, err
	}

	if t.collectTxnHashes {
		t.txnHashes = append(t.txnHashes, txnHash)
	}

	return t.waitForReceipt(txnHash)
}

// Client returns jsonrpc client
func (t *TxRelayerImpl) Client() *jsonrpc.EthClient {
	return t.client
}

func (t *TxRelayerImpl) sendTransactionLocked(txn *types.Transaction, key crypto.Key) (types.Hash, error) {
	t.lock.Lock()
	defer t.lock.Unlock()

	nonce, err := t.client.GetNonce(key.Address(), jsonrpc.PendingBlockNumberOrHash)
	if err != nil {
		return types.ZeroHash, fmt.Errorf("failed to get nonce: %w", err)
	}

	chainID, err := t.client.ChainID()
	if err != nil {
		return types.ZeroHash, err
	}

	txn.ChainID = chainID
	txn.Nonce = nonce

	if txn.From == types.ZeroAddress {
		txn.From = key.Address()
	}

	if txn.Type == types.DynamicFeeTx {
		maxPriorityFee := txn.GasTipCap
		if maxPriorityFee == nil {
			// retrieve the max priority fee per gas
			if maxPriorityFee, err = t.Client().MaxPriorityFeePerGas(); err != nil {
				return types.ZeroHash, fmt.Errorf("failed to get max priority fee per gas: %w", err)
			}

			// set retrieved max priority fee per gas increased by certain percentage
			compMaxPriorityFee := new(big.Int).Mul(maxPriorityFee, big.NewInt(feeIncreasePercentage))
			compMaxPriorityFee = compMaxPriorityFee.Div(compMaxPriorityFee, big.NewInt(100))
			txn.GasTipCap = new(big.Int).Add(maxPriorityFee, compMaxPriorityFee)
		}

		if txn.GasFeeCap == nil {
			// retrieve the latest base fee
			feeHist, err := t.Client().FeeHistory(1, jsonrpc.LatestBlockNumber, nil)
			if err != nil {
				return types.ZeroHash, fmt.Errorf("failed to get fee history: %w", err)
			}

			baseFee := big.NewInt(0)

			if len(feeHist.BaseFee) != 0 {
				baseFee = baseFee.SetUint64(feeHist.BaseFee[len(feeHist.BaseFee)-1])
			}

			// set max fee per gas as sum of base fee and max priority fee
			// (increased by certain percentage)
			maxFeePerGas := new(big.Int).Add(baseFee, maxPriorityFee)
			compMaxFeePerGas := new(big.Int).Mul(maxFeePerGas, big.NewInt(feeIncreasePercentage))
			compMaxFeePerGas = compMaxFeePerGas.Div(compMaxFeePerGas, big.NewInt(100))
			txn.GasFeeCap = new(big.Int).Add(maxFeePerGas, compMaxFeePerGas)
		}
	} else if txn.GasPrice != nil || txn.GasPrice.Uint64() == 0 {
		gasPrice, err := t.Client().GasPrice()
		if err != nil {
			return types.ZeroHash, fmt.Errorf("failed to get gas price: %w", err)
		}

		gasPriceBigInt := new(big.Int).SetUint64(gasPrice + (gasPrice * feeIncreasePercentage / 100))
		txn.GasPrice = gasPriceBigInt
	}

	if txn.Gas == 0 {
		gasLimit, err := t.client.EstimateGas(ConvertTxnToCallMsg(txn))
		if err != nil {
			if !t.estimateGasFallback {
				return types.ZeroHash, fmt.Errorf("failed to estimate gas: %w", err)
			}

			gasLimit = DefaultGasLimit
		} else {
			txn.Gas = gasLimit + (gasLimit * gasLimitIncreasePercentage / 100)
		}

		txn.Gas = gasLimit
	}

	signer := crypto.NewLondonSigner(chainID.Uint64(), true, crypto.NewEIP155Signer(chainID.Uint64(), true))

	if txn, err = signer.SignTx(txn, key.PrivateKey()); err != nil {
		return types.ZeroHash, err
	}

	data := txn.MarshalRLPTo(nil)
	if err != nil {
		return types.ZeroHash, err
	}

	if t.writer != nil {
		var msg string

		if txn.Type == types.DynamicFeeTx {
			msg = fmt.Sprintf("[TxRelayer.SendTransaction]\nFrom = %s\nGas = %d\n"+
				"Max Fee Per Gas = %d\nMax Priority Fee Per Gas = %d\n",
				txn.From, txn.Gas, txn.GasFeeCap, txn.GasTipCap)
		} else {
			msg = fmt.Sprintf("[TxRelayer.SendTransaction]\nFrom = %s\nGas = %d\nGas Price = %d\n",
				txn.From, txn.Gas, txn.GasPrice)
		}

		_, _ = t.writer.Write([]byte(msg))
	}

	return t.client.SendRawTransaction(data)
}

// SendTransactionLocal sends non-signed transaction
// (this function is meant only for testing purposes and is about to be removed at some point)
func (t *TxRelayerImpl) SendTransactionLocal(txn *types.Transaction) (*ethgo.Receipt, error) {
	txnHash, err := t.sendTransactionLocalLocked(txn)
	if err != nil {
		return nil, err
	}

	if t.collectTxnHashes {
		t.txnHashes = append(t.txnHashes, txnHash)
	}

	return t.waitForReceipt(txnHash)
}

func (t *TxRelayerImpl) sendTransactionLocalLocked(txn *types.Transaction) (types.Hash, error) {
	t.lock.Lock()
	defer t.lock.Unlock()

	accounts, err := t.client.Accounts()
	if err != nil {
		return types.ZeroHash, err
	}

	if len(accounts) == 0 {
		return types.ZeroHash, errNoAccounts
	}

	txn.From = accounts[0]

	gasLimit, err := t.client.EstimateGas(ConvertTxnToCallMsg(txn))
	if err != nil {
		return types.ZeroHash, err
	}

	txn.Gas = gasLimit
	txn.GasPrice = new(big.Int).SetUint64(defaultGasPrice)

	return t.client.SendTransaction(txn)
}

func (t *TxRelayerImpl) waitForReceipt(hash types.Hash) (*ethgo.Receipt, error) {
	// A negative numRetries means we don't want to receive the receipt after SendTransaction/SendTransactionLocal calls
	if t.numRetries < 0 {
		return nil, nil
	}

	for count := 0; count < t.numRetries; count++ {
		receipt, err := t.client.GetTransactionReceipt(hash)
		if err != nil {
			if err.Error() != "not found" {
				return nil, err
			}
		}

		if receipt != nil {
			return receipt, nil
		}

		time.Sleep(t.receiptTimeout)
	}

	return nil, fmt.Errorf("timeout while waiting for transaction %s to be processed", hash)
}

// ConvertTxnToCallMsg converts txn instance to call message
func ConvertTxnToCallMsg(txn *types.Transaction) *jsonrpc.CallMsg {
	var (
		gasPrice  *big.Int
		gasFeeCap *big.Int
		gasTipCap *big.Int
	)

	if txn.Type != types.DynamicFeeTx {
		if txn.GasPrice != nil {
			gasPrice = new(big.Int).Set(txn.GasPrice)
		}
	} else {
		gasFeeCap = txn.GasFeeCap
		gasTipCap = txn.GasTipCap
	}

	return &jsonrpc.CallMsg{
		From:      txn.From,
		To:        txn.To,
		Data:      txn.Input,
		GasPrice:  gasPrice,
		GasFeeCap: gasFeeCap,
		GasTipCap: gasTipCap,
		Value:     txn.Value,
		Gas:       txn.Gas,
	}
}

type TxRelayerOption func(*TxRelayerImpl)

func WithClient(client *jsonrpc.EthClient) TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.client = client
	}
}

func WithIPAddress(ipAddress string) TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.ipAddress = ipAddress
	}
}

func WithReceiptTimeout(receiptTimeout time.Duration) TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.receiptTimeout = receiptTimeout
	}
}

func WithWriter(writer io.Writer) TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.writer = writer
	}
}

// WithNumRetries sets the maximum number of eth_getTransactionReceipt retries
// before considering the transaction sending as timed out. Set to -1 to disable
// waitForReceipt and not wait for the transaction receipt
func WithNumRetries(numRetries int) TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.numRetries = numRetries
	}
}

func WithoutNonceGet() TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.nonceGet = false
	}
}

func WithCollectTxnHashes() TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.collectTxnHashes = true
	}
}

func WithEstimateGasFallback() TxRelayerOption {
	return func(t *TxRelayerImpl) {
		t.estimateGasFallback = true
	}
}
