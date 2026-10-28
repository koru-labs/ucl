// Package verify checks a pitau node's copy of ucl's history against a ucl archive, with ucl's own
// code: the independent verifier of the ucl to pitau migration (pitau's
// docs/plans/ucl-migration.md §7.5).
//
// pitau's follower checks every block it stores against ucl's header, in Rust. The verifier asks
// the same questions again from the other side, in Go and through ucl's types, of what a pitau
// node serves over JSON-RPC once it holds the history, so a follower bug that every validator
// shares is caught too. For every block from..to:
//
//   - pitau's stored header hashes, under ucl's IBFT header hash, to ucl's hash of the block, and
//     its parent hash is ucl's hash of the block before;
//   - pitau's transactions reproduce the header's transactions root, and are ucl's transactions;
//   - pitau's receipts reproduce the header's receipts root, under ucl's receipts formula;
//   - for every account and storage slot the block changed (ucl's export record names them), pitau
//     answers what the ucl archive answers, at the block and at its parent: balance, nonce, code,
//     storage. No root covers the values before a block (reverts), so this is their only check.
//
// And block to carries the committed seals of a quorum of ucl's validators.
package verify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0xPolygon/polygon-edge/consensus/ibft"
	"github.com/0xPolygon/polygon-edge/consensus/ibft/signer"
	"github.com/0xPolygon/polygon-edge/crypto"
	"github.com/0xPolygon/polygon-edge/helper/hex"
	"github.com/0xPolygon/polygon-edge/migration/export"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/0xPolygon/polygon-edge/types/buildroot"
	"github.com/0xPolygon/polygon-edge/validators"
)

// Config is what to verify.
type Config struct {
	// Pitau is a pitau node's JSON-RPC: an archive, to answer state at every old block.
	Pitau string
	// Ucl is a ucl archive's JSON-RPC with the export endpoint (ucl_exportBlocks).
	Ucl string
	// From and To are the first and last block to check; To is the cutover block H.
	From, To uint64
	// Validators are ucl's validators, of which a quorum must have sealed To. Empty: those block 0
	// names.
	Validators []types.Address
	// Workers is how many blocks are checked at once.
	Workers int
	// MaxFailures stops the run after this many failures; 0 means 100.
	MaxFailures int
	// Reconcile adds the balance reconciliation at To ([Reconciliation]).
	Reconcile bool
	// GenesisAccounts are the accounts ucl's genesis allocates, which no record names unless a
	// block changed them; reconciliation counts them too.
	GenesisAccounts []types.Address
}

// Reconciliation is the state at To in the form auditors read, from both chains side by side:
// every account the chain ever held (the genesis' and every one a block changed), their native
// total, the contracts among them, each ERC-20's supply against its holders' balances (holders:
// those accounts and every address in a Transfer log), and the largest accounts. An account or a
// token whose values differ between the chains is a failure.
type Reconciliation struct {
	Accounts    int            `json:"accounts"`
	Contracts   int            `json:"contracts"`
	NativeTotal *big.Int       `json:"nativeTotal"`
	Tokens      []TokenSupply  `json:"tokens"`
	Largest     []AccountValue `json:"largest"`
}

// TokenSupply is an ERC-20 at To: the same on both chains, or the check fails.
type TokenSupply struct {
	Token       types.Address `json:"token"`
	TotalSupply *big.Int      `json:"totalSupply"`
	Holders     int           `json:"holders"`
	HoldersSum  *big.Int      `json:"holdersSum"`
	// SumIsSupply: the holders' balances add up to the supply (false for a token whose holders
	// no block and no genesis account names).
	SumIsSupply bool `json:"sumIsSupply"`
}

// AccountValue is an account's native balance at To.
type AccountValue struct {
	Address types.Address `json:"address"`
	Balance *big.Int      `json:"balance"`
}

// Failure is one check that did not hold.
type Failure struct {
	Block  uint64 `json:"block"`
	Check  string `json:"check"`
	Detail string `json:"detail"`
}

