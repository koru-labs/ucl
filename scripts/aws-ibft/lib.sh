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

# $1 = output dir, $2 = role prefix (validator|fullnode|rpc)
# stdout = matching role dirs in numeric suffix order, one per line
aws_ibft_list_role_dirs() {
  local output prefix dir name
  output="$1"
  prefix="$2"

  shopt -s nullglob
  for dir in "${output}/${prefix}-"*; do
    [[ -d "$dir" ]] || continue
    name="${dir##*/}"
    if [[ "$name" =~ ^${prefix}-([0-9]+)$ ]]; then
      printf '%09d\t%s\n' "${BASH_REMATCH[1]}" "$dir"
    fi
  done | sort | while IFS=$'\t' read -r _ path; do
    printf '%s\n' "$path"
  done
  shopt -u nullglob
}

# $1 = manifest var prefix including trailing underscore (e.g. BOOTNODE_, PREMINE_)
# stdout = matching variable names in numeric suffix order, one per line
aws_ibft_list_indexed_vars() {
  local prefix name
  prefix="$1"

  while IFS= read -r name; do
    if [[ "$name" =~ ^${prefix}([0-9]+)$ ]]; then
      printf '%09d\t%s\n' "${BASH_REMATCH[1]}" "$name"
    fi
  done < <(compgen -A variable "$prefix") | sort | while IFS=$'\t' read -r _ var_name; do
    printf '%s\n' "$var_name"
  done
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
