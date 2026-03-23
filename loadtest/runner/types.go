package runner

import (
	"github.com/0xPolygon/polygon-edge/consensus/polybft/wallet"
	"github.com/0xPolygon/polygon-edge/jsonrpc"
	"github.com/0xPolygon/polygon-edge/types"
)

// ethClientList is a list of EthClients
type ethClientList []*jsonrpc.EthClient

// newEthClientList creates a new list of EthClients from the given JSON-RPC URLs
func newEthClientList(jsonRPCURLs []string) (ethClientList, error) {
	clients := make(ethClientList, 0, len(jsonRPCURLs))

	for _, url := range jsonRPCURLs {
		client, err := jsonrpc.NewEthClient(url)
		if err != nil {
			return nil, err
		}

		clients = append(clients, client)
	}

	return clients, nil
}

// close closes all the EthClients in the list
func (ecl ethClientList) close() error {
	for _, client := range ecl {
		if err := client.Close(); err != nil {
			return err
		}
	}

	return nil
}

// getClientForAccount returns an EthClient from the list of clients for the given account index
func (ecl ethClientList) getClientForAccount(accountIndex int) *jsonrpc.EthClient {
	return ecl[accountIndex%len(ecl)]
}

// getClient returns the first EthClient from the list of clients
func (ecl ethClientList) getClient() *jsonrpc.EthClient {
	return ecl[0]
}

// receiversList is a list of receiver addresses for tokens in a load test
type receiversList []types.Address

// newReceiversList creates a new list of receivers from the given number of receivers
func newReceiversList(receiversNum int) (receiversList, error) {
	receivers := make(receiversList, receiversNum)

	for i := 0; i < receiversNum; i++ {
		acc, err := wallet.GenerateAccount()
		if err != nil {
			return nil, err
		}

		receivers[i] = acc.Address()
	}

	return receivers, nil
}

// getReceiverForSender returns a receiver from the list of receivers for the given sender index
func (rl receiversList) getReceiverForSender(index int) *types.Address {
	return &rl[index%len(rl)]
}

// getReceiver returns the first receiver from the list of receivers
func (rl receiversList) getReceiver() types.Address {
	return rl[0]
}

// batchSendersList is a list of TransactionBatchSenders
type batchSendersList []*TransactionBatchSender

// newBatchSenders creates a new list of TransactionBatchSenders from the given EthClients
func newBatchSenders(jsonRPCURLs []string) batchSendersList {
	senders := make(batchSendersList, len(jsonRPCURLs))

	for i, url := range jsonRPCURLs {
		senders[i] = newTransactionBatchSender(url)
	}

	return senders
}

// getBatchSenderForAccount returns a TransactionBatchSender
// from the list of senders for the given account index
func (bs batchSendersList) getBatchSenderForAccount(accountIndex int) *TransactionBatchSender {
	return bs[accountIndex%len(bs)]
}

// getBatchSender returns the first TransactionBatchSender from the list of senders
func (bs batchSendersList) getBatchSender() *TransactionBatchSender {
	return bs[0]
}
