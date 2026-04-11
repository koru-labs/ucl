#!/usr/bin/env bash
set -euo pipefail
#
# Single entry point for writing genesis.json from a manifest plus secrets under --output.
#
# Usage (from repo root):
#   ./scripts/aws-ibft/genesis-from-manifest.sh --output ./aws-ibft-out \
#     [--manifest scripts/aws-ibft/manifest.example]
#
# Bootnodes (choose one):
#   A) Set BOOTNODE_1 and BOOTNODE_2 in the manifest (full multiaddrs), or
#   B) Pass --ip1 and --ip2 (IPv4 for fullnode-1 and fullnode-2 hosts), or
#   C) Pass --dns1 and --dns2 (hostnames for /dns4/... multiaddrs).
#
# Options B/C read Node IDs from OUTPUT/fullnode-1 and OUTPUT/fullnode-2.

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(dirname "$0")"
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/lib.sh"

DEFAULT_MANIFEST="${SCRIPT_DIR}/manifest.example"
OUTPUT="${ROOT}/aws-ibft-out"
MANIFEST=""
IP1=""
IP2=""
DNS1=""
DNS2=""

usage() {
  echo "Usage: $0 --output DIR [options]"
  echo ""
  echo "  --manifest FILE   Manifest with KEY=value (default: scripts/aws-ibft/manifest.example)"
  echo "  --output DIR      Contains validator-1, validator-2, and for IP/DNS bootnodes: fullnode-1, fullnode-2"
  echo ""
  echo "  Bootnodes — pick one:"
  echo "    (default) Values BOOTNODE_1, BOOTNODE_2 in the manifest"
  echo "    --ip1 A --ip2 B     Build /ip4/A/tcp/10002/p2p/<id> using fullnode-1 / fullnode-2 Node IDs"
  echo "    --dns1 H --dns2 H   Build /dns4/H/tcp/10002/p2p/<id>"
  echo ""
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) MANIFEST="$2"; shift 2 ;;
    --output) OUTPUT="$2"; shift 2 ;;
    --ip1) IP1="$2"; shift 2 ;;
    --ip2) IP2="$2"; shift 2 ;;
    --dns1) DNS1="$2"; shift 2 ;;
    --dns2) DNS2="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

[[ -z "$MANIFEST" ]] && MANIFEST="$DEFAULT_MANIFEST"
[[ -f "$MANIFEST" ]] || aws_ibft_die "manifest not found: $MANIFEST"
[[ -d "${OUTPUT}/validator-1" && -d "${OUTPUT}/validator-2" ]] || aws_ibft_die "run init-secrets.sh first (missing validators under ${OUTPUT})"

boot_mode=manifest
if [[ -n "$IP1" || -n "$IP2" || -n "$DNS1" || -n "$DNS2" ]]; then
  if [[ -n "$IP1" && -n "$IP2" && -z "$DNS1" && -z "$DNS2" ]]; then
    boot_mode=ip
  elif [[ -n "$DNS1" && -n "$DNS2" && -z "$IP1" && -z "$IP2" ]]; then
    boot_mode=dns
  else
    aws_ibft_die "use both --ip1 and --ip2, or both --dns1 and --dns2 (not mixed)"
  fi
  [[ -d "${OUTPUT}/fullnode-1" && -d "${OUTPUT}/fullnode-2" ]] || aws_ibft_die "fullnode-1 and fullnode-2 required for --ip/--dns (run init-secrets.sh)"
fi

# shellcheck source=/dev/null
set -a
# shellcheck disable=SC1090
source "$MANIFEST"
set +a

case "$boot_mode" in
  ip)
    FN1="$(aws_ibft_secrets_node_id "${OUTPUT}/fullnode-1")"
    FN2="$(aws_ibft_secrets_node_id "${OUTPUT}/fullnode-2")"
    BOOTNODE_1="/ip4/${IP1}/tcp/10002/p2p/${FN1}"
    BOOTNODE_2="/ip4/${IP2}/tcp/10002/p2p/${FN2}"
    ;;
  dns)
    FN1="$(aws_ibft_secrets_node_id "${OUTPUT}/fullnode-1")"
    FN2="$(aws_ibft_secrets_node_id "${OUTPUT}/fullnode-2")"
    BOOTNODE_1="/dns4/${DNS1}/tcp/10002/p2p/${FN1}"
    BOOTNODE_2="/dns4/${DNS2}/tcp/10002/p2p/${FN2}"
    ;;
  manifest)
    : "${BOOTNODE_1:?Set BOOTNODE_1 in manifest or use --ip1/--ip2 or --dns1/--dns2}"
    : "${BOOTNODE_2:?Set BOOTNODE_2 in manifest or use --ip1/--ip2 or --dns1/--dns2}"
    ;;
esac

: "${CHAIN_ID:?Set CHAIN_ID in manifest}"
: "${EPOCH_SIZE:?Set EPOCH_SIZE in manifest}"
: "${BLOCK_TIME:?Set BLOCK_TIME in manifest}"
: "${BLOCK_GAS_LIMIT:?Set BLOCK_GAS_LIMIT in manifest}"

CHAIN_NAME="${CHAIN_NAME:-aws-ibft}"
IBFT_VALIDATOR_TYPE="${IBFT_VALIDATOR_TYPE:-bls}"
BURN_CONTRACT="${BURN_CONTRACT:-0:0x0000000000000000000000000000000000000000}"
GENESIS_PATH="${OUTPUT}/genesis.json"

