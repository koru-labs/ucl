#!/usr/bin/env bash
set -euo pipefail
# Install Polygon Edge systemd unit + default env (edit ROLE / JSONRPC_BIND on each host).
# Run on the server as root from any cwd.

UCL_ROOT="${UCL_ROOT:-/home/ubuntu/ucl-src}"

install -m 0644 "${UCL_ROOT}/scripts/aws-ibft/polygon-edge.service" /etc/systemd/system/polygon-edge.service

if [[ ! -f /etc/default/polygon-edge ]]; then
  install -m 0644 "${UCL_ROOT}/scripts/aws-ibft/polygon-edge.default.example" /etc/default/polygon-edge
  echo "Created /etc/default/polygon-edge — edit ROLE and JSONRPC_BIND, then: systemctl daemon-reload && systemctl enable --now polygon-edge"
else
  echo "/etc/default/polygon-edge already exists — not overwriting"
fi

systemctl daemon-reload
