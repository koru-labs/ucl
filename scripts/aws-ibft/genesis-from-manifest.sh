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
#   A) Set BOOTNODE_1 .. BOOTNODE_N in the manifest (full multiaddrs), or
#   B) Pass --bootnode-ip once per bootnode host, or
#   C) Pass --bootnode-dns once per bootnode host.
#
# Options B/C read Node IDs from fullnode-* dirs under --output in numeric order.
# Extra rpc-* dirs under OUTPUT are ignored by genesis generation.

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SCRIPT_DIR="$(dirname "$0")"
# shellcheck source=/dev/null
source "${SCRIPT_DIR}/lib.sh"

DEFAULT_MANIFEST="${SCRIPT_DIR}/manifest.example"
OUTPUT="${ROOT}/aws-ibft-out"
MANIFEST=""
BOOTNODE_IPS=()
BOOTNODE_DNS=()
LEGACY_IP1=""
LEGACY_IP2=""
LEGACY_DNS1=""
LEGACY_DNS2=""

usage() {
  echo "Usage: $0 --output DIR [options]"
  echo ""
  echo "  --manifest FILE     Manifest with KEY=value (default: scripts/aws-ibft/manifest.example)"
  echo "  --output DIR        Contains validator-* dirs and optionally fullnode-* / rpc-*"
  echo ""
  echo "  Bootnodes - pick one:"
  echo "    (default) BOOTNODE_1 .. BOOTNODE_N in the manifest"
  echo "    --bootnode-ip A   Repeat to build /ip4/A/tcp/10002/p2p/<id> from fullnode-* Node IDs"
  echo "    --bootnode-dns H  Repeat to build /dns4/H/tcp/10002/p2p/<id> from fullnode-* Node IDs"
  echo ""
  echo "  Compatibility aliases:"
  echo "    --ip1 A --ip2 B"
  echo "    --dns1 H --dns2 H"
  echo ""
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --manifest) MANIFEST="$2"; shift 2 ;;
    --output) OUTPUT="$2"; shift 2 ;;
    --bootnode-ip) BOOTNODE_IPS+=( "$2" ); shift 2 ;;
    --bootnode-dns) BOOTNODE_DNS+=( "$2" ); shift 2 ;;
    --ip1) LEGACY_IP1="$2"; shift 2 ;;
    --ip2) LEGACY_IP2="$2"; shift 2 ;;
    --dns1) LEGACY_DNS1="$2"; shift 2 ;;
    --dns2) LEGACY_DNS2="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

[[ -z "$MANIFEST" ]] && MANIFEST="$DEFAULT_MANIFEST"
[[ -f "$MANIFEST" ]] || aws_ibft_die "manifest not found: $MANIFEST"

validator_dirs=()
while IFS= read -r dir; do
  [[ -n "$dir" ]] || continue
  validator_dirs+=( "$dir" )
