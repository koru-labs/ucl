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
  echo "  ibft-ssm        Start Supernets test environment locally with ibft consensus and SSM secrets"
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

function initIbftConsensuForSSM() {
  echo "Running with ibft consensus with SSM secrets"

  for i in {1..4}; do
    mkdir test-chain-$i test-chain-$i/bootstrap test-chain-$i/logs
    chmod -R 750 test-chain-$i

    ./polygon-edge secrets generate --dir test-chain-$i/bootstrap/secrets_config.json --type aws-ssm --name v$i --extra region=us-west-2,ssm-parameter-path=/ucl/ibft > /dev/null
    ./polygon-edge secrets output --config test-chain-$i/bootstrap/secrets_config.json --json > test-chain-$i/bootstrap/secrets.json

    if [ -z "$(jq -r '(.[0]? // .).address // empty' test-chain-$i/bootstrap/secrets.json)" ]; then
      ./polygon-edge secrets init --config test-chain-$i/bootstrap/secrets_config.json --json > test-chain-$i/bootstrap/secrets.json
    fi

  done

  node1_id=$(jq -r '(.[0]? // .).node_id' test-chain-1/bootstrap/secrets.json)
  node1_address=$(jq -r '(.[0]? // .).address' test-chain-1/bootstrap/secrets.json)

  node2_id=$(jq -r '(.[0]? // .).node_id' test-chain-2/bootstrap/secrets.json)
  node2_address=$(jq -r '(.[0]? // .).address' test-chain-2/bootstrap/secrets.json)

  node3_id=$(jq -r '(.[0]? // .).node_id' test-chain-3/bootstrap/secrets.json)
  node3_address=$(jq -r '(.[0]? // .).address' test-chain-3/bootstrap/secrets.json)

  node4_id=$(jq -r '(.[0]? // .).node_id' test-chain-4/bootstrap/secrets.json)
  node4_address=$(jq -r '(.[0]? // .).address' test-chain-4/bootstrap/secrets.json)

  genesis_params="--consensus ibft \
    --validators-prefix "" \
    --validators-path "" \
    --validators ${node1_address#0x} \
    --validators ${node2_address#0x} \
    --validators ${node3_address#0x} \
    --validators ${node4_address#0x} \
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

function createGenesisForSSM() {
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

function startNodesForSSM() {
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
        --num-block-confirmations 2 \
        --json-rpc-batch-request-limit 0 \
        --gossip-msg-size 4194304 \
        --log-level DEBUG &
    fi

  done

  wait
}

function startServerFromDockerCompose() {
  export EDGE_CONSENSUS="$1"

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

# Reset test-dirs
rm -rf test-chain-*
rm -f genesis.json

# Build binary
go build -o polygon-edge .

# If --docker flag is set run docker environment otherwise run from binary
case "$2" in
"--docker")
  # cluster {consensus} --docker destroy
  if [ "$3" == "destroy" ]; then
    destroyDockerEnvironment
    echo "Docker $1 environment destroyed!"
    exit 0
  # cluster {consensus} --docker stop
  elif [ "$3" == "stop" ]; then
    stopDockerEnvironment
    echo "Docker $1 environment stopped!"
    exit 0
  fi

  # cluster {consensus} --docker
  echo "Running $1 docker environment..."
  startServerFromDockerCompose $1
  echo "Docker $1 environment deployed."
  exit 0
  ;;
# cluster {consensus}
*)
  echo "Running $1 environment from local binary..."
  if [ "$1" == "ibft" ]; then
    # Initialize ibft consensus
    initIbftConsensus
    # Create genesis file and start the server from binary
    createGenesis
    startNodes $1 $2
    exit 0
  elif [ "$1" == "ibft-ssm" ]; then
    # Initialize ibft consensus
    initIbftConsensuForSSM
    # Create genesis file and start the server from binary
    createGenesisForSSM
    startNodesForSSM $1 $2
    exit 0
  elif [ "$1" == "polybft" ]; then
    echo "PolyBFT is no longer supported (BLS validator keys removed)."
    echo "Use: cluster ibft"
    showhelp
    exit 1
  else
    echo "Unsupported consensus mode. Supported modes are IBFT variants only (no polybft)."
    showhelp
    exit 1
  fi
  ;;
esac