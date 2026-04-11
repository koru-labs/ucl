# AWS IBFT — local genesis and artifact prep

Scripts for a **coordinator machine** (laptop, CI, or bastion) with the `polygon-edge` binary built from this repo. They prepare:

- `validator-1`, `validator-2` — secrets for IBFT signers (genesis validator set).
- `fullnode-1`, `fullnode-2` — secrets for non-validator nodes (peer / RPC only).
- `genesis.json` — IBFT PoA, explicit validators, bootnodes aimed at full nodes on **TCP 10002**.

They do **not** apply Terraform or SSH to EC2; copy the generated bundles to instances.

## Prerequisites

- `bash`, `polygon-edge` on `PATH` **or** set `EDGE_BIN` to the binary path.
- Run genesis-related commands from the **repository root** if `EDGE_BIN` is relative (`./polygon-edge`).

## Quick start

```bash
# 1. Create secrets (idempotent: skips dirs that already have secrets)
./scripts/aws-ibft/init-secrets.sh --output ./aws-ibft-out

# 2. Inspect Node IDs and validator lines
./scripts/aws-ibft/print-info.sh --output ./aws-ibft-out

# 3. Write genesis — one command; default manifest is scripts/aws-ibft/manifest.example
#
#    Bootnodes: either pass IPv4 for each full node host, or DNS hostnames, or set
#    BOOTNODE_1 / BOOTNODE_2 in a copied manifest and pass --manifest ./path/to/my.env
#
./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out \
  --ip1 10.0.1.10 --ip2 10.0.1.11

# or DNS:
./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out \
  --dns1 fullnode-1.vpc.internal --dns2 fullnode-2.vpc.internal

# or edit BOOTNODE_* in a copy of manifest.example, then:
./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out --manifest ./my.env

# 3b. Verify the genesis validator set matches validator-* secrets
./scripts/aws-ibft/check-genesis-validators.sh --output ./aws-ibft-out

# 4. Pack tarballs for each host
./scripts/aws-ibft/pack-bundles.sh --output ./aws-ibft-out
```

## Manifest (`manifest.example`)

Chain parameters (`CHAIN_ID`, `EPOCH_SIZE`, `BLOCK_TIME`, premines, …) live in **[manifest.example](manifest.example)**. Copy and pass **`--manifest`** if you customize them.

**Bootnodes:**

- Omit **`BOOTNODE_1` / `BOOTNODE_2`** from the file when using **`--ip1`/`--ip2`** or **`--dns1`/`--dns2`** (flags override file if both are set).
- Or set full multiaddrs in the manifest and do **not** pass IP/DNS flags.

## Reset output directory

```bash
./scripts/aws-ibft/init-secrets.sh --output ./aws-ibft-out --reset
```

## Runtime (on EC2)

Use the same `genesis.json` on every host. Example:

```bash
/home/ubuntu/ucl-src/polygon-edge server \
  --data-dir /home/ubuntu/ucl-src/validator-1 \
  --chain /home/ubuntu/ucl-src/genesis.json \
  --libp2p 0.0.0.0:10002 \
  --grpc-address 127.0.0.1:9632 \
  --jsonrpc 127.0.0.1:8545
```

Tune bind addresses for your threat model (`127.0.0.1` vs `0.0.0.0` for gRPC/JSON-RPC). See [`polygon-edge.service`](polygon-edge.service) (runs as `ubuntu:ubuntu`).

## Copying the repo to testnet hosts

Use **[`scripts/sync-repo-to-testnet.sh`](../sync-repo-to-testnet.sh)** from the repo root to `rsync` the tree over SSH (works with `ProxyCommand` / tunnel hosts defined in `~/.ssh/config`).

## Related docs

- [docs/aws-ibft-scripts-from-zero.md](../../docs/aws-ibft-scripts-from-zero.md) — full walkthrough from zero
- [docs/aws-ibft-topology.md](../../docs/aws-ibft-topology.md)
- [docs/genesis-template-values.md](../../docs/genesis-template-values.md)
