#!/bin/sh

set -e

POLYGON_EDGE_BIN=./polygon-edge
CHAIN_CUSTOM_OPTIONS=$(tr "\n" " " << EOL
--block-gas-limit 10000000
--epoch-size 10
--chain-id 51001
--name polygon-edge-docker
--premine 0x0000000000000000000000000000000000000000
--premine 0x228466F2C715CbEC05dEAbfAc040ce3619d7CF0B:0xD3C21BCECCEDA1000000
--premine 0xca48694ebcB2548dF5030372BE4dAad694ef174e:0xD3C21BCECCEDA1000000
--burn-contract 0:0x0000000000000000000000000000000000000000
EOL
)

# createGenesisConfig creates genesis configuration
createGenesisConfig() {
  local consensus_type="$1"
  local secrets="$2"
  shift 2
  echo "Generating $consensus_type Genesis file..."

  "$POLYGON_EDGE_BIN" genesis $CHAIN_CUSTOM_OPTIONS \
    --dir /data/genesis.json \
    --validators-path /data \
    --validators-prefix data- \
    --consensus $consensus_type \
    --bootnode "/dns4/node-1/tcp/1478/p2p/$(echo "$secrets" | jq -r '.[0] | .node_id')" \
    --bootnode "/dns4/node-2/tcp/1478/p2p/$(echo "$secrets" | jq -r '.[1] | .node_id')" \
    --bootnode "/dns4/node-3/tcp/1478/p2p/$(echo "$secrets" | jq -r '.[2] | .node_id')" \
    --bootnode "/dns4/node-4/tcp/1478/p2p/$(echo "$secrets" | jq -r '.[3] | .node_id')" \
    "$@"
}

case "$1" in
   "init")
      case "$2" in 
          "ibft")
              if [ -f "$GENESIS_PATH" ]; then
                  echo "Secrets have already been generated."
              else
                  echo "Generating IBFT secrets..."
                  secrets=$("$POLYGON_EDGE_BIN" secrets init --insecure --num 4 --data-dir /data/data- --json)
                  echo "Secrets have been successfully generated"

                  rm -f /data/genesis.json

                  createGenesisConfig "$2" "$secrets"
              fi
              ;;
          "polybft")
              echo "PolyBFT is no longer supported for local docker init (BLS validator keys removed)."
              echo "Use: $0 init ibft"
              exit 1
              ;;
          *)
              echo "Unsupported consensus type: $2 (supported: ibft)"
              exit 1
              ;;
      esac
      ;;
  "start-node-1")
    "$POLYGON_EDGE_BIN" server \
      --data-dir /data/data-1 \
      --chain /data/genesis.json \
      --grpc-address 0.0.0.0:9632 \
      --libp2p 0.0.0.0:1478 \
      --jsonrpc 0.0.0.0:8545 \
      --prometheus 0.0.0.0:5001
   ;;
   *)
      echo "Executing polygon-edge..."
      exec "$POLYGON_EDGE_BIN" "$@"
      ;;
esac
