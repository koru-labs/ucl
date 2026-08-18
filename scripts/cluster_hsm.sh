#!/usr/bin/env bash

dp_error_flag=0



# Check if docker-compose is installed
if [[ "$2" == "--docker" ]] && ! command -v docker-compose >/dev/null 2>&1; then
  echo "docker-compose is not installed."
  echo "Manual installation instructions: Visit https://docs.docker.com/compose/install/ for more information."
  dp_error_flag=1
fi

# Stop script if any of the dependencies have failed
if [[ "$dp_error_flag" -eq 1 ]]; then
  echo "Missing dependencies. Please install them and run the script again."
  exit 1
fi

function showhelp() {
  echo "Usage: cluster {consensus} [{command}] [{flags}]"
  echo "Consensus:"
  echo "  ibft            Start Supernets test environment locally with ibft consensus"
  echo "  ibft-hsm        Start Supernets test environment locally with ibft consensus and hsm signing"
  echo "Commands:"
  echo "  stop            Stop the running environment"
  echo "  destroy         Destroy the running environment"
  echo "  write-logs      Writes STDOUT and STDERR output to log file. Not applicable when using --docker flag."
  echo "Flags:"
  echo "  --docker        Run using Docker (requires docker-compose)."
  echo "  --help          Display this help information"
  echo "Examples:"
  echo "  cluster ibft -- Run the script with IBFT consensus"
  echo "  cluster ibft --docker -- Run the script with IBFT consensus using docker"
  echo "  cluster ibft stop -- Stop the running environment"
}

function initIbftConsensus() {
  echo "Running with ibft consensus"
  ./polygon-edge secrets init --insecure --data-dir test-chain- --num 4

  node1_id=$(./polygon-edge secrets output --data-dir test-chain-1 | grep Node | head -n 1 | awk -F ' ' '{print $4}')
  node2_id=$(./polygon-edge secrets output --data-dir test-chain-2 | grep Node | head -n 1 | awk -F ' ' '{print $4}')

  genesis_params="--consensus ibft --validators-prefix test-chain- \
    --bootnode /ip4/127.0.0.1/tcp/30301/p2p/$node1_id \
    --bootnode /ip4/127.0.0.1/tcp/30302/p2p/$node2_id"
}

function initIbftConsensusForHSM() {
  : "${HSM_MODULE:?--hsm-module is required}"
  : "${HSM_PIN:?--hsm-pin is required}"
  : "${HSM_TOKEN_LABEL:?--hsm-token-label is required}"
  : "${HSM_KEY_LABEL_BASE:?--hsm-key-label is required}"
  : "${HSM_PRIV_KEY_LABEL_BASE:?--hsm-priv-key-label is required}"

  echo "Running with ibft consensus with KMS"

  addresses=()
  pubkeys=()

  for i in {1..4}; do
      mkdir test-chain-$i test-chain-$i/bootstrap test-chain-$i/logs
      chmod -R 750 test-chain-$i

    ./polygon-edge secrets generate --dir test-chain-$i/bootstrap/secrets_config.json --type aws-ssm --name v$i --extra region=us-west-2,ssm-parameter-path=/ucl/ibft > /dev/null
    ./polygon-edge secrets output --config test-chain-$i/bootstrap/secrets_config.json --json > test-chain-$i/bootstrap/secrets.json
    
    if [ -z "$(jq -r '(.[0]? // .).node_id // empty' test-chain-$i/bootstrap/secrets.json)" ]; then
        ./polygon-edge secrets init --config test-chain-$i/bootstrap/secrets_config.json --json > test-chain-$i/bootstrap/secrets.json
    fi
    
    HSM_KEY_LABEL="${HSM_KEY_LABEL_BASE}-$i"
    HSM_PRIV_KEY_LABEL="${HSM_PRIV_KEY_LABEL_BASE}-$i"

    ./polygon-edge signer generate-config hsm \
      --hsm-lib-path "$HSM_MODULE" \
      --hsm-pin "$HSM_PIN" \
      --hsm-token-label "$HSM_TOKEN_LABEL" \
      --hsm-key-label "$HSM_KEY_LABEL" \
      --hsm-priv-key-label "$HSM_PRIV_KEY_LABEL" \
      --dir "test-chain-$i/bootstrap/signer_config.json"

    address=$(./polygon-edge secrets hsm-validator-address \
        --kms-config-path "test-chain-$i/bootstrap/signer_config.json" \
        --json | jq -r '.address')

    if [ -z "$address" ] || [ "$address" = "null" ]; then
        echo "failed to derive validator address for v$i" >&2
        exit 1
    fi

    addresses+=("$address")
  done

  node1_id=$(jq -r '(.[0]? // .).node_id' test-chain-1/bootstrap/secrets.json)

  node2_id=$(jq -r '(.[0]? // .).node_id' test-chain-2/bootstrap/secrets.json)

  node3_id=$(jq -r '(.[0]? // .).node_id' test-chain-3/bootstrap/secrets.json)

  node4_id=$(jq -r '(.[0]? // .).node_id' test-chain-4/bootstrap/secrets.json)

 echo "Validator addresses:"
  for i in "${!addresses[@]}"; do
      echo "  validator-$((i+1)): ${addresses[$i]}"
  done

  genesis_params="--consensus ibft \
    --validators-prefix "" \
    --validators-path "" \
    --validators ${addresses[0]#0x} \
    --validators ${addresses[1]#0x} \
    --validators ${addresses[2]#0x} \
    --validators ${addresses[3]#0x} \
    --bootnode /ip4/127.0.0.1/tcp/30301/p2p/$node1_id \
    --bootnode /ip4/127.0.0.1/tcp/30302/p2p/$node2_id \
    --bootnode /ip4/127.0.0.1/tcp/30303/p2p/$node3_id \
    --bootnode /ip4/127.0.0.1/tcp/30304/p2p/$node4_id"
}