// Report is what was checked and what failed.
type Report struct {
	From         uint64          `json:"from"`
	To           uint64          `json:"to"`
	Hash         types.Hash      `json:"hash"`
	StateRoot    types.Hash      `json:"stateRoot"`
	SealedBy     []types.Address `json:"sealedBy"`
	Validators   []types.Address `json:"validators"`
	Blocks       uint64          `json:"blocks"`
	Transactions uint64          `json:"transactions"`
	Accounts     uint64          `json:"accounts"`
	Slots        uint64          `json:"slots"`
	Failures     []Failure       `json:"failures"`
	// Reconciliation is set with Config.Reconcile.
	Reconciliation *Reconciliation `json:"reconciliation,omitempty"`
}

// OK reports whether every check held.
func (r *Report) OK() bool { return len(r.Failures) == 0 }

// Run verifies blocks cfg.From..cfg.To.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	if cfg.From > cfg.To {
		return nil, fmt.Errorf("from %d is after to %d", cfg.From, cfg.To)
	}

	if cfg.Workers <= 0 {
		cfg.Workers = 8
	}

	if cfg.MaxFailures <= 0 {
		cfg.MaxFailures = 100
	}

	hashSigner, err := newHashSigner()
	if err != nil {
		return nil, err
	}

	v := &verifier{
		cfg:    cfg,
		pitau:  newClient(cfg.Pitau),
		ucl:    newClient(cfg.Ucl),
		signer: hashSigner,
		report: &Report{From: cfg.From, To: cfg.To, Failures: []Failure{}},

		accounts: map[types.Address]struct{}{},
		holders:  map[types.Address]map[types.Address]struct{}{},
	}

	for _, address := range cfg.GenesisAccounts {
		v.accounts[address] = struct{}{}
	}

	if err := v.seals(ctx); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	blocks := make(chan uint64)
	errs := make(chan error, cfg.Workers)

	var wg sync.WaitGroup

	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for n := range blocks {
				if err := v.block(ctx, n); err != nil {
					errs <- err

					cancel()

					return
				}

				if v.full() {
					cancel()

					return
				}
			}
		}()
	}

feed:
	for n := cfg.From; n <= cfg.To; n++ {
		select {
		case blocks <- n:
		case <-ctx.Done():
			break feed
		}
	}

	close(blocks)
	wg.Wait()
	close(errs)

	if err := <-errs; err != nil {
		return nil, err
	}

	if cfg.Reconcile && !v.full() {
		if err := v.reconcile(context.Background()); err != nil {
			return nil, err
		}
	}

	sort.Slice(v.report.Failures, func(i, j int) bool {
		return v.report.Failures[i].Block < v.report.Failures[j].Block
	})

	return v.report, nil
}

type verifier struct {
	cfg    Config
	pitau  *client
	ucl    *client
	signer signer.Signer

	mu     sync.Mutex
	report *Report

	// Every account a record names, and every address in each token's Transfer logs.
	accounts map[types.Address]struct{}
	holders  map[types.Address]map[types.Address]struct{}
}

func (v *verifier) fail(block uint64, check, format string, args ...interface{}) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.report.Failures = append(v.report.Failures,
		Failure{Block: block, Check: check, Detail: fmt.Sprintf(format, args...)})
}

func (v *verifier) full() bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	return len(v.report.Failures) >= v.cfg.MaxFailures
}

func (v *verifier) count(txs, accounts, slots int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.report.Blocks++
	v.report.Transactions += uint64(txs)
	v.report.Accounts += uint64(accounts)
	v.report.Slots += uint64(slots)
}

