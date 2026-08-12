In this section, we'll prepare a new chain with IBFT consensus (ECDSA validators) and prepare the initial Edge nodes.

## 1. Generate Keys

Initialize secrets for each node with `polygon-edge secrets init`. This command creates the ECDSA validator key and networking key used by the node.

<details>
<summary>Flags ↓</summary>

| Flag            | Description                                                                                               | Example                    |
|-----------------|-----------------------------------------------------------------------------------------------------------|----------------------------|
| `--ecdsa`       | Whether a new ECDSA validator key is created (default true).                                              |                            |
| `--network`     | Whether a new Network key is created (default true).                                                      |                            |
| `--config`      | The path to the SecretsManager config file. If omitted, the local FS secrets manager is used.              | `--config /path/to/config` |
| `--data-dir`    | The directory for the Polygon Edge data if the local FS is used.                                          | `--data-dir /path/to/dir`  |
| `--insecure`    | Allow storing secrets on the local filesystem (dev/test only).                                            |                            |
| `--num`         | How many secrets should be created, only for the local FS (default 1).                                    | `--num 4`                  |

</details>

```bash
./polygon-edge secrets init --insecure --data-dir test-chain- --num 4
```

<details>
<summary>Output example ↓</summary>

```bash
[WARNING: INSECURE LOCAL SECRETS - SHOULD NOT BE RUN IN PRODUCTION]

[SECRETS INIT]
Public key (address)|0x61324166B0202DB1E7502924326262274Fa4358F
Node ID|16Uiu2HAmMYyzK7c649Tnn6XdqFLP7fpPB2QWdck1Ee9vj5a7Nhg8
```

</details>

:::info Example with AWS Secrets

### Prerequisites

1. You need to have [AWS CLI](https://aws.amazon.com/cli/) installed and configured on your machine.
2. An [AWS SSO account](https://aws.amazon.com/iam/identity-center/) with the right permissions to access the SSM Parameter Store is required.

### Step 1: AWS SSO Login

```bash
aws sso login
```

### Step 2: Create Config.json File

```json
{
  "Type": "aws-ssm",
  "Name": "validator1",
  "Extra": {
    "region": "us-west-2",
    "ssm-parameter-path": "/test"
  }
}
```

### Step 3: Run the Secret Generation Command

```bash
go run main.go secrets init --config config.json
```

### Step 4: Check Outputs in AWS SSM Parameter Store

Verify that `network-key` and `validator-key` were generated. BLS keys are no longer created.

:::

### Understand the Generated Secrets

The generated secrets include:

- **ECDSA Private and Public Keys**: used to sign and verify transactions / IBFT seals.
- **P2P Networking Node ID**: unique identifier for each validator node.

> Retrieve output again with: `./polygon-edge secrets output --data-dir test-chain-1`

## 2. Next Steps

As the next step, navigate to the [<ins>Configure a New Childchain</ins>](genesis.md) deployment guide. Generate a genesis file with `--consensus ibft` and define the initial ECDSA validator set.
