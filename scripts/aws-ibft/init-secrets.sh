#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=/dev/null
source "$(dirname "$0")/lib.sh"

OUTPUT="${ROOT}/aws-ibft-out"
RESET=0

usage() {
  echo "Usage: $0 [--output DIR] [--reset]"
  echo "  Creates validator-1, validator-2, fullnode-1, fullnode-2 under DIR via secrets init."
  echo "  --reset  remove DIR first (destructive)."
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTPUT="$2"; shift 2 ;;
    --reset) RESET=1; shift ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

BIN="$(aws_ibft_edge_bin)"
mkdir -p "$OUTPUT"

if [[ "$RESET" -eq 1 ]]; then
  rm -rf "${OUTPUT:?}/validator-1" "${OUTPUT:?}/validator-2" "${OUTPUT:?}/fullnode-1" "${OUTPUT:?}/fullnode-2"
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

init_one validator-1
init_one validator-2
init_one fullnode-1
init_one fullnode-2

echo "done. Next: ./scripts/aws-ibft/print-info.sh --output ${OUTPUT}"