// seals checks that block To, as pitau stores it, carries the committed seals of a quorum of ucl's
// validators over ucl's hash of it.
func (v *verifier) seals(ctx context.Context) error {
	header, err := v.pitauHeader(ctx, v.cfg.To)
	if err != nil {
		return err
	}

	set := v.cfg.Validators
	if len(set) == 0 {
		genesis, err := v.pitauHeader(ctx, 0)
		if err != nil {
			return err
		}

		named, err := v.signer.GetValidators(genesis)
		if err != nil {
			return fmt.Errorf("block 0 names no validators: %w", err)
		}

		for i := 0; i < named.Len(); i++ {
			set = append(set, named.At(uint64(i)).Addr())
		}
	}

	ecdsaSet := validators.NewECDSAValidatorSet()
	for _, address := range set {
		if err := ecdsaSet.Add(validators.NewECDSAValidator(address)); err != nil {
			return fmt.Errorf("validator %s: %w", address, err)
		}
	}

	hash, err := v.signer.CalculateHeaderHash(header)
	if err != nil {
		return fmt.Errorf("block %d: %w", v.cfg.To, err)
	}

	v.report.Hash = hash
	v.report.StateRoot = header.StateRoot
	v.report.Validators = set

	extra, err := v.signer.GetIBFTExtra(header)
	if err != nil {
		v.fail(v.cfg.To, "seals", "no IBFT extra: %v", err)

		return nil
	}

	// What the seals sign: the hash, and the round from go-ibft on, as ucl checks them
	// (consensus/ibft VerifyHeader).
	sealed := ibft.ProposalHash(hash, extra.RoundNumber)
	quorum := 2*ecdsaSet.Len()/3 + 1

	if err := v.signer.VerifyCommittedSeals(sealed, extra.CommittedSeals, ecdsaSet, quorum); err != nil {
		v.fail(v.cfg.To, "seals", "not sealed by %d of the %d validators: %v", quorum, ecdsaSet.Len(), err)

		return nil
	}

	v.report.SealedBy = v.sealers(sealed, extra.CommittedSeals, ecdsaSet)

	return nil
}

// block checks block n; an error is a failure to ask, not a failed check.
func (v *verifier) block(ctx context.Context, n uint64) error {
	record, err := v.record(ctx, n)
	if err != nil {
		return err
	}

	header, err := v.pitauHeader(ctx, n)
	if err != nil {
		return err
	}

	// The header: ucl's hash, ucl's parent.
	hash, err := v.signer.CalculateHeaderHash(header)
	if err != nil {
		v.fail(n, "header", "ucl's header hash: %v", err)
	} else if hash != record.Hash {
		v.fail(n, "header", "pitau's header hashes to %s, ucl's block is %s", hash, record.Hash)
	}

	// ucl's hash leaves out the mix hash, the nonce and the base fee, so the hash alone does not
	// vouch for them: every field is compared with what ucl's archive answers.
	if err := v.headerFields(ctx, n); err != nil {
		return err
	}

	if n > 0 {
		parent, err := v.uclHash(ctx, n-1)
		if err != nil {
			return err
		}

		if header.ParentHash != parent {
			v.fail(n, "parent", "parent hash %s, ucl's block %d is %s", header.ParentHash, n-1, parent)
		}
	}

	// Transactions: pitau's reproduce the root and are ucl's.
	var block pitauBlock
	if err := v.pitau.call(ctx, "eth_getBlockByNumber", []interface{}{quantity(n), false}, &block); err != nil {
		return err
	}

	if block.Hash != record.Hash {
		v.fail(n, "header", "pitau serves block %d as %s, ucl's is %s", n, block.Hash, record.Hash)
	}

	stored := make([]*types.Transaction, 0, len(block.Transactions))

	for _, hash := range block.Transactions {
		raw, err := v.pitau.bytes(ctx, "eth_getRawTransactionByHash", hash.String())
		if errors.Is(err, errNoResult) {
			v.fail(n, "transactions", "pitau lists transaction %s but does not have it", hash)

			continue
		} else if err != nil {
			return err
		}

		tx := &types.Transaction{}
		if err := tx.UnmarshalRLP(raw); err != nil {
			v.fail(n, "transactions", "pitau's transaction %s: %v", hash, err)

			continue
		}

		stored = append(stored, tx)
	}

	if root := buildroot.CalculateTransactionsRoot(stored, n); root != header.TxRoot {
		v.fail(n, "transactions", "pitau's transactions give root %s, the header holds %s", root, header.TxRoot)
	}

	if len(block.Transactions) != len(record.Transactions) {
		v.fail(n, "transactions", "pitau has %d transactions, ucl %d", len(block.Transactions), len(record.Transactions))
	} else {
		for i, tx := range record.Transactions {
			if want := types.BytesToHash(crypto.Keccak256(tx)); block.Transactions[i] != want {
				v.fail(n, "transactions", "transaction %d is %s, ucl's is %s", i, block.Transactions[i], want)
			}
		}
	}

	// Receipts: pitau's give ucl's receipts root.
	var receipts []pitauReceipt
	if err := v.pitau.call(ctx, "eth_getBlockReceipts", []interface{}{quantity(n)}, &receipts); err != nil {
		return err
	}

	if root := receiptsRoot(receipts); root != header.ReceiptsRoot {
		v.fail(n, "receipts", "pitau's receipts give root %s, the header holds %s", root, header.ReceiptsRoot)
	}

	// State: every account and slot the block changed, at the block and at its parent.
	slots := 0

	for _, change := range record.Accounts {
		slots += len(change.Storage)

		for _, at := range []uint64{n, n - 1} {
			if n == 0 {
				break
			}

			if err := v.account(ctx, n, at, change); err != nil {
				return err
			}
		}
	}

	v.count(len(record.Transactions), len(record.Accounts), slots)
	v.collect(record)

	return nil
}

