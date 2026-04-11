#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(dirname "$0")"
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/lib.sh"

OUTPUT="${ROOT}/aws-ibft-out"

usage() {
  echo "Usage: $0 [--output DIR]"
  echo "  Writes ${OUTPUT}/bundle-*.tar.gz with genesis.json + one data dir each."
  echo "  Includes validator-*, fullnode-*, and rpc-* dirs that exist under OUTPUT."
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTPUT="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

G="${OUTPUT}/genesis.json"
[[ -f "$G" ]] || { echo "missing $G — run genesis-from-manifest.sh first" >&2; exit 1; }

cd "$OUTPUT"

pack() {
  local name="$1"
  [[ -d "$name" ]] || { echo "skip $name (missing dir)"; return; }
  local out="bundle-${name}.tar.gz"
  echo "creating $out"
  tar -czf "$out" genesis.json "$name"
}

for prefix in validator fullnode rpc; do
  while IFS= read -r dir; do
    [[ -n "$dir" ]] || continue
    pack "${dir##*/}"
  done < <(aws_ibft_list_role_dirs "$OUTPUT" "$prefix")
done

echo "done."
