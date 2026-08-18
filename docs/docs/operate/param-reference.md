
Configuration parameters are crucial for setting up and operating an Edge-powered chain. You can configure these parameters using the `secrets`, `genesis`, and `server` commands.

Before running genesis or server, generate validator keys with `secrets init`. Nodes use a single ECDSA validator key (`validator.key`) plus a networking key. BLS validator keys and PolyBFT operator secret generation are no longer supported.

For information on the available configuration flags and their descriptions, refer to the categorized tabs below.

<Tabs
defaultValue="secrets"
values={[
{ label: 'Secrets', value: 'secrets', },
{ label: 'Genesis', value: 'genesis', },
{ label: 'Server', value: 'server', },
]
}>

<!-- ===================================================================================================================== -->
<!-- ==================================================== SECRETS ======================================================== -->
<!-- ===================================================================================================================== -->

<TabItem value="secrets">

## Secrets Configuration Parameter Reference

Use `polygon-edge secrets init` to generate ECDSA validator and networking keys.

| Parameter | Description | Default Value | Mandatory |
| :-------- | :---------- | :------------ | :-------- |
| `--ecdsa` | Whether a new ECDSA validator key is created | TRUE | NO |
| `--network` | Whether a new Network key is created | TRUE | NO |
| `--config string` | Path to the SecretsManager config file | "" | NO |
| `--data-dir string` | Directory for local filesystem secrets storage | "" | YES* |
| `--insecure` | Allow storing secrets on the local filesystem (dev/test only) | FALSE | NO |
| `--num int` | Number of secrets to create (local FS only) | 1 | NO |
| `--json-tls-cert` | Whether a new self-signed TLS certificate is created for JSON-RPC | TRUE | NO |

:::info Mutually Exclusive Parameters

- `--config` and `--data-dir` are mutually exclusive.
- `--num` and `--config` are mutually exclusive.

:::

</TabItem>

<!-- ===================================================================================================================== -->
<!-- ==================================================== GENESIS ======================================================== -->
<!-- ===================================================================================================================== -->

<TabItem value="genesis">

## Genesis Configuration Parameter Reference

IBFT with ECDSA validators is the supported consensus path. `--consensus polybft` and `--ibft-validator-type` are no longer available.

| Parameter | Description | Default Value | Mandatory | Example | Reconfigurable at Runtime |
| :-------- | :---------- | :------------ | :-------- | :------ | :----------------------- |
| `--chain-id` | The ID of the chain. | 100 | NO | `genesis --chain-id "100"` | NO |
| `--max-validator-count` | The maximum number of validators in the validator set for PoS. | 9007199254740990 | NO | `genesis --max-validator-count "9007199254740990"` | NO |
| `--min-validator-count` | The minimum number of validators in the validator set for PoS. | 1 | NO | `genesis --min-validator-count "1"` | NO |
| `--pos` | Flag indicating use of Proof of Stake IBFT. | N/A | NO | `genesis --pos` | NO |
| `--block-gas-limit` | The maximum amount of gas used by all transactions in a block | 5242880 | NO | `genesis --block-gas-limit "10000000"` | NO |
| `--block-time` | The predefined period which determines block creation frequency | 2s | NO | `genesis --block-time "10s"` | NO |
| `--bootnode` | MultiAddr URL for p2p discovery bootstrap. This flag can be used multiple times. | N/A | YES (IBFT) | `genesis --bootnode "/ip4/127.0.0.1/tcp/30301/p2p/..."` | NO |
| `--burn-contract` | The burn contract block and address (format: `<block>:<address>`) | "" | NO | `genesis --burn-contract "0:0x0000000000000000000000000000000000000000"` | NO |
| `--consensus` | The consensus protocol to be used | `"ibft"` | NO | `genesis --consensus ibft` | NO |
| `--dir` | File path for the genesis data | `"./genesis.json"` | NO | `genesis --dir "/data/genesis.json"` | NO |
| `--epoch-size` | The epoch size for the chain | 100000 | NO | `genesis --epoch-size "10"` | NO |
| `--name` | The name for the chain | `"polygon-edge"` | NO | `genesis --name "test-chain"` | NO |
| `--premine` | The premined accounts and balances | []string{} | NO | `genesis --premine 0x85da99c8a7c2c95964c8efd687e95e632fc533d6:1000000000000000000000` | NO |
| `--trieroot` | Trie root from the corresponding triedb | "" | NO | `genesis --trieroot "0xabc123"` | NO |
| `--validators` | Validator addresses for the chain (format: `<ECDSA address>`) | []string{} | NO* | `genesis --validators "0x9c106ada8a2a36a9de8d67b347c07156033882e0"` | NO |
| `--validators-path` | Root path containing validators' secrets | `"./"` | NO* | `genesis --validators-path "/data/validators"` | NO |
| `--validators-prefix` | Folder prefix names for validators' secrets | `"test-chain-"` | NO* | `genesis --validators-prefix "test-chain-"` | NO |

:::info Validator sources

Provide validators either with `--validators` (ECDSA addresses) or by pointing `--validators-path` / `--validators-prefix` at directories created by `secrets init`.

:::

</TabItem>

<!-- ===================================================================================================================== -->
<!-- ==================================================== SERVER ========================================================= -->
<!-- ===================================================================================================================== -->

<TabItem value="server">

## Server Configuration Parameter Reference

Server startup has no BLS-specific flags. Validator keys are loaded from `--data-dir` or `--secrets-config` using the ECDSA validator key generated by `secrets init`.

| Parameter | Description |
| :-------- | :---------- |
| `--data-dir` | Node data directory containing secrets and chain state |
| `--secrets-config` | Optional remote secrets manager configuration |
| `--chain` | Path to genesis.json |

</TabItem>

</Tabs>
