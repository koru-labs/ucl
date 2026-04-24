#!/usr/bin/env bash

dp_error_flag=0

# Check if jq is installed
if [[ "$1" == "polybft" ]] && ! command -v jq >/dev/null 2>&1; then
  echo "jq is not installed."
  echo "Manual installation instructions: Visit https://jqlang.github.io/jq/ for more information."
  dp_error_flag=1
fi

# Check if curl is installed
if [[ "$1" == "polybft" ]] && ! command -v curl >/dev/null 2>&1; then
  echo "curl is not installed."
  echo "Manual installation instructions: Visit https://everything.curl.dev/get/ for more information."
  dp_error_flag=1
fi

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
  echo "  polybft         Start Supernets test environment locally with polybft consensus"
  echo "Commands:"
  echo "  stop            Stop the running environment"
  echo "  destroy         Destroy the running environment"
  echo "  write-logs      Writes STDOUT and STDERR output to log file. Not applicable when using --docker flag."
  echo "Flags:"
  echo "  --docker        Run using Docker (requires docker-compose)."
  echo "  --help          Display this help information"
  echo "Examples:"
  echo "  cluster polybft -- Run the script with the polybft consensus"
  echo "  cluster polybft --docker -- Run the script with the polybft consensus using docker"
  echo "  cluster polybft stop -- Stop the running environment"
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

    pkcs11-tool --module "$HSM_MODULE" \
      --login --pin "$HSM_PIN" \
      --token-label "$HSM_TOKEN_LABEL" \
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

function initPolybftConsensus() {
  echo "Running with polybft consensus"
  genesis_params="--consensus polybft"

  address1=$(./polygon-edge polybft-secrets --insecure --data-dir test-chain-1 | grep Public | head -n 1 | awk -F ' ' '{print $5}')
  address2=$(./polygon-edge polybft-secrets --insecure --data-dir test-chain-2 | grep Public | head -n 1 | awk -F ' ' '{print $5}')
  address3=$(./polygon-edge polybft-secrets --insecure --data-dir test-chain-3 | grep Public | head -n 1 | awk -F ' ' '{print $5}')
  address4=$(./polygon-edge polybft-secrets --insecure --data-dir test-chain-4 | grep Public | head -n 1 | awk -F ' ' '{print $5}')
}

function createGenesis() {
  ./polygon-edge genesis $genesis_params \
    --block-gas-limit 10000000 \
    --premine 0x85da99c8a7c2c95964c8efd687e95e632fc533d6 \
    --premine 0x0000000000000000000000000000000000000000 \
    --epoch-size 10 \
    --reward-wallet 0xDEADBEEF:1000000 \
    --native-token-config "Polygon:MATIC:18:true:$address1" \
    --burn-contract 0:0x0000000000000000000000000000000000000000 \
    --proxy-contracts-admin 0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed
}

function createGenesisForHSM() {
  ./polygon-edge genesis $genesis_params \
    --block-gas-limit 10000000 \
    --premine 0x85da99c8a7c2c95964c8efd687e95e632fc533d6 \
    --premine 0x0000000000000000000000000000000000000000 \
    --epoch-size 10 \
    --reward-wallet 0xDEADBEEF:1000000 \
    --native-token-config "Polygon:MATIC:18:true:$address1" \
    --burn-contract 0:0x0000000000000000000000000000000000000000 \
    --proxy-contracts-admin 0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed

  for i in {1..4}; do
    cp genesis.json test-chain-$i/bootstrap/
  done
}