done < <(aws_ibft_list_role_dirs "$OUTPUT" validator)
[[ ${#validator_dirs[@]} -gt 0 ]] || aws_ibft_die "run init-secrets.sh first (missing validator-* dirs under ${OUTPUT})"

fullnode_dirs=()
while IFS= read -r dir; do
  [[ -n "$dir" ]] || continue
  fullnode_dirs+=( "$dir" )
done < <(aws_ibft_list_role_dirs "$OUTPUT" fullnode)

if [[ -n "$LEGACY_IP1" || -n "$LEGACY_IP2" || -n "$LEGACY_DNS1" || -n "$LEGACY_DNS2" ]]; then
  if [[ ${#BOOTNODE_IPS[@]} -gt 0 || ${#BOOTNODE_DNS[@]} -gt 0 ]]; then
    aws_ibft_die "do not mix --ip1/--ip2 or --dns1/--dns2 with --bootnode-ip/--bootnode-dns"
  fi
  if [[ -n "$LEGACY_IP1" || -n "$LEGACY_IP2" ]]; then
    [[ -n "$LEGACY_IP1" && -n "$LEGACY_IP2" && -z "$LEGACY_DNS1" && -z "$LEGACY_DNS2" ]] || \
      aws_ibft_die "use both --ip1 and --ip2 (not mixed with DNS flags)"
    BOOTNODE_IPS+=( "$LEGACY_IP1" "$LEGACY_IP2" )
  else
    [[ -n "$LEGACY_DNS1" && -n "$LEGACY_DNS2" && -z "$LEGACY_IP1" && -z "$LEGACY_IP2" ]] || \
      aws_ibft_die "use both --dns1 and --dns2 (not mixed with IP flags)"
    BOOTNODE_DNS+=( "$LEGACY_DNS1" "$LEGACY_DNS2" )
  fi
fi

boot_mode=manifest
if [[ ${#BOOTNODE_IPS[@]} -gt 0 && ${#BOOTNODE_DNS[@]} -gt 0 ]]; then
  aws_ibft_die "use either --bootnode-ip or --bootnode-dns (not both)"
elif [[ ${#BOOTNODE_IPS[@]} -gt 0 ]]; then
  boot_mode=ip
elif [[ ${#BOOTNODE_DNS[@]} -gt 0 ]]; then
  boot_mode=dns
fi

# shellcheck source=/dev/null
set -a
# shellcheck disable=SC1090
source "$MANIFEST"
set +a

BOOTNODES=()
case "$boot_mode" in
  ip)
    [[ ${#fullnode_dirs[@]} -ge ${#BOOTNODE_IPS[@]} ]] || \
      aws_ibft_die "need at least ${#BOOTNODE_IPS[@]} fullnode-* dirs under ${OUTPUT} for --bootnode-ip"
    for i in "${!BOOTNODE_IPS[@]}"; do
      node_id="$(aws_ibft_secrets_node_id "${fullnode_dirs[$i]}")"
      BOOTNODES+=( "/ip4/${BOOTNODE_IPS[$i]}/tcp/10002/p2p/${node_id}" )
    done
    ;;
  dns)
    [[ ${#fullnode_dirs[@]} -ge ${#BOOTNODE_DNS[@]} ]] || \
      aws_ibft_die "need at least ${#BOOTNODE_DNS[@]} fullnode-* dirs under ${OUTPUT} for --bootnode-dns"
    for i in "${!BOOTNODE_DNS[@]}"; do
      node_id="$(aws_ibft_secrets_node_id "${fullnode_dirs[$i]}")"
      BOOTNODES+=( "/dns4/${BOOTNODE_DNS[$i]}/tcp/10002/p2p/${node_id}" )
    done
    ;;
  manifest)
    while IFS= read -r var_name; do
      [[ -n "$var_name" ]] || continue
      value="${!var_name:-}"
      [[ -n "$value" ]] || aws_ibft_die "${var_name} is empty in ${MANIFEST}"
      BOOTNODES+=( "$value" )
    done < <(aws_ibft_list_indexed_vars "BOOTNODE_")
    [[ ${#BOOTNODES[@]} -gt 0 ]] || \
      aws_ibft_die "set BOOTNODE_1..N in manifest or use --bootnode-ip/--bootnode-dns"
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

VALIDATOR_LABELS=()
VALIDATOR_ADDRS=()
VALIDATOR_LINES=()
HAVE_VALIDATOR_PREMINE=()

for dir in "${validator_dirs[@]}"; do
  label="${dir##*/}"
  addr="$(aws_ibft_secrets_address "$dir")"
  bls="$(aws_ibft_secrets_bls "$dir")"
  VALIDATOR_LABELS+=( "$label" )
  VALIDATOR_ADDRS+=( "$addr" )
  VALIDATOR_LINES+=( "${addr}:${bls}" )
  HAVE_VALIDATOR_PREMINE+=( 0 )
done

# Case-insensitive 0x address compare (hex body only).
aws_ibft_addr_same() {
  local a b
  a="$(echo "$1" | sed 's/^0[xX]//' | tr '[:upper:]' '[:lower:]')"
  b="$(echo "$2" | sed 's/^0[xX]//' | tr '[:upper:]' '[:lower:]')"
  [[ "$a" == "$b" ]]
}

validator_index_by_label() {
  local target="$1" i
  for i in "${!VALIDATOR_LABELS[@]}"; do
    if [[ "${VALIDATOR_LABELS[$i]}" == "$target" ]]; then
      echo "$i"
      return 0
    fi
  done
  return 1
}

validator_address_by_label() {
  local idx
  idx="$(validator_index_by_label "$1")" || return 1
  echo "${VALIDATOR_ADDRS[$idx]}"
}

mark_validator_premine_by_label() {
  local idx
  idx="$(validator_index_by_label "$1")" || return 1
  HAVE_VALIDATOR_PREMINE[$idx]=1
}

mark_validator_premine_by_addr() {
  local target="$1" i
  for i in "${!VALIDATOR_ADDRS[@]}"; do
    if aws_ibft_addr_same "$target" "${VALIDATOR_ADDRS[$i]}"; then
      HAVE_VALIDATOR_PREMINE[$i]=1
    fi
  done
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
while IFS= read -r var_name; do
  [[ -n "$var_name" ]] || continue
  value="${!var_name:-}"
  [[ -n "$value" ]] || continue
  case "$value" in
    auto:validator-*)
      validator_label="${value#auto:}"
      validator_addr="$(validator_address_by_label "$validator_label")" || \
        aws_ibft_die "unknown validator label in ${var_name}: ${validator_label}"
      weight="${PREMINE_VALIDATOR_WEI:-1000000000000000000000000}"
      PREMINE_ARGS+=( --premine "${validator_addr}:${weight}" )
      mark_validator_premine_by_label "$validator_label"
      ;;
    *)
      premine_addr="${value%%:*}"
      mark_validator_premine_by_addr "$premine_addr"
      PREMINE_ARGS+=( --premine "$value" )
      ;;
  esac
done < <(aws_ibft_list_indexed_vars "PREMINE_")

# Add default premine for any validator not already listed in PREMINE_* (genesis last flag wins for dupes).
INCLUDE_VALIDATOR_PREMINE="${INCLUDE_VALIDATOR_PREMINE:-1}"
VALIDATOR_WEI="${PREMINE_VALIDATOR_WEI:-1000000000000000000000000}"
if [[ "$INCLUDE_VALIDATOR_PREMINE" != "0" ]]; then
  for i in "${!VALIDATOR_LABELS[@]}"; do
    if [[ "${HAVE_VALIDATOR_PREMINE[$i]}" -eq 0 ]]; then
      PREMINE_ARGS+=( --premine "${VALIDATOR_ADDRS[$i]}:${VALIDATOR_WEI}" )
    fi
  done
fi

EPOCH_REWARD="${EPOCH_REWARD:-0}"

echo "Validators (${#VALIDATOR_LINES[@]}):"
for i in "${!VALIDATOR_LABELS[@]}"; do
  echo "  ${VALIDATOR_LABELS[$i]} => ${VALIDATOR_LINES[$i]}"
done
echo

echo "Bootnodes (${#BOOTNODES[@]}):"
for bootnode in "${BOOTNODES[@]}"; do
  echo "  ${bootnode}"
done
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
)

for bootnode in "${BOOTNODES[@]}"; do
  CMD+=( --bootnode "$bootnode" )
done

for validator_line in "${VALIDATOR_LINES[@]}"; do
  CMD+=( --validators "$validator_line" )
done

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
