# ucl to pitau migration

Code ucl needs for the migration of a running ucl network to pitau. The plan is pitau's
`docs/plans/ucl-migration.md`; section numbers below refer to it.

## Export endpoint (`migration/export`, §6.1)

A ucl full node started with `--migration-export-dir <dir>` serves `ucl_exportBlocks(from, to)`:
the records of blocks `from..to`, which pitau's follower stores without executing them. It is refused
on a validator (`--seal`). `--migration-export-workers` (default 4) bounds how many blocks execute at
once across all requests.

- **On demand.** A block executes only when a request asks for it, on its parent's state, exactly as
  ucl imported it. The re-execution must reproduce the stored state root, gas used, transactions
  root, receipts root and logs bloom, or the block is refused as irreproducible.
- **Cached.** Every record is kept in `<dir>/v<format>/<n / 10000>/<n>.rec` (the block hash, then the
  frame). A damaged or foreign entry is dropped and recomputed.
- **One job per block.** Concurrent and overlapping requests share each block's execution
  (single-flight by block number).
- **A state of its own.** The export executes on its own trie state, without the node's cache of
  tries (`export.IsolatedExecutor`): ucl's tries write to their nodes on reads and deletes, so they
  cannot be shared between blocks exported at once, or with the node's own block import and RPC.
- **Ranges.** At most 1,000 blocks and 64 MiB of frames per answer (always at least one record),
  capped at the head. The first block that fails ends the range, after the records before it.

The answer is `{records: [base64 frame…], next, head, error?: {block, message, irreproducible}}`; the
caller asks again from `next`.

## Record format (version 2)

A frame is the CRC-32C of the record's RLP (4 bytes, big endian) followed by the RLP compressed
with zstd. The record is an RLP list:

```
[version, number, hash, header, [tx…], [receipt…], [account…]]
receipt = [type, success, cumulativeGasUsed, [[address, [topic…], data]…]]
account = [address, before, after, code, [[slot, before, after]…], storageReset]
before, after = [] (absent / destroyed) | [nonce, balance, codeHash]
```

- `hash` and `header` are ucl's, verbatim: the header RLP holds all 16 fields, seals included.
- `tx` is the transaction's envelope (typed ones prefixed with their type).
- `code` is set when the block deployed the account's code; storage values are integers, zero
  meaning empty. A destroyed account (`after = []`) lists no slots: its whole storage is gone.
- `storageReset` is true for an account that exists after the block but whose storage before it is
  gone: the block destroyed it (SELFDESTRUCT) and created it again at the same address. Its slots
  are then every non-zero slot after the block, each with its value before the block. Version 1
  had no way to say this, so pitau would have kept the old slots and refused the state root.
- `before` values come from the parent's state. pitau's follower stores them as reth's history; the
  migration's verifier checks them against a ucl archive (§6.4, §7.5).

## Attestations and the halt (`migration/contracts`, `migration/halt`, §7.4)

`MigrationAttestations` (Foundry project in `migration/contracts`; `forge test` there) holds each pitau
validator's latest attestation, sent by its operator address: the ucl block its node is at and the
roots it computed, accepted only for ucl's canonical hash of one of the last 256 blocks. The first
time more than two thirds of the registry's validators agree on a block attested within `freshness`
blocks, it records `earliestHalt = block + margin`, once; the admin can tune `margin` until then.

The contract keeps the hashes of the registry's keys for the registry's current `version()` and reads
the keys again only when the version changes (an ML-DSA-65 key is 61 storage slots). An attestation
costs about 99,000 gas with 4 ML-DSA-65 validators and 430,000 with 50; the first one after each
change of the set costs what reading the whole set costs, 0.9 and 10.2 million
(`forge test --match-contract Gas`). Before the cache it was 0.8 and 9.3 million every time.

With `--migration-attestations 0x…` (config file: `migration_attestations`), set on every node to the
address of the deployed contract, ucl halts after the first block h for which the contract answers `shouldHalt(h)` on the state of block
h-1: validators build nothing after it and every node refuses any later block
(`Blockchain.MigrationHalted`, `migration/halt`). If the followers fall behind, the quorum goes stale
and ucl keeps producing blocks until they catch up.

## State proofs (`eth_getProof`, §7.2)

Every node answers `eth_getProof(address, slots, block)` as Ethereum's API does (EIP-1186): the
account's Merkle proof from the block's state root, and each slot's from the account's storage root
(`itrie.Prove`, `Eth.GetProof`). An account that does not exist is answered with zero nonce and
balance, the empty code hash and storage root, and a proof that the state does not hold it. pitau's
relay sentries read the halt and the validator registry at the cutover block this way, believing only
what the proofs and the block's IBFT seals show. pitau's tests check every proof of the vectors
network (`e2e/migration_rpc_test.go`) and that reth answers the same proofs byte for byte.

## Independent verifier (`migration/verify`, `migration/cmd/ucl-migration-verify`, §7.5)

```bash
go build -o ucl-migration-verify ./migration/cmd/ucl-migration-verify
ucl-migration-verify --pitau http://pitau-archive:8545 --ucl http://ucl-archive:8545 --to <H> \
  [--from 0] [--validators 0xa,0xb,…] [--workers 8] [--report report.json]
```

It asks a pitau archive, over its JSON-RPC (`eth_*` only), what it holds of ucl's history, and checks
every answer with ucl's own code against a ucl archive (a ucl node with the export endpoint, ideally
the frozen archive kept after the halt). For every block from `--from` to `--to`:

- every header field pitau answers equals ucl's, and pitau's header hashes to ucl's hash of the block
  under ucl's IBFT header hash (`CalculateHeaderHash`); the parent hash is ucl's hash of the block
  before. ucl's hash leaves out the mix hash, the nonce and the base fee, which is why the fields are
  compared one by one;
- pitau's transactions (`eth_getRawTransactionByHash`) reproduce the header's transactions root and
  are ucl's;
- pitau's receipts (`eth_getBlockReceipts`) reproduce the header's receipts root under ucl's formula
  (untyped receipt RLP, `buildroot.CalculateReceiptsRoot`);
- for every account and slot the block changed (ucl's export record names them), balance, nonce,
  code and storage are the same on pitau and ucl at the block and at its parent. No root covers the
  values before a block, so this is their only independent check.

And block `--to` carries the committed seals of a quorum (`2N/3 + 1`) of ucl's validators: those
`--validators` names, else those block 0 names, over the hash and the round as ucl checks them
(`ibft.ProposalHash`). With `--reconcile` (and `--ucl-genesis`, whose accounts no record names unless a block changed them)
it adds the balance reconciliation at `--to`, both chains side by side: every account the chain ever
held (the genesis' and every one a record names), their native total, the contracts among them, each
ERC-20's `totalSupply` against the sum of `balanceOf` over those accounts and every address in its
`Transfer` logs, and the ten largest accounts. A value that differs between the chains is a failure.
It exits 0 when every check held, 1 when one failed (each listed), 2 when it
could not ask. pitau's `testnet/ucl-migration-drill.sh` runs it on a migrated network and against a
proxy that alters one kind of answer at a time.