// transferTopic is ERC-20's Transfer(address,address,uint256).
var transferTopic = types.BytesToHash(crypto.Keccak256([]byte("Transfer(address,address,uint256)")))

// collect keeps the accounts a record names and the holders its Transfer logs name.
func (v *verifier) collect(record *export.Record) {
	v.mu.Lock()
	defer v.mu.Unlock()

	for _, change := range record.Accounts {
		v.accounts[change.Address] = struct{}{}
	}

	for _, receipt := range record.Receipts {
		for _, log := range receipt.Logs {
			if len(log.Topics) != 3 || log.Topics[0] != transferTopic {
				continue
			}

			set := v.holders[log.Address]
			if set == nil {
				set = map[types.Address]struct{}{}
				v.holders[log.Address] = set
			}

			set[types.BytesToAddress(log.Topics[1].Bytes())] = struct{}{}
			set[types.BytesToAddress(log.Topics[2].Bytes())] = struct{}{}
		}
	}
}

// both asks pitau and ucl the same question at block To and returns both answers.
func (v *verifier) both(ctx context.Context, method string, params ...interface{}) (string, string, error) {
	var p, u string
	if err := v.pitau.call(ctx, method, params, &p); err != nil {
		return "", "", err
	}

	if err := v.ucl.call(ctx, method, params, &u); err != nil {
		return "", "", err
	}

	return p, u, nil
}

func bigOf(s string) *big.Int {
	n, ok := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
	if !ok {
		return new(big.Int)
	}

	return n
}

