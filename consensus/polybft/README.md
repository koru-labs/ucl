
# Polybft consensus protocol

Polybft is a consensus protocol, which runs [go-ibft](https://github.com/Ethernal-Tech/go-ibft) consensus engine.

It has a native support for running bridge, which enables running cross-chain transactions with Ethereum-compatible blockchains.

> **Operator note:** PolyBFT node operator workflows that generate or use BLS validator keys
> (`polybft-secrets`, `polybft` CLI commands, and `--consensus polybft` genesis/server startup)
> are no longer exposed. The supported operator path is IBFT with ECDSA validator keys via
> `secrets init` and `genesis --consensus ibft`.
>
> The PolyBFT implementation, on-chain BLS verification, and related protocol code remain in the
> repository for now, but are not a supported node-operator configuration.

## Historical local testing environment

The steps below describe the previous PolyBFT local setup and are retained for reference only.
They require BLS validator keys and are not supported by the current CLI.

1. Build binary

    ```bash
    $ go build -o polygon-edge .
    ```

2. Init secrets (legacy; generated ECDSA + BLS + networking keys)

    ```bash
    $ polygon-edge polybft-secrets --data-dir test-chain- --num 4
    ```

3. Create chain configuration with `--consensus polybft` and validator BLS public keys.

4. Start rootchain / deploy stake manager / register validators / start servers as previously documented.
