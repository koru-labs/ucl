#!/usr/bin/env bash
set -euo pipefail
#
# Inspect IBFT validators encoded in genesis.json and compare them to validator-* secrets.
#
# Usage (from repo root):
#   EDGE_BIN=./polygon-edge ./scripts/aws-ibft/check-genesis-validators.sh \
#     [--output ./aws-ibft-out] [--genesis ./aws-ibft-out/genesis.json]
#
# Notes:
#   - Expected validators are read from OUTPUT/validator-*
#   - Current parser supports validator_type=bls

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(dirname "$0")"
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/lib.sh"

OUTPUT="${ROOT}/aws-ibft-out"
GENESIS=""

usage() {
  echo "Usage: $0 [--output DIR] [--genesis FILE]"
  echo ""
  echo "  --output DIR    Directory containing validator-* data dirs (default: ./aws-ibft-out)"
  echo "  --genesis FILE  Genesis file to inspect (default: OUTPUT/genesis.json)"
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) OUTPUT="$2"; shift 2 ;;
    --genesis) GENESIS="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

[[ -n "$GENESIS" ]] || GENESIS="${OUTPUT}/genesis.json"
[[ -f "$GENESIS" ]] || aws_ibft_die "genesis not found: $GENESIS"
command -v python3 >/dev/null 2>&1 || aws_ibft_die "python3 not found"

shopt -s nullglob
validator_dirs=( "${OUTPUT}"/validator-* )
shopt -u nullglob
[[ ${#validator_dirs[@]} -gt 0 ]] || aws_ibft_die "no validator-* dirs found under ${OUTPUT}"

expected_pairs=()
for dir in "${validator_dirs[@]}"; do
  [[ -d "$dir" ]] || continue
  label="$(basename "$dir")"
  expected_pairs+=( "${label}=$(aws_ibft_secrets_address "$dir")" )
done

python3 - "$GENESIS" "${expected_pairs[@]}" <<'PY'
import json
import re
import sys

genesis_path = sys.argv[1]
expected_pairs = []
for raw in sys.argv[2:]:
    if "=" not in raw:
        print(f"invalid expected validator entry: {raw}", file=sys.stderr)
        sys.exit(2)
    label, addr = raw.split("=", 1)
    expected_pairs.append((label, addr))

with open(genesis_path, encoding="utf-8") as f:
    data = json.load(f)

ibft = data.get("params", {}).get("engine", {}).get("ibft", {})
validator_type = ibft.get("validator_type")
if validator_type != "bls":
    print(
        f"validator_type={validator_type!r} is not supported by this checker "
        "(expected 'bls')",
        file=sys.stderr,
    )
    sys.exit(2)

extra = data.get("genesis", {}).get("extraData")
if not isinstance(extra, str) or not extra.startswith("0x"):
    print("genesis.extraData is missing or invalid", file=sys.stderr)
    sys.exit(2)

# BLS validator entries in IBFT extraData look like:
#   94<20-byte-address>b0<48-byte-bls-pubkey>
actual = [f"0x{m}" for m in re.findall(r"94([0-9a-f]{40})b0[0-9a-f]{96}", extra[2:].lower())]
expected_addrs = [addr for _, addr in expected_pairs]

expected_lower = {addr.lower() for addr in expected_addrs}
actual_lower = {addr.lower() for addr in actual}

missing = [(label, addr) for label, addr in expected_pairs if addr.lower() not in actual_lower]
unexpected = [addr for addr in actual if addr.lower() not in expected_lower]

print(f"genesis: {genesis_path}")
print(f"validator_type: {validator_type}")
print("expected validators from validator-* secrets:")
for label, addr in expected_pairs:
    print(f"  {label}: {addr}")

print("validators encoded in genesis.extraData:")
if actual:
    for addr in actual:
        print(f"  {addr}")
else:
    print("  (none parsed)")

print(f"validator_count = {len(actual)}")

if missing:
    print("missing validators:")
    for label, addr in missing:
        print(f"  {label}: {addr}")

if unexpected:
    print("unexpected validators:")
    for addr in unexpected:
        print(f"  {addr}")

match = len(actual) == len(expected_pairs) and not missing and not unexpected
print(f"match = {match}")
sys.exit(0 if match else 1)
PY
