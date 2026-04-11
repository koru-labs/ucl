#!/usr/bin/env bash
# Shared helpers for aws-ibft scripts. Source from repo root or script dir.

aws_ibft_die() {
  echo "error: $*" >&2
  exit 1
}

aws_ibft_repo_root() {
  local here
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  echo "$here"
}

aws_ibft_edge_bin() {
  if [[ -n "${EDGE_BIN:-}" ]]; then
    echo "$EDGE_BIN"
    return
  fi
  if command -v polygon-edge >/dev/null 2>&1; then
    command -v polygon-edge
    return
  fi
  aws_ibft_die "set EDGE_BIN or put polygon-edge on PATH"
}

# $1 = data dir
aws_ibft_has_secrets() {
  [[ -f "$1/consensus/validator.key" ]]
}

# $1 = data dir → stdout node id only
aws_ibft_secrets_node_id() {
  local bin dir
  bin="$(aws_ibft_edge_bin)"
  dir="$1"
  [[ -d "$dir" ]] || aws_ibft_die "missing data dir: $dir"
  "$bin" secrets output --data-dir "$dir" --node-id 2>/dev/null | tr -d '[:space:]'
}

# $1 = data dir → stdout 0x-prefixed address
aws_ibft_secrets_address() {
  local bin dir
  bin="$(aws_ibft_edge_bin)"
  dir="$1"
  "$bin" secrets output --data-dir "$dir" --validator 2>/dev/null | tr -d '[:space:]'
}

# $1 = data dir → stdout hex BLS pubkey (no 0x required downstream)
aws_ibft_secrets_bls() {
  local bin dir
  bin="$(aws_ibft_edge_bin)"
  dir="$1"
  "$bin" secrets output --data-dir "$dir" --bls 2>/dev/null | tr -d '[:space:]'
}
