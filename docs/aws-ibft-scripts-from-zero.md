# AWS IBFT scripts: guide from zero

This guide walks through using [`scripts/aws-ibft/`](../scripts/aws-ibft/) on a **coordinator machine** (your laptop, CI, or bastion) to create secrets, `genesis.json`, and per-host tarballs for a **two-validator, two–full-node** layout. It does **not** provision AWS or run `polygon-edge` on servers; you copy artifacts to EC2 yourself.

For **why** the topology and ports look like this (e.g. TCP **10002**, security groups), see [aws-ibft-topology.md](aws-ibft-topology.md).

---

## Prerequisites

1. **This repository** cloned and a shell at the **repository root** (`ucl/`).
2. **`bash`** available.
3. **`polygon-edge` binary** built from this tree (or on `PATH`):

   ```bash
   cd /path/to/ucl
   go build -o polygon-edge .
   ```

   If the binary lives elsewhere, export:

   ```bash
   export EDGE_BIN=/absolute/path/to/polygon-edge
   ```

4. **Network planning**: know the **reachable** IPv4 or DNS names for the two hosts that will run **fullnode-1** and **fullnode-2** (libp2p on port **10002** between them and toward validators). You plug these in when generating genesis.

---

## What gets created

All paths below assume output directory `./aws-ibft-out` (configurable with `--output`).

| Path | Purpose |
|------|---------|
| `aws-ibft-out/validator-1`, `validator-2` | IBFT signer secrets (addresses in genesis validator set). |
| `aws-ibft-out/fullnode-1`, `fullnode-2` | Non-validator node secrets (peer/RPC; **not** in validator set). |
| `aws-ibft-out/genesis.json` | One chain definition; **same file on every host**. |
| `aws-ibft-out/bundle-*.tar.gz` | Optional archives for copying to each machine. |

---

## Step 1 — Create secrets (empty → four data dirs)

From the repo root:

```bash
./scripts/aws-ibft/init-secrets.sh --output ./aws-ibft-out
```

- Creates `validator-1`, `validator-2`, `fullnode-1`, `fullnode-2` using `polygon-edge secrets init --insecure`.
- **Idempotent**: if a directory already has `consensus/validator.key`, that role is skipped.
- **Start over** (destructive):

  ```bash
  ./scripts/aws-ibft/init-secrets.sh --output ./aws-ibft-out --reset
  ```

`--insecure` is for **local generation only**; protect the resulting directories like production keys.

---

## Step 2 — Inspect Node IDs and validator lines

```bash
./scripts/aws-ibft/print-info.sh --output ./aws-ibft-out
```

You need:

- **Full node Node IDs** for **bootnodes** in genesis (multiaddrs must end with `/p2p/<FullNodePeerID>`).
- **Validator `address:BLSkey` lines** if you ever build `genesis` manually; the scripted flow reads them from disk automatically.

---

## Step 3 — Write `genesis.json`

Use **[`genesis-from-manifest.sh`](../scripts/aws-ibft/genesis-from-manifest.sh)** only. It reads chain parameters from a manifest (default: [`scripts/aws-ibft/manifest.example`](../scripts/aws-ibft/manifest.example)) and validator keys from `validator-1` / `validator-2` under `--output`.

**Bootnodes** must target hosts where **full nodes** listen on **TCP 10002** (see [aws-ibft-topology.md](aws-ibft-topology.md)). Pick **one** approach:

### A — Pass IPv4 for each full-node host (simplest)

Uses Node IDs from `fullnode-1` / `fullnode-2` automatically:

```bash
./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out \
  --ip1 10.0.1.10 --ip2 10.0.1.11
```

### B — Pass DNS hostnames

Builds `/dns4/.../tcp/10002/p2p/<NodeID>` multiaddrs:

```bash
./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out \
  --dns1 fullnode-1.vpc.internal --dns2 fullnode-2.vpc.internal
```

### C — Set bootnodes in the manifest

1. Copy and edit chain params / premines as needed. Validator ECDSA addresses from `validator-1` / `validator-2` are **premined automatically** (`PREMINE_VALIDATOR_WEI`) unless you list them in `PREMINE_*` or set `INCLUDE_VALIDATOR_PREMINE=0` in the manifest.

   ```bash
   cp scripts/aws-ibft/manifest.example ./aws-ibft-out/my.env
   ```

2. Set **`BOOTNODE_1`** and **`BOOTNODE_2`** to full multiaddrs (from `print-info.sh` Node IDs), e.g. `/ip4/<ip>/tcp/10002/p2p/<fullnode-1 Node ID>`.

3. Run:

   ```bash
   ./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out --manifest ./aws-ibft-out/my.env
   ```

Do **not** mix `--ip1`/`--ip2` with `--dns1`/`--dns2`. If you pass IP or DNS flags, they **override** any `BOOTNODE_*` lines in the manifest for that run.

**Note:** `polygon-edge genesis` refuses to overwrite an existing `genesis.json`. Remove it first or use a fresh `--output` directory if you need to regenerate.

---

## Step 4 — Pack bundles for each host

```bash
./scripts/aws-ibft/pack-bundles.sh --output ./aws-ibft-out
```

Produces under `./aws-ibft-out/`:

- `bundle-validator-1.tar.gz` … `genesis.json` + `validator-1/`
- `bundle-validator-2.tar.gz`
- `bundle-fullnode-1.tar.gz`
- `bundle-fullnode-2.tar.gz`

Copy the right tarball to the matching EC2 instance, unpack, and point `polygon-edge server` at that data directory and the shared `genesis.json`.

---

## Step 5 — Run the node (outline)

On each host, after unpacking:

```bash
polygon-edge server \
  --data-dir /path/to/validator-1 \
  --chain /path/to/genesis.json \
  --libp2p 0.0.0.0:10002 \
  --grpc-address 127.0.0.1:9632 \
  --jsonrpc 127.0.0.1:8545
```

Use the **bundle’s** directory name (`validator-1` vs `fullnode-1`, etc.). Tighten gRPC/JSON-RPC bind addresses for production. See [`scripts/aws-ibft/polygon-edge.service`](../scripts/aws-ibft/polygon-edge.service).

---

## Quick reference (happy path)

```bash
cd /path/to/ucl
go build -o polygon-edge .

./scripts/aws-ibft/init-secrets.sh --output ./aws-ibft-out
./scripts/aws-ibft/print-info.sh --output ./aws-ibft-out

# Edit scripts/aws-ibft/manifest.example chain params if needed, then:
./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out --ip1 <FN1_IP> --ip2 <FN2_IP>

./scripts/aws-ibft/pack-bundles.sh --output ./aws-ibft-out
```

---

## Troubleshooting

| Issue | What to check |
|-------|----------------|
| `EDGE_BIN` / `polygon-edge` not found | Build with `go build -o polygon-edge .` or set `export EDGE_BIN=...`. |
| Genesis fails on base fee | Do not set `BASE_FEE_CONFIG` to an invalid value; omit it or use a valid `fee:em:denom` triple. |
| Wrong peers / no blocks | Bootnode IPs/DNS must match where **full nodes** listen on **10002**; security groups must allow FN↔FN and V↔FN on that port. |
| Regenerate only genesis | Remove `aws-ibft-out/genesis.json`, then re-run `genesis-from-manifest.sh` with the same `--output`. |

---

## See also

- [scripts/aws-ibft/README.md](../scripts/aws-ibft/README.md) — short README next to the scripts.
- [aws-ibft-topology.md](aws-ibft-topology.md) — roles, port 10002, validator vs full node.
- [genesis-template-values.md](genesis-template-values.md) — placeholder meanings for manual `genesis` commands.