V1_DIR="${OUTPUT}/validator-1"
V2_DIR="${OUTPUT}/validator-2"
ADDR1="$(aws_ibft_secrets_address "$V1_DIR")"
BLS1="$(aws_ibft_secrets_bls "$V1_DIR")"
ADDR2="$(aws_ibft_secrets_address "$V2_DIR")"
BLS2="$(aws_ibft_secrets_bls "$V2_DIR")"
VAL_LINE1="${ADDR1}:${BLS1}"
VAL_LINE2="${ADDR2}:${BLS2}"

# Case-insensitive 0x address compare (hex body only).
aws_ibft_addr_same() {
  local a b
  a="$(echo "$1" | sed 's/^0[xX]//' | tr '[:upper:]' '[:lower:]')"
  b="$(echo "$2" | sed 's/^0[xX]//' | tr '[:upper:]' '[:lower:]')"
  [[ "$a" == "$b" ]]
}

BIN="$(aws_ibft_edge_bin)"
case "$BIN" in
  /*) ;;
  */*) BIN="${ROOT}/${BIN}" ;;
  *) BIN="$(command -v "$BIN")" ;;
esac

case "$GENESIS_PATH" in
  /*) ;;
  *) GENESIS_PATH="${ROOT}/${GENESIS_PATH#./}" ;;
esac

# polygon-edge genesis defaults validators-prefix to test-chain-, which makes it scan the
# current working directory for test-chain-* and merge those validators into IBFT extraData.
# Run genesis from a clean temp directory so only the explicit --validators lines are used.
GENESIS_WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/aws-ibft-genesis.XXXXXX")"
cleanup() {
  rm -rf "$GENESIS_WORKDIR"
}
trap cleanup EXIT

PREMINE_ARGS=()
HAVE_PREMINE_V1=0
HAVE_PREMINE_V2=0
for i in $(seq 0 32); do
  k="PREMINE_${i}"
  v="${!k:-}"
  [[ -z "$v" ]] && continue
  case "$v" in
    auto:validator-1)
      w="${PREMINE_VALIDATOR_WEI:-1000000000000000000000000}"
      PREMINE_ARGS+=( --premine "${ADDR1}:${w}" )
      HAVE_PREMINE_V1=1
      ;;
    auto:validator-2)
      w="${PREMINE_VALIDATOR_WEI:-1000000000000000000000000}"
      PREMINE_ARGS+=( --premine "${ADDR2}:${w}" )
      HAVE_PREMINE_V2=1
      ;;
    *)
      addr="${v%%:*}"
      aws_ibft_addr_same "$addr" "$ADDR1" && HAVE_PREMINE_V1=1
      aws_ibft_addr_same "$addr" "$ADDR2" && HAVE_PREMINE_V2=1
      PREMINE_ARGS+=( --premine "$v" )
      ;;
  esac
done

# Add default premine for any validator not already listed in PREMINE_* (genesis last flag wins for dupes).
INCLUDE_VALIDATOR_PREMINE="${INCLUDE_VALIDATOR_PREMINE:-1}"
VAL_WEI="${PREMINE_VALIDATOR_WEI:-1000000000000000000000000}"
if [[ "$INCLUDE_VALIDATOR_PREMINE" != "0" ]]; then
  [[ "$HAVE_PREMINE_V1" -eq 0 ]] && PREMINE_ARGS+=( --premine "${ADDR1}:${VAL_WEI}" )
  [[ "$HAVE_PREMINE_V2" -eq 0 ]] && PREMINE_ARGS+=( --premine "${ADDR2}:${VAL_WEI}" )
fi

EPOCH_REWARD="${EPOCH_REWARD:-0}"

echo "Bootnodes:"
echo "  $BOOTNODE_1"
echo "  $BOOTNODE_2"
echo

CMD=(
  "$BIN" genesis
  --dir "$GENESIS_PATH"
  --name "$CHAIN_NAME"
  --chain-id "$CHAIN_ID"
  --consensus ibft
  --ibft-validator-type "$IBFT_VALIDATOR_TYPE"
  --epoch-size "$EPOCH_SIZE"
  --epoch-reward "$EPOCH_REWARD"
  --block-time "$BLOCK_TIME"
  --block-gas-limit "$BLOCK_GAS_LIMIT"
  --burn-contract "$BURN_CONTRACT"
  --bootnode "$BOOTNODE_1"
  --bootnode "$BOOTNODE_2"
  --validators "$VAL_LINE1"
  --validators "$VAL_LINE2"
)

if [[ -n "${REWARD_WALLET:-}" ]]; then
  CMD+=( --reward-wallet "$REWARD_WALLET" )
fi

if [[ -n "${PROXY_CONTRACTS_ADMIN:-}" ]]; then
  CMD+=( --proxy-contracts-admin "$PROXY_CONTRACTS_ADMIN" )
fi

if [[ -n "${BASE_FEE_CONFIG:-}" ]]; then
  CMD+=( --base-fee-config "$BASE_FEE_CONFIG" )
fi

if [[ ${#PREMINE_ARGS[@]} -gt 0 ]]; then
  CMD+=( "${PREMINE_ARGS[@]}" )
fi

echo "Running:"
printf ' %q' "${CMD[@]}"
echo
(cd "$GENESIS_WORKDIR" && "${CMD[@]}")

echo "Wrote $GENESIS_PATH"
