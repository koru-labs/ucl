# AWS IBFT — local genesis and artifact prep

Scripts for a **coordinator machine** (laptop, CI, or bastion) with the `polygon-edge` binary built from this repo. They prepare:

- `validator-*` — secrets for IBFT signers (genesis validator set).
- `fullnode-*` — secrets for non-validator relay / peer nodes.
- optional `rpc-*` — extra non-validator RPC nodes that peer through the full nodes.
- `genesis.json` — IBFT PoA, explicit validators, bootnodes aimed at full nodes on **TCP 10002**.

They do **not** apply Terraform or SSH to EC2; copy the generated bundles to instances.

## Prerequisites

- `bash`, `polygon-edge` on `PATH` **or** set `EDGE_BIN` to the binary path.
- Run genesis-related commands from the **repository root** if `EDGE_BIN` is relative (`./polygon-edge`).

## Quick start

```bash
# 1. Create secrets (idempotent: skips dirs that already have secrets)
./scripts/aws-ibft/init-secrets.sh --output ./aws-ibft-out \
  --validator-count 4 --fullnode-count 3 --rpc-count 2

# 2. Inspect Node IDs and validator lines
./scripts/aws-ibft/print-info.sh --output ./aws-ibft-out

# 3. Write genesis — one command; default manifest is scripts/aws-ibft/manifest.example
#
#    Bootnodes: either pass --bootnode-ip / --bootnode-dns once per bootnode host,
#    or set BOOTNODE_1 .. BOOTNODE_N in a copied manifest and pass --manifest ./path/to/my.env
#
./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out \
  --bootnode-ip 10.0.1.10 \
  --bootnode-ip 10.0.1.11

# or DNS:
./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out \
  --bootnode-dns fullnode-1.vpc.internal \
  --bootnode-dns fullnode-2.vpc.internal

# or edit BOOTNODE_* in a copy of manifest.example, then:
./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out --manifest ./my.env

# 3b. Verify the genesis validator set matches validator-* secrets
./scripts/aws-ibft/check-genesis-validators.sh --output ./aws-ibft-out

# 4. Pack tarballs for each host (includes rpc-* if present)
./scripts/aws-ibft/pack-bundles.sh --output ./aws-ibft-out
```

## Manifest (`manifest.example`)

Chain parameters (`CHAIN_ID`, `EPOCH_SIZE`, `BLOCK_TIME`, premines, …) live in **[manifest.example](manifest.example)**. Copy and pass **`--manifest`** if you customize them.

**Bootnodes:**

- Omit **`BOOTNODE_1 .. BOOTNODE_N`** from the file when using repeated **`--bootnode-ip`** or **`--bootnode-dns`** flags.
- Repeated `--bootnode-ip` / `--bootnode-dns` entries map to discovered `fullnode-*` dirs in numeric order.
- Or set full multiaddrs in the manifest and do **not** pass bootnode flags.
- Keep bootnodes pointed at `fullnode-*`. Extra `rpc-*` nodes are not part of genesis validator or bootnode config.

## Reset output directory

```bash
./scripts/aws-ibft/init-secrets.sh --output ./aws-ibft-out --reset
```

## Runtime (on EC2)

Use the same `genesis.json` on every host.

**systemd (recommended):** set `ROLE` and JSON-RPC bind in `/etc/default/polygon-edge` (see [`polygon-edge.default.example`](polygon-edge.default.example)). The unit [`polygon-edge.service`](polygon-edge.service) expands `${UCL_BASE}/${ROLE}` for `--data-dir` and `WorkingDirectory`. On **RPC** hosts set `JSONRPC_BIND=0.0.0.0` so JSON-RPC listens on all interfaces; on validators and relay full nodes keep `JSONRPC_BIND=127.0.0.1` unless you intentionally expose the API.

Install:

```bash
sudo UCL_ROOT=/home/ubuntu/ucl-src /home/ubuntu/ucl-src/scripts/install_service.sh
sudo nano /etc/default/polygon-edge   # ROLE=…, JSONRPC_BIND=…
sudo systemctl enable --now polygon-edge
```

Manual `polygon-edge server` example (same flags as the unit):

```bash
/home/ubuntu/ucl-src/polygon-edge server \
  --data-dir /home/ubuntu/ucl-src/aws-ibft-out/validator-1 \
  --chain /home/ubuntu/ucl-src/aws-ibft-out/genesis.json \
  --libp2p 0.0.0.0:10002 \
  --grpc-address 127.0.0.1:9632 \
  --jsonrpc 127.0.0.1:8545
```

On `rpc-*` hosts use `--data-dir …/rpc-N` and `--jsonrpc 0.0.0.0:8545`; keep libp2p peering aimed at the full-node layer.

## Copying the repo to testnet hosts

Use **[`scripts/sync-repo-to-testnet.sh`](../sync-repo-to-testnet.sh)** from the repo root to `rsync` the tree over SSH (works with `ProxyCommand` / tunnel hosts defined in `~/.ssh/config`).

## Related docs

- [docs/aws-ibft-scripts-from-zero.md](../../docs/aws-ibft-scripts-from-zero.md) — full walkthrough from zero
- [docs/aws-ibft-topology.md](../../docs/aws-ibft-topology.md)
- [docs/genesis-template-values.md](../../docs/genesis-template-values.md)
