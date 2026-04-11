#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"

OUTPUT="${ROOT}/aws-ibft-out"

usage() {
  echo "Usage: $0 [--output DIR]"
  echo "  Writes ${OUTPUT}/bundle-*.tar.gz with genesis.json + one data dir each."
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

pack validator-1
pack validator-2
pack fullnode-1
pack fullnode-2

echo "done."
