# Disaster recovery tools

Operator index for the offline recovery tools. Two pages go deeper on a single tool:

- [Txpool journal](txpool-journal.md) — stuck height, bad local tx
- [Unwind committed blocks](blockchain-unwind.md) — roll HEAD backward

IBFT on this chain re-executes a proposal **before** voting. A state-root mismatch rejects the proposal. The bad block is usually **not** committed. The stall is liveness at height H, not “commit then diverge at H+1”.

---

## Which tool

| Symptom | Tool | Offline? |
| --- | --- | --- |
| Height frozen. Logs: `block verification failed` / `invalid block state root`. Restart brings the stall back. | `txpool journal` | Yes. Stop the node. |
| You need to see *why* a proposal was refused and which tx diverged. | `txpool journal rejected` | Yes. Stop the node. |
| A height you do not want is already canonical. | `unwind` | Yes. Stop the node. |
| After unwind or a failed write, snapshot metadata sat ahead of HEAD. | Automatic on startup (snapshot store) | No CLI. |

Do not unwind a halt that never committed. Remove the journaled tx instead.

---

## What shipped

### 1. Txpool journal editor — `polygon-edge txpool journal`

Local JSON-RPC submits are written to `<data-dir>/txpool/transactions.rlp` and reloaded on start. Gossiped copies are not journaled. The node that accepted the submit will re-propose the same bad tx forever until that file is edited.

| Command | Purpose |
| --- | --- |
| `list --data-dir DIR` | Live journal |
| `list --data-dir DIR --removed` | Quarantine |
| `remove --data-dir DIR --hash 0x...` | Move to `txpool/removed.rlp` |
| `restore --data-dir DIR --hash 0x...` | Put back |
| `rejected --data-dir DIR` | Last 16 IBFT rejected proposals |

`--chain-id` default is `100` (sender recovery only). `--hash` is repeatable.

Rejected-block file: `<data-dir>/consensus/rejected-blocks.rlp` (magic `RJ02`). Written on the nodes that **voted no**. `Count = 0` means the reject ran on an older binary, or you are looking at the proposer.

Per-tx fingerprints on a reject: local status, gas used, **return hash**, **state delta hash**, plus proposed vs local state root. Compare two voters; the first tx whose return hash or state delta hash differs is the bad one. Remove that hash from the **submitter** journal.

### 2. Blockchain unwind — `polygon-edge unwind`

Moves canonical HEAD backward on a **stopped** validator. Deletes canonical hash, block lookup, and tx lookup for dropped heights. Leaves header/body/receipt bytes as orphans. Does not rewrite the state trie (the parent `StateRoot` is already stored). Clamps IBFT `consensus/metadata` and drops `consensus/snapshots` entries above the new head.

```bash
./polygon-edge unwind --data-dir ./test-chain-1 --blocks 1
./polygon-edge unwind --data-dir ./test-chain-1 --to 20
./polygon-edge unwind --data-dir ./test-chain-1 --blocks 3 --dry-run
```

`--blocks` and `--to` cannot be combined. `--to 0` is genesis. Pebble refuses the DB if the node still has it open.

Every validator that should stay in the same network must unwind to the **same** height. Unwind does not remove a journaled tx.

### 3. Snapshot store fixes (no new CLI)

Startup now clamps snapshots when `LastBlock` is above HEAD. `find` no longer returns a future snapshot. Epoch-boundary snapshots are always written. `putByNumber` / prune use a real range search. Block write commits the chain **before** updating snapshots. `Close` writes `snapshots` then `metadata`.

You test this by unwinding and restarting: the node should start and produce the next height from the new head.

---

## How to test

Build once:

```bash
go build -o polygon-edge .
```

`./scripts/cluster ibft` wipes `test-chain-*` and `genesis.json` unless you pass `--keep-data`. Stop the processes with `Ctrl-C` or `pkill -f 'polygon-edge server'`, then resume:

```bash
./scripts/cluster ibft write-logs --keep-data
```

### Automated

```bash
go test ./txpool/ ./command/unwind/ ./blockchain/ \
  ./consensus/ibft/ ./consensus/ibft/fork/ \
  ./validators/store/snapshot/
```

Covers journal read/remove/restore, unwind + dry-run, rejected-block store, and snapshot clamp/find/prune/replace.

### A. Journal recovery

Use this on a validator that is already stuck (frozen height, `invalid block state root`, stall survives restart). Stop every validator first.

```bash
./polygon-edge txpool journal list --data-dir ./test-chain-1
./polygon-edge txpool journal rejected --data-dir ./test-chain-2
./polygon-edge txpool journal rejected --data-dir ./test-chain-3
```

Compare return hash / state delta hash across two voters. Remove the first differing tx from the submitter journal:

```bash
./polygon-edge txpool journal remove --data-dir ./test-chain-1 --hash 0x...
./polygon-edge txpool journal list --data-dir ./test-chain-1
./polygon-edge txpool journal list --data-dir ./test-chain-1 --removed
```

Start again with `./scripts/cluster ibft write-logs --keep-data`. Height should advance. If you removed the wrong hash, `restore` it from `--removed`.

`unwind` is the wrong tool if the bad block never committed.

### B. Unwind a committed head

Do this on a **healthy** chain.

1. Start a local cluster and wait until height is at least 25.
2. Stop the validators (`Ctrl-C` or `pkill -f 'polygon-edge server'`). Do not start again without `--keep-data`.
3. Preview, then unwind **every** node to the same height:

   ```bash
   ./polygon-edge unwind --data-dir ./test-chain-1 --blocks 3 --dry-run
   for i in 1 2 3 4; do
     ./polygon-edge unwind --data-dir ./test-chain-$i --to 20
   done
   ```

   Expect HEAD `20`, three removed heights, snapshot `LastBlock` clamped if it was ahead.

4. Resume the same data dirs:

   ```bash
   ./scripts/cluster ibft write-logs --keep-data
   ```

   `eth_blockNumber` should resume from 20 and climb. That also exercises the snapshot clamp on startup.

5. Negative checks:

   - Unwind only node 1 to 20, leave 2–4 at 25, start everyone — they will disagree.
   - `--dry-run` must not change HEAD.
   - `--blocks` past genesis must error.
   - Opening pebble while the node is running must fail (“stop the validator first”).

### C. Snapshot files after unwind

After B, before restart:

```bash
python3 -c 'import json,sys; print(json.load(open(sys.argv[1])))' test-chain-1/consensus/metadata
```

`LastBlock` must be `<=` new head. `consensus/snapshots` must not contain `Number` above that head.

### D. Rejected-block capture

Written only after a real reject on a current binary. If `rejected` prints `Count = 0`, you are on an older binary, or you are reading the proposer rather than a voter.

---

## After any recovery

1. If a local tx caused the problem, it must be out of the submitter journal before restart.
2. If you unwound, every remaining peer must share that head.
3. Start the nodes. Confirm `eth_blockNumber` moves.

---

## Quick reference

```bash
# unit tests
go test ./txpool/ ./command/unwind/ ./blockchain/ ./consensus/ibft/ \
  ./consensus/ibft/fork/ ./validators/store/snapshot/

# journal
./polygon-edge txpool journal list --data-dir ./test-chain-1
./polygon-edge txpool journal rejected --data-dir ./test-chain-2
./polygon-edge txpool journal remove --data-dir ./test-chain-1 --hash 0x...

# committed-head unwind (all validators, same --to)
./polygon-edge unwind --data-dir ./test-chain-1 --blocks 1 --dry-run
./polygon-edge unwind --data-dir ./test-chain-1 --to 20
```