HSM_MODULE_LOCAL="/usr/lib/x86_64-linux-gnu/softhsm/libsofthsm2.so"
HSM_PIN_LOCAL="1234"
HSM_TOKEN_LABEL_LOCAL="ibft-validator"

function initIbftConsensusForHSMLocal() {
  echo "Running with ibft consensus with HSM"

  addresses=()

  for i in {1..4}; do
      mkdir test-chain-$i test-chain-$i/bootstrap test-chain-$i/logs
      chmod -R 750 test-chain-$i

    ./polygon-edge secrets generate --dir test-chain-$i/bootstrap/secrets_config.json --type aws-ssm --name v$i --extra region=us-west-2,ssm-parameter-path=/ucl/ibft > /dev/null
    ./polygon-edge secrets output --config test-chain-$i/bootstrap/secrets_config.json --json > test-chain-$i/bootstrap/secrets.json
    
    if [ -z "$(jq -r '(.[0]? // .).node_id // empty' test-chain-$i/bootstrap/secrets.json)" ]; then
        ./polygon-edge secrets init --config test-chain-$i/bootstrap/secrets_config.json --json > test-chain-$i/bootstrap/secrets.json
    fi

    HSM_KEY_LABEL="ibft-validator-key-$i"

    ./polygon-edge signer generate-config hsm \
      --hsm-lib-path "$HSM_MODULE_LOCAL" \
      --hsm-pin "$HSM_PIN_LOCAL" \
      --hsm-token-label "$HSM_TOKEN_LABEL_LOCAL" \
      --hsm-key-label "$HSM_KEY_LABEL" \
      --dir "test-chain-$i/bootstrap/signer_config.json"

    pkcs11-tool --module "$HSM_MODULE_LOCAL" \
      --login --pin "$HSM_PIN_LOCAL" \
      --token-label "$HSM_TOKEN_LABEL_LOCAL" \
      --read-object --type pubkey \
      --label "$HSM_KEY_LABEL" \
      --output-file /tmp/pubkey.der 2>/dev/null

    pubkey_b64=$(base64 -w 0 /tmp/pubkey.der)
    rm /tmp/pubkey.der

    pubkeys+=("$pubkey_b64")
    address=$(./polygon-edge secrets pubkey-to-address --pubkey "$pubkey_b64" | jq -r '.address')
    addresses+=("$address")
  done

  node1_id=$(jq -r '(.[0]? // .).node_id' test-chain-1/bootstrap/secrets.json)

  node2_id=$(jq -r '(.[0]? // .).node_id' test-chain-2/bootstrap/secrets.json)

  node3_id=$(jq -r '(.[0]? // .).node_id' test-chain-3/bootstrap/secrets.json)

  node4_id=$(jq -r '(.[0]? // .).node_id' test-chain-4/bootstrap/secrets.json)

 echo "Validator addresses:"
  for i in "${!addresses[@]}"; do
      echo "  validator-$((i+1)): ${addresses[$i]}"
  done

  genesis_params="--consensus ibft \
    --validators-prefix "" \
    --validators-path "" \
    --validators ${addresses[0]#0x} \
    --validators ${addresses[1]#0x} \
    --validators ${addresses[2]#0x} \
    --validators ${addresses[3]#0x} \
    --bootnode /ip4/127.0.0.1/tcp/30301/p2p/$node1_id \
    --bootnode /ip4/127.0.0.1/tcp/30302/p2p/$node2_id \
    --bootnode /ip4/127.0.0.1/tcp/30303/p2p/$node3_id \
    --bootnode /ip4/127.0.0.1/tcp/30304/p2p/$node4_id"
}


