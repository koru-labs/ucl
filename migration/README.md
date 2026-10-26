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
- **Ranges.** At most 1,000 blocks and 64 MiB of frames per answer (always at least one record),
  capped at the head. The first block that fails ends the range, after the records before it.

The answer is `{records: [base64 frame…], next, head, error?: {block, message, irreproducible}}`; the
caller asks again from `next`.

## Record format (version 1)

A frame is the CRC-32C of the record's RLP (4 bytes, big endian) followed by the RLP compressed
with zstd. The record is an RLP list:

```
[version, number, hash, header, [tx…], [receipt…], [account…]]
receipt = [type, success, cumulativeGasUsed, [[address, [topic…], data]…]]
account = [address, before, after, code, [[slot, before, after]…]]
before, after = [] (absent / destroyed) | [nonce, balance, codeHash]
```

- `hash` and `header` are ucl's, verbatim: the header RLP holds all 16 fields, seals included.
- `tx` is the transaction's envelope (typed ones prefixed with their type).
- `code` is set when the block deployed the account's code; storage values are integers, zero
  meaning empty. A destroyed account (`after = []`) lists no slots: its whole storage is gone.
- `before` values come from the parent's state. pitau's follower stores them as reth's history; the
  migration's verifier checks them against a ucl archive (§6.4, §7.5).
