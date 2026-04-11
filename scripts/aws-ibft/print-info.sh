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

print_group() {
  local prefix="$1" found=0 dir
  while IFS= read -r dir; do
    [[ -n "$dir" ]] || continue
    found=1
    print_role "${dir##*/}" "$dir"
  done < <(aws_ibft_list_role_dirs "$OUTPUT" "$prefix")

  if [[ "$found" -eq 0 ]]; then
    echo "=== ${prefix}-* (${OUTPUT}/${prefix}-*) ==="
    echo "(none found)"
    echo
  fi
}

print_group validator
print_group fullnode
print_group rpc

echo "Bootnode hints (use full-node Node IDs + DNS/IP :10002):"
while IFS= read -r dir; do
  [[ -n "$dir" ]] || continue
  if aws_ibft_has_secrets "$dir"; then
    echo "  ${dir##*/}: /dns4/<${dir##*/}-host>/tcp/10002/p2p/$(aws_ibft_secrets_node_id "$dir")"
  fi
done < <(aws_ibft_list_role_dirs "$OUTPUT" fullnode)
echo "RPC nodes should use the same genesis and peer via fullnode-* roles, not as bootnodes."