// reconcile builds the reconciliation at To.
func (v *verifier) reconcile(ctx context.Context) error {
	tag := quantity(v.cfg.To)
	r := &Reconciliation{NativeTotal: new(big.Int), Tokens: []TokenSupply{}}

	accounts := make([]types.Address, 0, len(v.accounts))
	for address := range v.accounts {
		accounts = append(accounts, address)
	}

	sort.Slice(accounts, func(i, j int) bool { return bytes.Compare(accounts[i][:], accounts[j][:]) < 0 })

	var contracts, balances []AccountValue

	for _, address := range accounts {
		p, u, err := v.both(ctx, "eth_getBalance", address.String(), tag)
		if err != nil {
			return err
		}

		if !sameHex(p, u) {
			v.fail(v.cfg.To, "reconciliation", "balance of %s: pitau %s, ucl %s", address, p, u)
		}

		balance := bigOf(p)
		r.NativeTotal.Add(r.NativeTotal, balance)
		balances = append(balances, AccountValue{Address: address, Balance: balance})

		pc, uc, err := v.both(ctx, "eth_getCode", address.String(), tag)
		if err != nil {
			return err
		}

		if !sameHex(pc, uc) {
			v.fail(v.cfg.To, "reconciliation", "code of %s differs", address)
		}

		if len(strings.TrimPrefix(pc, "0x")) > 0 {
			contracts = append(contracts, AccountValue{Address: address})
		}
	}

	r.Accounts, r.Contracts = len(accounts), len(contracts)

	// ERC-20s: contracts that answer totalSupply(), and every Transfer log emitter.
	candidates := map[types.Address]struct{}{}
	for _, c := range contracts {
		candidates[c.Address] = struct{}{}
	}

	for token := range v.holders {
		candidates[token] = struct{}{}
	}

	tokens := make([]types.Address, 0, len(candidates))
	for token := range candidates {
		tokens = append(tokens, token)
	}

	sort.Slice(tokens, func(i, j int) bool { return bytes.Compare(tokens[i][:], tokens[j][:]) < 0 })

	for _, token := range tokens {
		call := func(data string) (string, string, error) {
			return v.both(ctx, "eth_call", map[string]string{"to": token.String(), "data": data}, tag)
		}

		p, u, err := call("0x18160ddd") // totalSupply()
		if err != nil || len(strings.TrimPrefix(u, "0x")) != 64 {
			continue // not an ERC-20, or a call that reverts: not a token
		}

		if !sameHex(p, u) {
			v.fail(v.cfg.To, "reconciliation", "totalSupply of %s: pitau %s, ucl %s", token, p, u)
		}

		holders := map[types.Address]struct{}{}
		for _, a := range accounts {
			holders[a] = struct{}{}
		}

		for h := range v.holders[token] {
			holders[h] = struct{}{}
		}

		sum := new(big.Int)

		for holder := range holders {
			data := "0x70a08231" + strings.Repeat("0", 24) + hex.EncodeToHex(holder.Bytes())[2:] // balanceOf(address)

			hp, hu, err := call(data)
			if err != nil {
				return err
			}

			if !sameHex(hp, hu) {
				v.fail(v.cfg.To, "reconciliation", "balanceOf(%s) on %s: pitau %s, ucl %s", holder, token, hp, hu)
			}

			sum.Add(sum, bigOf(hp))
		}

		supply := bigOf(p)
		r.Tokens = append(r.Tokens, TokenSupply{
			Token: token, TotalSupply: supply, Holders: len(holders), HoldersSum: sum,
			SumIsSupply: sum.Cmp(supply) == 0,
		})
	}

	sort.Slice(balances, func(i, j int) bool { return balances[i].Balance.Cmp(balances[j].Balance) > 0 })

	if len(balances) > 10 {
		balances = balances[:10]
	}

	r.Largest = balances
	v.report.Reconciliation = r

	return nil
}

// account compares one changed account, and its changed slots, at block at.
func (v *verifier) account(ctx context.Context, n, at uint64, change export.AccountChange) error {
	tag := quantity(at)
	address := change.Address.String()

	for _, method := range []string{"eth_getBalance", "eth_getTransactionCount", "eth_getCode"} {
		var p, u string
		if err := v.pitau.call(ctx, method, []interface{}{address, tag}, &p); err != nil {
			return err
		}

		if err := v.ucl.call(ctx, method, []interface{}{address, tag}, &u); err != nil {
			return err
		}

		if !sameHex(p, u) {
			v.fail(n, "state", "%s of %s at block %d: pitau %s, ucl %s", method, address, at, abbreviate(p), abbreviate(u))
		}
	}

	for _, slot := range change.Storage {
		var p, u string
		if err := v.pitau.call(ctx, "eth_getStorageAt", []interface{}{address, slot.Slot.String(), tag}, &p); err != nil {
			return err
		}

		if err := v.ucl.call(ctx, "eth_getStorageAt", []interface{}{address, slot.Slot.String(), tag}, &u); err != nil {
			return err
		}

		if !sameHex(p, u) {
			v.fail(n, "state", "slot %s of %s at block %d: pitau %s, ucl %s", slot.Slot, address, at, p, u)
		}
	}

	return nil
}

