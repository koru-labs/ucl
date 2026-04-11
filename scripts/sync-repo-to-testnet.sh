#!/usr/bin/env bash
#
# Rsync this repository to a host reachable via SSH (including Host aliases that use
# ProxyCommand / AWS SSM tunnel in ~/.ssh/config — same as `ssh my-host`).
#
# Usage (from repository root):
#   ./scripts/sync-repo-to-testnet.sh <ssh-host-or-alias> [remote-directory]
#
# Examples:
#   ./scripts/sync-repo-to-testnet.sh testnet_fullnode_use11_0
#   ./scripts/sync-repo-to-testnet.sh testnet_fullnode_use11_0 ~/ucl
#   ./scripts/sync-repo-to-testnet.sh --dry-run ec2-user@10.0.1.5 /opt/ucl
#
set -euo pipefail

DRY_RUN=()
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

usage() {
  echo "Usage: $0 [--dry-run] <ssh-host-or-alias> [remote-directory]"
  echo "  Default remote directory: ~/ucl-src"
  echo "  Run from repo root; rsync copies the tree excluding VCS/IDE/build noise."
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=(--dry-run); shift ;;
    -h|--help) usage ;;
    *) break ;;
  esac
done

[[ $# -ge 1 ]] || usage

REMOTE_HOST="$1"
REMOTE_DIR="${2:-~/ucl-src}"

cd "$REPO_ROOT"

echo "Local:  $REPO_ROOT"
echo "Remote: ${REMOTE_HOST}:${REMOTE_DIR}"
echo ""

# rsync trailing slash on source = copy contents; we want full tree with top-level dir name preserved:
#   rsync -a ./ host:~/ucl-src/  -> puts files directly in ucl-src
# Better: sync so remote has ~/ucl-src/ with same top-level files as repo root.
# Using: rsync -a ./ host:~/ucl-src/ is correct for "contents of repo into remote folder"

# Bash 3.2 + set -u: "${DRY_RUN[@]}" with an empty array trips "unbound variable".
set +u
rsync "${DRY_RUN[@]}" -avz \
  --human-readable \
  --progress \
  --exclude '.git/' \
  --exclude '.vscode/' \
  --exclude '.idea/' \
  --exclude '.cursor/' \
  --exclude '.DS_Store' \
  --exclude 'node_modules/' \
  --exclude 'dist/' \
  --exclude 'artifacts/' \
  --exclude 'bin/' \
  --exclude 'vendor/' \
  --exclude 'coverage.out' \
  --exclude '*.log' \
  --exclude 'polygon-edge' \
  --exclude 'main' \
  --exclude 'main.exe' \
  --exclude 'test-chain*/' \
  --exclude 'test-rootchain*/' \
  --exclude 'polygon-edge-chain*/' \
  ./ "${REMOTE_HOST}:${REMOTE_DIR}/"
set -u

echo ""
echo "Done. On the remote: cd ${REMOTE_DIR} && go build -o polygon-edge ."