function initRootchain() {
  echo "Initializing rootchain"

  if [ "$1" == "write-logs" ]; then
    echo "Writing rootchain server logs to the file..."
    ./polygon-edge rootchain server 2>&1 | tee ./rootchain-server.log &
  else
    ./polygon-edge rootchain server >/dev/null &
  fi

  set +e
  while true; do
    if curl -sSf -o /dev/null http://127.0.0.1:8545; then
      break
    fi
    sleep 1
  done
  set -e

  proxyContractsAdmin=0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed

  ./polygon-edge polybft stake-manager-deploy \
    --jsonrpc http://127.0.0.1:8545 \
    --proxy-contracts-admin ${proxyContractsAdmin} \
    --test

  stakeManagerAddr=$(cat genesis.json | jq -r '.params.engine.polybft.bridge.stakeManagerAddr')
  stakeToken=$(cat genesis.json | jq -r '.params.engine.polybft.bridge.stakeTokenAddr')

  ./polygon-edge rootchain deploy \
    --stake-manager ${stakeManagerAddr} \
    --stake-token ${stakeToken} \
    --proxy-contracts-admin ${proxyContractsAdmin} \
    --test

  customSupernetManagerAddr=$(cat genesis.json | jq -r '.params.engine.polybft.bridge.customSupernetManagerAddr')
  supernetID=$(cat genesis.json | jq -r '.params.engine.polybft.supernetID')

  ./polygon-edge rootchain fund \
    --stake-token ${stakeToken} \
    --mint \
    --addresses ${address1},${address2},${address3},${address4} \
    --amounts 1000000000000000000000000,1000000000000000000000000,1000000000000000000000000,1000000000000000000000000

  ./polygon-edge polybft whitelist-validators \
    --addresses ${address1},${address2},${address3},${address4} \
    --supernet-manager ${customSupernetManagerAddr} \
    --private-key aa75e9a7d427efc732f8e4f1a5b7646adcc61fd5bae40f80d13c8419c9f43d6d \
    --jsonrpc http://127.0.0.1:8545

  counter=1
  while [ $counter -le 4 ]; do
    echo "Registering validator: ${counter}"

    ./polygon-edge polybft register-validator \
      --supernet-manager ${customSupernetManagerAddr} \
      --data-dir test-chain-${counter} \
      --jsonrpc http://127.0.0.1:8545

    ./polygon-edge polybft stake \
      --data-dir test-chain-${counter} \
      --amount 1000000000000000000000000 \
      --supernet-id ${supernetID} \
      --stake-manager ${stakeManagerAddr} \
      --stake-token ${stakeToken} \
      --jsonrpc http://127.0.0.1:8545

    ((counter++))
  done

  ./polygon-edge polybft supernet \
    --private-key aa75e9a7d427efc732f8e4f1a5b7646adcc61fd5bae40f80d13c8419c9f43d6d \
    --supernet-manager ${customSupernetManagerAddr} \
    --finalize-genesis-set \
    --enable-staking \
    --jsonrpc http://127.0.0.1:8545
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

    relayer_arg=""
    # Start relayer only if running polybft and for the 1st node
    if [ "$1" == "polybft" ] && [ $i -eq 1 ]; then
      relayer_arg="--relayer"
    fi

    if [ "$2" == "write-logs" ]; then
      if [ ! -f "$log_file" ]; then
        touch "$log_file"
      fi

      ./polygon-edge server --data-dir "$data_dir" --chain genesis.json \
        --grpc-address ":$grpc_port" --libp2p ":$libp2p_port" --jsonrpc ":$jsonrpc_port" \
        --num-block-confirmations 2 $relayer_arg \
        --json-rpc-batch-request-limit 0 \
        --gossip-msg-size 4194304 \
        --log-level DEBUG 2>&1 | tee $log_file &
    else
      ./polygon-edge server --data-dir "$data_dir" --chain genesis.json \
        --grpc-address ":$grpc_port" --libp2p ":$libp2p_port" --jsonrpc ":$jsonrpc_port" \
        --num-block-confirmations 2 $relayer_arg \
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
  if [ "$CONSENSUS" != "polybft" ]; then
    export EDGE_CONSENSUS="$CONSENSUS"
  fi

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
  # Initialize ibft or polybft consensus
  if [ "$CONSENSUS" == "ibft" ]; then
    # Initialize ibft consensus
    initIbftConsensus
    # Create genesis file and start the server from binary
    createGenesis
    startNodes $CONSENSUS $1
    exit 0
  elif [ "$CONSENSUS" == "ibft-hsm" ]; then
    # Initialize ibft consensus
    initIbftConsensusForHSM
    # Create genesis file and start the server from binary
    createGenesisForHSM
    startNodesForHSM $CONSENSUS $1
    exit 0
  elif [ "$CONSENSUS" == "ibft-hsm-local" ]; then
    # Initialize ibft consensus
    initIbftConsensusForHSMLocal
    # Create genesis file and start the server from binary
    createGenesisForHSM
    startNodesForHSM $CONSENSUS $1
    exit 0
  elif [ "$CONSENSUS" == "polybft" ]; then
    # Initialize polybft consensus
    initPolybftConsensus
    # Create genesis file and start the server from binary
    createGenesis
    initRootchain $1
    startNodes $CONSENSUS $1
    exit 0
  else
    echo "Unsupported consensus mode. Supported modes are: ibft and polybft."
    showhelp
    exit 1
  fi
  ;;
esac