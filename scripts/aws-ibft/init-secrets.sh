#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=/dev/null
source "$(dirname "$0")/lib.sh"

OUTPUT="${ROOT}/aws-ibft-out"
RESET=0
VALIDATOR_COUNT=2
FULLNODE_COUNT=2
RPC_COUNT=2

usage() {
  echo "Usage: $0 [--output DIR] [--reset] [--validator-count N] [--fullnode-count N] [--rpc-count N]"
  echo "  Creates validator-1..N, fullnode-1..N, and optionally rpc-1..N under DIR via secrets init."
  echo "  --reset  remove DIR first (destructive)."
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTPUT="$2"; shift 2 ;;
    --reset) RESET=1; shift ;;
    --validator-count) VALIDATOR_COUNT="$2"; shift 2 ;;
    --fullnode-count) FULLNODE_COUNT="$2"; shift 2 ;;
    --rpc-count) RPC_COUNT="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

[[ "$VALIDATOR_COUNT" =~ ^[0-9]+$ ]] || aws_ibft_die "--validator-count must be a non-negative integer"
[[ "$FULLNODE_COUNT" =~ ^[0-9]+$ ]] || aws_ibft_die "--fullnode-count must be a non-negative integer"
[[ "$RPC_COUNT" =~ ^[0-9]+$ ]] || aws_ibft_die "--rpc-count must be a non-negative integer"

BIN="$(aws_ibft_edge_bin)"
mkdir -p "$OUTPUT"

if [[ "$RESET" -eq 1 ]]; then
  for prefix in validator fullnode rpc; do
    while IFS= read -r dir; do
      [[ -n "$dir" ]] || continue
      rm -rf "$dir"
    done < <(aws_ibft_list_role_dirs "$OUTPUT" "$prefix")
  done
fi

init_one() {
  local name="$1"
  local dir="${OUTPUT}/${name}"
  if aws_ibft_has_secrets "$dir"; then
    echo "skip $name (already initialized)"
    return
  fi
  mkdir -p "$dir"
  echo "init $name -> $dir"
  "$BIN" secrets init --insecure --data-dir "$dir"
}

init_many() {
  local prefix="$1" count="$2" i=1
  while [[ "$i" -le "$count" ]]; do
    init_one "${prefix}-${i}"
    i=$((i + 1))
  done
}

init_many validator "$VALIDATOR_COUNT"
init_many fullnode "$FULLNODE_COUNT"
init_many rpc "$RPC_COUNT"

echo "done. Next: ./scripts/aws-ibft/print-info.sh --output ${OUTPUT}"
