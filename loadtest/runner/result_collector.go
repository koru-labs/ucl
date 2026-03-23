package runner

import (
	"context"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
)

// VUTxnCount represents the number of transactions sent by a virtual user
// in one iteration of send transactions loop
type VUTxnCount struct {
	VU      string
	TxCount int
}

// ResultCollector collects the results of the load test.
type ResultCollector struct {
	BalanceReadCountCh chan struct{}
	BalanceReadErrorCh chan error
	BalanceReadCount   int
	BalanceReadErrors  []error

	NonceReadCountCh chan struct{}
	NonceReadErrorCh chan error
	NonceReadCount   int
	NonceReadErrors  []error

	TxPoolStatusReadCountCh chan struct{}
	TxPoolStatusReadErrorCh chan error
	TxPoolStatusReadCount   int
	TxPoolStatusReadErrors  []error

	CodeReadCountCh chan struct{}
	CodeReadErrorCh chan error
	CodeReadCount   int
	CodeReadErrors  []error

	VUTxnCountCh chan VUTxnCount
	VUTxns       map[string]int
}

// NewResultCollector creates a new ResultCollector instance.
func NewResultCollector(cfg LoadTestConfig) *ResultCollector {
	return &ResultCollector{
		BalanceReadCountCh:      make(chan struct{}, 3000),
		BalanceReadErrorCh:      make(chan error, 3000),
		NonceReadCountCh:        make(chan struct{}, 3000),
		NonceReadErrorCh:        make(chan error, 3000),
		TxPoolStatusReadCountCh: make(chan struct{}, 3000),
		TxPoolStatusReadErrorCh: make(chan error, 3000),
		CodeReadCountCh:         make(chan struct{}, 3000),
		CodeReadErrorCh:         make(chan error, 3000),
		VUTxnCountCh:            make(chan VUTxnCount, 3000),
		VUTxns:                  make(map[string]int, cfg.VUs),
	}
}

// CollectResults collects the results of the load test.
func (r *ResultCollector) CollectResults(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.BalanceReadCountCh:
			r.BalanceReadCount++
		case err := <-r.BalanceReadErrorCh:
			r.BalanceReadErrors = append(r.BalanceReadErrors, err)
		case <-r.NonceReadCountCh:
			r.NonceReadCount++
		case err := <-r.NonceReadErrorCh:
			r.NonceReadErrors = append(r.NonceReadErrors, err)
		case <-r.TxPoolStatusReadCountCh:
			r.TxPoolStatusReadCount++
		case err := <-r.TxPoolStatusReadErrorCh:
			r.TxPoolStatusReadErrors = append(r.TxPoolStatusReadErrors, err)
		case <-r.CodeReadCountCh:
			r.CodeReadCount++
		case err := <-r.CodeReadErrorCh:
			r.CodeReadErrors = append(r.CodeReadErrors, err)
		case vuTxns := <-r.VUTxnCountCh:
			r.VUTxns[vuTxns.VU] += vuTxns.TxCount
		}
	}
}

// PrintResults prints the results of the load test.
func (r *ResultCollector) PrintResults() {
	fmt.Println("=============================================================")
	fmt.Println("VUs transaction count:")

	table := tablewriter.NewWriter(os.Stdout)
	table.Header([]string{"VU", "Num of Sent Transactions"})

	for vu, txCount := range r.VUTxns {
		if err := table.Append([]string{vu, fmt.Sprintf("%d", txCount)}); err != nil {
			fmt.Println("table append error", err)
		}
	}

	if err := table.Render(); err != nil {
		fmt.Println("table render error", err)
	}

	fmt.Println("=============================================================")
	fmt.Println("Total balance read count:", r.BalanceReadCount)
	fmt.Println("Total nonce read count:", r.NonceReadCount)
	fmt.Println("Total tx pool status read count:", r.TxPoolStatusReadCount)
	fmt.Println("Total code read count:", r.CodeReadCount)

	if len(r.BalanceReadErrors) > 0 {
		fmt.Println("====================================")
		fmt.Println("Balance read errors:")

		for i, err := range r.BalanceReadErrors {
			fmt.Printf("%d: %v\n", i, err)
		}
	}

	if len(r.NonceReadErrors) > 0 {
		fmt.Println("====================================")
		fmt.Println("Nonce read errors:")

		for i, err := range r.NonceReadErrors {
			fmt.Printf("%d: %v\n", i, err)
		}
	}

	if len(r.TxPoolStatusReadErrors) > 0 {
		fmt.Println("====================================")
		fmt.Println("Tx pool status read errors:")

		for i, err := range r.TxPoolStatusReadErrors {
			fmt.Printf("%d: %v\n", i, err)
		}
	}

	if len(r.CodeReadErrors) > 0 {
		fmt.Println("====================================")
		fmt.Println("Code read errors:")

		for i, err := range r.CodeReadErrors {
			fmt.Printf("%d: %v\n", i, err)
		}
	}
}
