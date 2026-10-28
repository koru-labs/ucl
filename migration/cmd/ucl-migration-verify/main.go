// ucl-migration-verify checks a pitau node's copy of ucl's history against a ucl archive, with
// ucl's own code (package migration/verify; pitau's docs/plans/ucl-migration.md §7.5).
//
//	ucl-migration-verify --pitau http://pitau-archive:8545 --ucl http://ucl-archive:8545 \
//	    --to <H> [--from 0] [--validators 0xa,0xb,…] [--workers 8] [--report report.json]
//
// It exits 0 when every check held, 1 when one did not, and 2 when it could not ask.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/0xPolygon/polygon-edge/migration/verify"
	"github.com/0xPolygon/polygon-edge/types"
)

func main() {
	pitau := flag.String("pitau", "", "a pitau archive's JSON-RPC")
	ucl := flag.String("ucl", "", "a ucl archive's JSON-RPC, with the export endpoint (ucl_exportBlocks)")
	from := flag.Uint64("from", 0, "the first block to check")
	to := flag.Uint64("to", 0, "the last block to check: the cutover block H")
	validators := flag.String("validators", "", "ucl's validators whose quorum sealed --to, comma-separated (default: block 0's)")
	workers := flag.Int("workers", 8, "blocks checked at once")
	report := flag.String("report", "", "write the report here as JSON")
	flag.Parse()

	if *pitau == "" || *ucl == "" || *to == 0 {
		flag.Usage()
		os.Exit(2)
	}

	cfg := verify.Config{Pitau: *pitau, Ucl: *ucl, From: *from, To: *to, Workers: *workers}

	for _, v := range strings.Split(*validators, ",") {
		if v = strings.TrimSpace(v); v != "" {
			cfg.Validators = append(cfg.Validators, types.StringToAddress(v))
		}
	}

	result, err := verify.Run(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ucl-migration-verify: %v\n", err)
		os.Exit(2)
	}

	if *report != "" {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ucl-migration-verify: %v\n", err)
			os.Exit(2)
		}

		if err := os.WriteFile(*report, append(data, '\n'), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "ucl-migration-verify: %v\n", err)
			os.Exit(2)
		}
	}

	fmt.Printf("blocks %d..%d: %d checked, %d transactions, %d accounts and %d slots compared at each block and its parent\n",
		result.From, result.To, result.Blocks, result.Transactions, result.Accounts, result.Slots)
	fmt.Printf("block %d: hash %s, state root %s, sealed by %d of %d validators\n",
		result.To, result.Hash, result.StateRoot, len(result.SealedBy), len(result.Validators))

	if result.OK() {
		fmt.Println("every check held")

		return
	}

	for _, f := range result.Failures {
		fmt.Printf("FAILED block %d %s: %s\n", f.Block, f.Check, f.Detail)
	}

	os.Exit(1)
}