// headerFields are the header fields JSON-RPC answers, which must be the same on pitau and ucl.
var headerFields = []string{
	"hash", "parentHash", "sha3Uncles", "miner", "stateRoot", "transactionsRoot", "receiptsRoot",
	"logsBloom", "difficulty", "number", "gasLimit", "gasUsed", "timestamp", "extraData", "mixHash",
	"nonce", "baseFeePerGas",
}

// headerFields compares every header field of block n on pitau and on ucl's archive.
func (v *verifier) headerFields(ctx context.Context, n uint64) error {
	var p, u map[string]interface{}
	if err := v.pitau.call(ctx, "eth_getBlockByNumber", []interface{}{quantity(n), false}, &p); err != nil {
		return err
	}

	if err := v.ucl.call(ctx, "eth_getBlockByNumber", []interface{}{quantity(n), false}, &u); err != nil {
		return err
	}

	for _, field := range headerFields {
		a, _ := p[field].(string)
		b, _ := u[field].(string)

		// ucl leaves a zero base fee out of its answer.
		if field == "baseFeePerGas" && b == "" {
			b = "0x0"
		}

		if !strings.EqualFold(a, b) && !sameHex(a, b) {
			v.fail(n, "header", "%s: pitau %s, ucl %s", field, abbreviate(a), abbreviate(b))
		}
	}

	return nil
}

