#!/usr/bin/env bash

dp_error_flag=0

# Parse arguments - extract consensus, command, and flags
consensus=""
command_arg=""
docker_flag=""
write_logs_flag=""
premine_addresses=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    ibft)
      consensus="$1"
      shift
      ;;
    polybft)
      echo "PolyBFT is no longer supported (BLS validator keys removed)."
      echo "Use: cluster ibft"
      exit 1
      ;;
    --docker)
      docker_flag="--docker"
      shift
      ;;
    --help)
      # Will be handled below
      consensus="--help"
      shift
      ;;
    --premine)
      if [[ -n "$2" ]] && [[ "$2" != --* ]]; then
        IFS=',' read -ra premine_addresses <<< "$2"
        shift 2
      else
        echo "Error: --premine requires a comma-separated list of addresses."
        exit 1
      fi
      ;;
    stop|destroy|write-logs)
      command_arg="$1"
      shift
      ;;
    *)
      echo "Unknown argument: $1"
      shift
      ;;
  esac
done



# Check if docker-compose is installed
if [[ "$docker_flag" == "--docker" ]] && ! command -v docker-compose >/dev/null 2>&1; then
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
  echo "Commands:"
  echo "  stop            Stop the running environment"
  echo "  destroy         Destroy the running environment"
  echo "  write-logs      Writes STDOUT and STDERR output to log file. Not applicable when using --docker flag."
  echo "Flags:"
  echo "  --docker        Run using Docker (requires docker-compose)."
  echo "  --premine       Comma-separated list of addresses to premine."
  echo "                  Example: --premine 0xAddr1,0xAddr2,0xAddr3"
  echo "                  If not provided, default addresses are used."
  echo "  --help          Display this help information"
  echo "Examples:"
  echo "  cluster ibft -- Run the script with IBFT consensus"
  echo "  cluster ibft --docker -- Run the script with IBFT consensus using docker"
  echo "  cluster ibft stop -- Stop the running environment"
  echo "  cluster ibft --premine 0xABC,0xDEF -- Run with custom premine addresses"
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


function createGenesis() {
  # Build premine arguments dynamically
  local premine_args=""
  for addr in "${premine_addresses[@]}"; do
    premine_args+="--premine ${addr} "
  done

  ./polygon-edge genesis $genesis_params \
    --block-gas-limit 10000000 \
    --premine 0x85da99c8a7c2c95964c8efd687e95e632fc533d6 \
    --premine 0x0000000000000000000000000000000000000000 \
    ${premine_args} \
    --epoch-size 10 \
    --burn-contract 0:0x0000000000000000000000000000000000000000
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
if [[ "$consensus" == "--help" ]] || [[ -z "$consensus" ]]; then
  showhelp
  exit 0
fi

# Reset test-dirs
rm -rf test-chain-*
rm -f genesis.json

# Build binary
go build -o polygon-edge .

# If --docker flag is set run docker environment otherwise run from binary
case "$docker_flag" in
"--docker")
  # cluster {consensus} --docker destroy
  if [ "$command_arg" == "destroy" ]; then
    destroyDockerEnvironment
    echo "Docker $consensus environment destroyed!"
    exit 0
  # cluster {consensus} --docker stop
  elif [ "$command_arg" == "stop" ]; then
    stopDockerEnvironment
    echo "Docker $consensus environment stopped!"
    exit 0
  fi

  # cluster {consensus} --docker
  echo "Running $consensus docker environment..."
  startServerFromDockerCompose $consensus
  echo "Docker $consensus environment deployed."
  exit 0
  ;;
# cluster {consensus}
*)
  echo "Running $consensus environment from local binary..."
  if [ "$consensus" == "ibft" ]; then
    initIbftConsensus
    createGenesis
    startNodes $consensus $command_arg
    exit 0
  else
    echo "Unsupported consensus mode. Supported mode: ibft"
    showhelp
    exit 1
  fi
  ;;
esac