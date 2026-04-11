#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=/dev/null
source "$(dirname "$0")/lib.sh"

OUTPUT="${ROOT}/aws-ibft-out"

usage() {
  echo "Usage: $0 [--output DIR]"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTPUT="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

BIN="$(aws_ibft_edge_bin)"

print_role() {
  local label="$1" dir="$2"
  echo "=== ${label} (${dir}) ==="
  if ! aws_ibft_has_secrets "$dir"; then
    echo "(not initialized — run init-secrets.sh)"
    echo
    return
  fi
  echo "Node ID:    $(aws_ibft_secrets_node_id "$dir")"
  echo "Address:    $(aws_ibft_secrets_address "$dir")"
  echo "BLS pubkey: $(aws_ibft_secrets_bls "$dir")"
  echo "Genesis --validators segment (BLS type):"
  echo "  $(aws_ibft_secrets_address "$dir"):$(aws_ibft_secrets_bls "$dir")"
  echo
}

print_role "validator-1" "${OUTPUT}/validator-1"
print_role "validator-2" "${OUTPUT}/validator-2"
print_role "fullnode-1" "${OUTPUT}/fullnode-1"
print_role "fullnode-2" "${OUTPUT}/fullnode-2"

echo "Bootnode hints (use full-node Node IDs + DNS/IP :10002):"
echo "  /dns4/<fullnode1-host>/tcp/10002/p2p/<fullnode-1 Node ID>"
echo "  /dns4/<fullnode2-host>/tcp/10002/p2p/<fullnode-2 Node ID>"