function createGenesis() {
  ./polygon-edge genesis $genesis_params \
    --block-gas-limit 10000000 \
    --premine 0x85da99c8a7c2c95964c8efd687e95e632fc533d6 \
    --premine 0x0000000000000000000000000000000000000000 \
    --epoch-size 10 \
    --burn-contract 0:0x0000000000000000000000000000000000000000
}

function createGenesisForHSM() {
  ./polygon-edge genesis $genesis_params \
    --block-gas-limit 10000000 \
    --premine 0x85da99c8a7c2c95964c8efd687e95e632fc533d6 \
    --premine 0x0000000000000000000000000000000000000000 \
    --epoch-size 10 \
    --burn-contract 0:0x0000000000000000000000000000000000000000

  for i in {1..4}; do
    cp genesis.json test-chain-$i/bootstrap/
  done
}


function startNodes() {
  if [ "$2" == "write-logs" ]; then
    echo "Writing validators logs to the files..."
  fi

  for i in {1..4}; do
    data_dir="./test-chain-$i"
    grpc_port=$((10000 * $i))
    libp2p_port=$((30300 + $i))
    jsonrpc_port=$((10000 * $i + 2))

    log_file="./validator-$i.log"

    if [ "$2" == "write-logs" ]; then
      if [ ! -f "$log_file" ]; then
        touch "$log_file"
      fi

      ./polygon-edge server --data-dir "$data_dir" --chain genesis.json \
        --grpc-address ":$grpc_port" --libp2p ":$libp2p_port" --jsonrpc ":$jsonrpc_port" \
        --num-block-confirmations 2 \
        --json-rpc-batch-request-limit 0 \
        --gossip-msg-size 4194304 \
        --log-level DEBUG 2>&1 | tee $log_file &
    else
      ./polygon-edge server --data-dir "$data_dir" --chain genesis.json \
        --grpc-address ":$grpc_port" --libp2p ":$libp2p_port" --jsonrpc ":$jsonrpc_port" \
        --num-block-confirmations 2 \
        --json-rpc-batch-request-limit 0 \
        --gossip-msg-size 4194304 \
        --log-level DEBUG &
    fi

  done

  wait
}

