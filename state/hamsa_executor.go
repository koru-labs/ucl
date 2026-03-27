package state

import (
	"fmt"

	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/types"
)

func (t *Transition) Write3(txn *types.Transaction) (*types.Receipt, error) {
	// ✅ Note: From address recovery and nonce check are unified in consensus_backend.go
	//    apply() method in Apply will automatically check nonce and increase nonce

	// Make a local copy and apply the transaction
	msg := txn.Copy() // make a local copy and apply the transaction to prevent subsequent modifications
	// call Apply to execute the transaction, return the execution result
	// apply() method in Apply will check nonce and automatically increase nonce
	result, e := t.Apply(msg)
	if e != nil {
		t.logger.Error("failed to apply tx", "err", e)

		return nil, e
	}

	// TODO: hamsa removed
	// t.TotalGas += result.GasUsed

	logs := t.state.Logs()

	receipt := &types.Receipt{
		CumulativeGasUsed: t.totalGas,
		TransactionType:   txn.Type,
		TxHash:            txn.Hash,
		GasUsed:           result.GasUsed,
	}

	// The suicided accounts are set as deleted for the next iteration
	if err := t.state.CleanDeleteObjects(true); err != nil {
		return nil, fmt.Errorf("failed to clean deleted objects: %w", err)
	}

	if result.Failed() {
		receipt.SetStatus(types.ReceiptFailed)
	} else {
		receipt.SetStatus(types.ReceiptSuccess)
	}

	// if the transaction created a contract, store the creation address in the receipt.
	if msg.To == nil {
		receipt.ContractAddress = crypto.CreateAddress(msg.From, txn.Nonce).Ptr()
	}

	// Set the receipt logs and create a bloom for filtering
	receipt.Logs = logs
	receipt.LogsBloom = types.CreateBloom([]*types.Receipt{receipt})

	return receipt, nil
}
