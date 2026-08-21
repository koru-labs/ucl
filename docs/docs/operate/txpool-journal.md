# Recovering from a stuck IBFT chain

This is a practical guide for the offline `txpool journal` tool. Use it when validators keep proposing the same bad transaction, reject each other's blocks, and the height never moves.

It is **not** a "revert the last committed block" tool. IBFT on this chain checks execution **before** voting, so a bad transaction usually never becomes canonical. The chain just stops.

---

## When you need this

Typical picture:

- `eth_blockNumber` is frozen on every validator.
- Logs show `block verification failed` wrapping `invalid block state root` (or invalid gas / receipts).
- `consensus_state` shows a growing round and no quorum.
- After a restart, the stall comes back immediately.

That last point is the important one. A transaction submitted over JSON-RPC is treated as **local**. Local transactions are written to disk and loaded again on startup. Gossiped copies on the other validators are **not** written to disk. So:

1. Node A accepts the RPC submit and journals the tx.
2. Node A proposes a block that includes it.
3. Nodes B, C, D re-execute, get a different state root, and refuse to vote.
4. You restart everyone. Node A loads the journal and proposes the same tx again.
5. The halt is now permanent until that journal entry is removed.

Use this tool after you have **stopped the validator**. The journal file is rewritten while the process is running. Editing it live will race the node.

---

## What the tool is looking at

Each validator data directory has two files that matter:

| File | Meaning |
| --- | --- |
| `<data-dir>/txpool/transactions.rlp` | Live local journal. Reloaded into the mempool on start. |
| `<data-dir>/txpool/removed.rlp` | Quarantine. Txs you removed, kept in case you want them back. |
| `<data-dir>/consensus/rejected-blocks.rlp` | Last 16 IBFT proposals this node refused. Written only after a reject on a current binary. |

Only the node that received `eth_sendRawTransaction` (or otherwise called `AddTx`) has the bad tx in its journal. In the local 4-node cluster that is usually `test-chain-1`.

Rejected-block files appear on the nodes that **voted no** — often `test-chain-2` through `test-chain-4`. The proposer that built the block typically does not record a reject for its own proposal.

---

## Commands

All of these are offline. Point `--data-dir` at one validator folder. Default `--chain-id` is `100` (used only to recover `from`).

### List the local journal

```bash
./polygon-edge txpool journal list --data-dir ./test-chain-1
```

Shows hash, from, to, nonce, value, gas, gas price, type, and input size.

### List quarantined transactions

```bash
./polygon-edge txpool journal list --data-dir ./test-chain-1 --removed
```

### List rejected IBFT proposals

```bash
./polygon-edge txpool journal rejected --data-dir ./test-chain-2
```

For each stored proposal you get:

- why it was rejected
- the **proposed** state root (what the proposer put in the header)
- this node's **local** state root (what re-execution produced)
- every transaction in that proposal

Per transaction you also get this node's local execution fingerprints:

- **Local status** and **local gas used**
- **Return hash** — keccak of returndata
- **State delta hash** — keccak of dirty account/storage after that tx
- **In local journal** — whether this same hash sits in *this* node's journal

`Count = 0` is normal if the file was never written. That happens when the reject ran on an older binary, or you are looking at the proposer.

### Remove a transaction (quarantine)

```bash
./polygon-edge txpool journal remove --data-dir ./test-chain-1 --hash 0xabc...
```

The tx is appended to `removed.rlp` and deleted from `transactions.rlp`. You can pass `--hash` more than once.

### Put it back

```bash
./polygon-edge txpool journal restore --data-dir ./test-chain-1 --hash 0xabc...
```

---

## How to find the faulty transaction

The transaction list in a rejected block is the **proposer's** list. Every validator that rejected that proposal stores the same hashes. Comparing hashes across nodes will not tell you which tx is bad.

What differs is **local execution**. After a reject is recorded, compare two voters:

```bash
./polygon-edge txpool journal rejected --data-dir ./test-chain-2
./polygon-edge txpool journal rejected --data-dir ./test-chain-3
```

Walk the transactions in order. The **first** tx whose `Return hash` or `State delta hash` differs is the one that made the state roots diverge.

Then remove that hash from the journal of the node that actually persisted it (the submitter):

```bash
./polygon-edge txpool journal list --data-dir ./test-chain-1
./polygon-edge txpool journal remove --data-dir ./test-chain-1 --hash 0x...
```

`In local journal` on node 2/3 is often `false`. That only means "not in my file". It can still be in node 1's file.

---

## Recovery procedure

1. Confirm the stall: frozen height, `invalid block state root` in logs, no quorum in `consensus_state`.
2. **Stop every validator.**
3. On a node that voted no, list rejected blocks and compare fingerprints if you have more than one voter.
4. On the submitter, list the journal and remove the matching hash.
5. Start the validators again.

If height advances, you are done. If it stalls on the same hash, you missed a journal copy (another node also submitted it locally). Repeat step 4 there.

If you removed the wrong tx, restore it from `--removed` and try again.

---

## Reproducing a halt locally

This repo has a test-only precompile at `0x2060` that writes `os.Getpid()` into the **caller** account. It is registered only when `UCL_TEST_NONDET_PRECOMPILE=1`. That env var also tells `scripts/cluster` to keep `test-chain-*` and `genesis.json` on restart so height resumes.

```bash
# terminal 1
UCL_TEST_NONDET_PRECOMPILE=1 ./scripts/cluster ibft write-logs

# terminal 2, after RPC is up
./scripts/trigger-bad-block.sh --at-block 20
```

Expected: the CALL is proposed, other validators reject (`invalid block state root`), height freezes. Restarting with the same env var reloads the journal and the stall returns. Then use the journal commands above.

To start the local cluster from genesis again while that env is set:

```bash
rm -rf test-chain-* genesis.json
UCL_TEST_NONDET_PRECOMPILE=1 ./scripts/cluster ibft write-logs
```

---

## What this tool does not do

- It does not rewind a **committed** block. If a bad block actually landed on disk, use [blockchain unwind](blockchain-unwind.md).
- It does not drop a live mempool tx over RPC. Stop the node and edit the journal.
- It does not run on PolyBFT rejected proposals. The rejected-block log is IBFT-only.
- It does not protect production from the `0x2060` precompile. That hook is env-gated and for local experiments only.

---

## Quick reference

```bash
# inspect
./polygon-edge txpool journal list --data-dir ./test-chain-1
./polygon-edge txpool journal rejected --data-dir ./test-chain-2

# fix
./polygon-edge txpool journal remove --data-dir ./test-chain-1 --hash 0x...

# undo
./polygon-edge txpool journal restore --data-dir ./test-chain-1 --hash 0x...
./polygon-edge txpool journal list --data-dir ./test-chain-1 --removed
```