// record is ucl's export record of block n, from the ucl archive.
func (v *verifier) record(ctx context.Context, n uint64) (*export.Record, error) {
	if n == 0 {
		// Block 0 has no record: its hash is all there is to compare.
		hash, err := v.uclHash(ctx, 0)
		if err != nil {
			return nil, err
		}

		return &export.Record{Hash: hash}, nil
	}

	var answer struct {
		Records []string `json:"records"`
		Error   *struct {
			Block   uint64 `json:"block"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := v.ucl.call(ctx, "ucl_exportBlocks", []interface{}{quantity(n), quantity(n)}, &answer); err != nil {
		return nil, err
	}

	if len(answer.Records) == 0 {
		if answer.Error != nil {
			return nil, fmt.Errorf("ucl cannot export block %d: %s", n, answer.Error.Message)
		}

		return nil, fmt.Errorf("ucl exported nothing for block %d", n)
	}

	frame, err := base64.StdEncoding.DecodeString(answer.Records[0])
	if err != nil {
		return nil, fmt.Errorf("record %d: %w", n, err)
	}

	raw, err := export.DecodeFrame(frame)
	if err != nil {
		return nil, fmt.Errorf("record %d: %w", n, err)
	}

	record := &export.Record{}
	if err := record.UnmarshalRLP(raw); err != nil {
		return nil, fmt.Errorf("record %d: %w", n, err)
	}

	if record.Number != n {
		return nil, fmt.Errorf("asked for record %d, got %d", n, record.Number)
	}

	return record, nil
}

func (v *verifier) uclHash(ctx context.Context, n uint64) (types.Hash, error) {
	var block struct {
		Hash types.Hash `json:"hash"`
	}

	if err := v.ucl.call(ctx, "eth_getBlockByNumber", []interface{}{quantity(n), false}, &block); err != nil {
		return types.Hash{}, err
	}

	if block.Hash == types.ZeroHash {
		return types.Hash{}, fmt.Errorf("ucl has no block %d", n)
	}

	return block.Hash, nil
}

// pitauHeader is block n's header as pitau serves it (eth_getBlockByNumber), as ucl's type: every
// field ucl's header hash covers, the seals in the extra data included.
func (v *verifier) pitauHeader(ctx context.Context, n uint64) (*types.Header, error) {
	var h struct {
		ParentHash    types.Hash  `json:"parentHash"`
		Sha3Uncles    types.Hash  `json:"sha3Uncles"`
		Miner         string      `json:"miner"`
		StateRoot     types.Hash  `json:"stateRoot"`
		TxRoot        types.Hash  `json:"transactionsRoot"`
		ReceiptsRoot  types.Hash  `json:"receiptsRoot"`
		LogsBloom     types.Bloom `json:"logsBloom"`
		Difficulty    string      `json:"difficulty"`
		Number        string      `json:"number"`
		GasLimit      string      `json:"gasLimit"`
		GasUsed       string      `json:"gasUsed"`
		Timestamp     string      `json:"timestamp"`
		ExtraData     string      `json:"extraData"`
		MixHash       types.Hash  `json:"mixHash"`
		Nonce         string      `json:"nonce"`
		BaseFeePerGas string      `json:"baseFeePerGas"`
	}

	if err := v.pitau.call(ctx, "eth_getBlockByNumber", []interface{}{quantity(n), false}, &h); err != nil {
		return nil, err
	}

	miner, err := hex.DecodeHex(h.Miner)
	if err != nil {
		return nil, fmt.Errorf("pitau's header %d miner: %w", n, err)
	}

	extra, err := hex.DecodeHex(h.ExtraData)
	if err != nil {
		return nil, fmt.Errorf("pitau's header %d extra data: %w", n, err)
	}

	nonce, err := hex.DecodeHex(h.Nonce)
	if err != nil || len(nonce) != len(types.Nonce{}) {
		return nil, fmt.Errorf("pitau's header %d nonce %q", n, h.Nonce)
	}

	header := &types.Header{
		ParentHash:   h.ParentHash,
		Sha3Uncles:   h.Sha3Uncles,
		Miner:        miner,
		StateRoot:    h.StateRoot,
		TxRoot:       h.TxRoot,
		ReceiptsRoot: h.ReceiptsRoot,
		LogsBloom:    h.LogsBloom,
		Difficulty:   mustQuantity(h.Difficulty),
		Number:       mustQuantity(h.Number),
		GasLimit:     mustQuantity(h.GasLimit),
		GasUsed:      mustQuantity(h.GasUsed),
		Timestamp:    mustQuantity(h.Timestamp),
		ExtraData:    extra,
		MixHash:      h.MixHash,
		BaseFee:      mustQuantity(h.BaseFeePerGas),
	}
	copy(header.Nonce[:], nonce)

	if header.Number != n {
		return nil, fmt.Errorf("asked pitau for block %d, got %d", n, header.Number)
	}

	return header, nil
}

type pitauBlock struct {
	Hash         types.Hash   `json:"hash"`
	Transactions []types.Hash `json:"transactions"`
}

type pitauReceipt struct {
	Type              string `json:"type"`
	Status            string `json:"status"`
	CumulativeGasUsed string `json:"cumulativeGasUsed"`
	Logs              []struct {
		Address types.Address `json:"address"`
		Topics  []types.Hash  `json:"topics"`
		Data    string        `json:"data"`
	} `json:"logs"`
}

// receiptsRoot is ucl's receipts root of pitau's receipts: each receipt's untyped RLP with its own
// bloom, as ucl's executor builds them.
func receiptsRoot(receipts []pitauReceipt) types.Hash {
	built := make([]*types.Receipt, len(receipts))

	for i, r := range receipts {
		receipt := &types.Receipt{
			CumulativeGasUsed: mustQuantity(r.CumulativeGasUsed),
			TransactionType:   types.TxType(mustQuantity(r.Type)),
		}
		receipt.SetStatus(types.ReceiptStatus(mustQuantity(r.Status)))

		for _, l := range r.Logs {
			data, _ := hex.DecodeHex(l.Data)
			receipt.Logs = append(receipt.Logs, &types.Log{Address: l.Address, Topics: l.Topics, Data: data})
		}

		receipt.LogsBloom = types.CreateBloom([]*types.Receipt{receipt})
		built[i] = receipt
	}

	return buildroot.CalculateReceiptsRoot(built)
}

// sealers are the validators whose committed seals over hash are among seals.
func (v *verifier) sealers(hash types.Hash, seals signer.Seals, set validators.Validators) []types.Address {
	committed, ok := seals.(*signer.SerializedSeal)
	if !ok {
		return nil
	}

	var addresses []types.Address

	for _, seal := range *committed {
		for i := 0; i < set.Len(); i++ {
			address := set.At(uint64(i)).Addr()
			if v.signer.VerifyCommittedSeal(set, address, seal, hash.Bytes()) == nil {
				addresses = append(addresses, address)

				break
			}
		}
	}

	sort.Slice(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 })

	return addresses
}

// newHashSigner is a signer for ucl's IBFT header hash and seal checks; its own key signs nothing.
func newHashSigner() (signer.Signer, error) {
	key, err := crypto.GenerateECDSAPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("the hash signer's key: %w", err)
	}

	// The parent key manager reads the parent's committed seals in every header above block 1.
	manager := signer.NewECDSAKeyManagerFromKey(key)

	return signer.NewSigner(manager, manager), nil
}

func quantity(n uint64) string { return fmt.Sprintf("0x%x", n) }

func mustQuantity(s string) uint64 {
	n, ok := new(big.Int).SetString(strings.TrimPrefix(s, "0x"), 16)
	if !ok || s == "" {
		return 0
	}

	return n.Uint64()
}

// sameHex compares two JSON-RPC hex values by number for quantities and by bytes for data.
func sameHex(a, b string) bool {
	if a == b {
		return true
	}

	x, okA := new(big.Int).SetString(strings.TrimPrefix(a, "0x"), 16)
	y, okB := new(big.Int).SetString(strings.TrimPrefix(b, "0x"), 16)

	if strings.TrimPrefix(a, "0x") == "" && strings.TrimPrefix(b, "0x") == "" {
		return true
	}

	return okA && okB && x.Cmp(y) == 0 && len(a) <= 66 && len(b) <= 66
}

func abbreviate(s string) string {
	if len(s) > 42 {
		return s[:20] + "…" + s[len(s)-8:]
	}

	return s
}

// client is a JSON-RPC client over HTTP.
type client struct {
	url  string
	http *http.Client
}

func newClient(url string) *client {
	return &client{url: url, http: &http.Client{Timeout: 5 * time.Minute}}
}

var errNoResult = errors.New("no result")

func (c *client) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	body, err := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}

	var lastErr error

	for attempt := 0; attempt < 5; attempt++ {
		lastErr = c.once(ctx, method, body, out)
		if lastErr == nil || errors.Is(lastErr, errNoResult) || ctx.Err() != nil {
			return lastErr
		}

		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}

	return lastErr
}

func (c *client) once(ctx context.Context, method string, body []byte, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	// The URL is the operator's --pitau or --ucl flag: asking it is the verifier's whole job.
	resp, err := c.http.Do(req) //nolint:gosec
	if err != nil {
		return fmt.Errorf("%s at %s: %w", method, c.url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	var answer struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return fmt.Errorf("%s at %s: %w", method, c.url, err)
	}

	if answer.Error != nil {
		return fmt.Errorf("%s at %s: %s: %w", method, c.url, answer.Error.Message, errNoResult)
	}

	if len(answer.Result) == 0 || string(answer.Result) == "null" {
		return fmt.Errorf("%s at %s: %w", method, c.url, errNoResult)
	}

	return json.Unmarshal(answer.Result, out)
}

func (c *client) bytes(ctx context.Context, method string, params ...interface{}) ([]byte, error) {
	var s string
	if err := c.call(ctx, method, params, &s); err != nil {
		return nil, err
	}

	return hex.DecodeHex(s)
}