function startNodesForHSM() {
  if [ "$2" == "write-logs" ]; then
    echo "Writing validators logs to the files..."
  fi

  for i in {1..4}; do
    data_dir="./test-chain-$i"
    grpc_port=$((10000 * $i))
    libp2p_port=$((30300 + $i))
    jsonrpc_port=$((10000 * $i + 2))

    log_file="$data_dir/logs/validator-$i.log"

    if [ "$2" == "write-logs" ]; then
      ./polygon-edge server --data-dir "$data_dir" \
        --secrets-config "$data_dir/bootstrap/secrets_config.json" \
        --chain "$data_dir/bootstrap/genesis.json" \
        --grpc-address ":$grpc_port" \
        --libp2p ":$libp2p_port" \
        --jsonrpc ":$jsonrpc_port" \
        --signer-config "test-chain-$i/bootstrap/signer_config.json" \
        --num-block-confirmations 2 \
        --json-rpc-batch-request-limit 0 \
        --gossip-msg-size 4194304 \
        --log-level DEBUG \
        --log-to $log_file &
    else
      ./polygon-edge server --data-dir "$data_dir" \
        --secrets-config "$data_dir/bootstrap/secrets_config.json" \
        --chain "$data_dir/bootstrap/genesis.json" \
        --grpc-address ":$grpc_port" \
        --libp2p ":$libp2p_port" \
        --jsonrpc ":$jsonrpc_port" \
        --signer-config "test-chain-$i/bootstrap/signer_config.json" \
        --num-block-confirmations 2 \
        --json-rpc-batch-request-limit 0 \
        --gossip-msg-size 4194304 \
        --log-level DEBUG &
    fi

  done

  wait
}

function startServerFromDockerCompose() {
  export EDGE_CONSENSUS="$CONSENSUS"

  docker-compose -f ./docker/local/docker-compose.yml up -d --build
}

function destroyDockerEnvironment() {
  docker-compose -f ./docker/local/docker-compose.yml down -v
}

function stopDockerEnvironment() {
  docker-compose -f ./docker/local/docker-compose.yml stop
}

set -e

# Show help if help flag is entered or no arguments are provided
if [[ "$1" == "--help" ]] || [[ $# -eq 0 ]]; then
  showhelp
  exit 0
fi

CONSENSUS="$1"
shift

# Parse HSM flags
HSM_MODULE=""
HSM_PIN=""
HSM_TOKEN_LABEL=""
HSM_KEY_LABEL_BASE="validator"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --hsm-module)      HSM_MODULE="$2";         shift 2 ;;
    --hsm-pin)         HSM_PIN="$2";            shift 2 ;;
    --hsm-token-label) HSM_TOKEN_LABEL="$2";    shift 2 ;;
    --hsm-priv-key-label) HSM_PRIV_KEY_LABEL_BASE="$2";  shift 2;;
    --hsm-key-label)   HSM_KEY_LABEL_BASE="$2"; shift 2 ;;
    *)                 break ;;
  esac
done

COMMAND="$1"  # write-logs, --docker, etc.

# Reset test-dirs
rm -rf test-chain-*
rm -f genesis.json

# Build binary
go build -o polygon-edge .

# If --docker flag is set run docker environment otherwise run from binary
case "$1" in
"--docker")
  # cluster {consensus} --docker destroy
  if [ "$2" == "destroy" ]; then
    destroyDockerEnvironment
    echo "Docker $CONSENSUS environment destroyed!"
    exit 0
  # cluster {consensus} --docker stop
  elif [ "$2" == "stop" ]; then
    stopDockerEnvironment
    echo "Docker $CONSENSUS environment stopped!"
    exit 0
  fi

  # cluster {consensus} --docker
  echo "Running $CONSENSUS docker environment..."
  startServerFromDockerCompose $1
  echo "Docker $CONSENSUS environment deployed."
  exit 0
  ;;
# cluster {consensus}
*)
  echo "Running $CONSENSUS environment from local binary..."
  if [ "$CONSENSUS" == "ibft" ]; then
    initIbftConsensus
    createGenesis
    startNodes $CONSENSUS $1
    exit 0
  elif [ "$CONSENSUS" == "ibft-hsm" ]; then
    initIbftConsensusForHSM
    createGenesisForHSM
    startNodesForHSM $CONSENSUS $1
    exit 0
  elif [ "$CONSENSUS" == "ibft-hsm-local" ]; then
    initIbftConsensusForHSMLocal
    createGenesisForHSM
    startNodesForHSM $CONSENSUS $1
    exit 0
  elif [ "$CONSENSUS" == "polybft" ]; then
    echo "PolyBFT is no longer supported (BLS validator keys removed)."
    echo "Use an IBFT mode instead."
    showhelp
    exit 1
  else
    echo "Unsupported consensus mode. PolyBFT is no longer supported."
    showhelp
    exit 1
  fi
  ;;
esac