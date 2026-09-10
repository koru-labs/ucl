# Unwinding committed blocks

Use `polygon-edge unwind` when a validator has already **committed** a head you want to roll back. The node must be stopped. The tool moves canonical HEAD backward, drops lookup indexes for the removed heights, and clamps IBFT snapshot files so they do not sit ahead of the new head.

It does **not** rewrite the state trie. The parent block's `StateRoot` is already in the database; leftover trie nodes and orphan header/body/receipt bytes stay on disk.

If the chain is stuck because a bad transaction is being proposed and rejected, the bad block usually never committed. Use [the txpool journal tool](txpool-journal.md) instead.

---

## When you need this

- A height you do not want is already canonical (`eth_blockNumber` reached it).
- You are recovering a validator database offline, not racing a live process.
- Every validator that should remain in the same network will unwind to the **same** height before restart.

Do not unwind only one validator and then start it against peers that still have the higher head. They will disagree.

---

## Commands

Stop the validator first. Pebble will refuse the database if the node still has it open.

### Drop the last N blocks

```bash
./polygon-edge unwind --data-dir ./test-chain-1 --blocks 1
```

### Move HEAD to a height

```bash
./polygon-edge unwind --data-dir ./test-chain-1 --to 20
```

`--to 0` is genesis. `--blocks` and `--to` cannot be combined.

### Preview

```bash
./polygon-edge unwind --data-dir ./test-chain-1 --blocks 3 --dry-run
```

Dry-run prints the same report and does not write.

---

## What it changes

| Location | Change |
| --- | --- |
| `<data-dir>/blockchain` | HEAD hash/number move to the parent. Canonical hash, block lookup, and tx lookup for dropped heights are deleted. |
| `<data-dir>/consensus/metadata` | `LastBlock` is clamped to the new head if it was higher. |
| `<data-dir>/consensus/snapshots` | Entries with `Number` above the new head are dropped. |

Header, body, and receipt records for the unwound blocks are left as orphans. The trie is not pruned.

---

## After unwind

1. If a local transaction caused the problem, remove it from the submitter journal before restart (`txpool journal remove`). Otherwise the same tx is proposed again.
2. Unwind **every** validator that should share the new head, to the same height.
3. Start the nodes.

---

## What this tool does not do

- It does not delete orphan block bytes or reclaim trie space.
- It does not edit the txpool journal.
- It does not talk to a running node. There is no RPC unwind.
- It does not keep a one-node network in consensus with peers that still have the old head.

---

## Quick reference

```bash
# preview
./polygon-edge unwind --data-dir ./test-chain-1 --blocks 1 --dry-run

# drop last block
./polygon-edge unwind --data-dir ./test-chain-1 --blocks 1

# set head
./polygon-edge unwind --data-dir ./test-chain-1 --to 20
```
