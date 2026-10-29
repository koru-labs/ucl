// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {MigrationAttestations, IValidatorSet} from "../src/MigrationAttestations.sol";
import {ValidatorRegistry, IValidatorRegistry} from "./ValidatorRegistryStub.sol";

interface GasVm {
    function roll(uint256) external;
    function prank(address) external;
    function setBlockhash(uint256, bytes32) external;
}

/// What one attestation costs with `N` ML-DSA-65 validators, the production key type: `forge test
/// --match-contract Gas`. `setUp` attests once, which caches the registry's keys; `test_attest` is
/// every later attestation.
abstract contract AttestationGas {
    GasVm constant vm = GasVm(address(uint160(uint256(keccak256("hevm cheat code")))));
    MigrationAttestations attestations;

    function n() internal pure virtual returns (uint256);

    function setUp() public {
        IValidatorRegistry.Validator[] memory initial = new IValidatorRegistry.Validator[](n());
        for (uint256 i = 0; i < n(); i++) {
            bytes memory key = new bytes(1952);
            key[0] = bytes1(uint8(i));
            key[1] = bytes1(uint8(i >> 8));
            initial[i] = IValidatorRegistry.Validator({keyType: 2, pubKey: key});
        }
        ValidatorRegistry registry = new ValidatorRegistry(50, 1 << 2, initial);
        attestations = new MigrationAttestations(IValidatorSet(address(registry)), 50, 100, address(this));
        for (uint256 i = 0; i < n(); i++) {
            attestations.setOperator(keccak256(initial[i].pubKey), address(uint160(0xA000 + i)));
        }
        vm.roll(1_000);
        vm.setBlockhash(990, keccak256("ucl 990"));
        vm.prank(address(uint160(0xA001)));
        attestations.attest(990, keccak256("ucl 990"), bytes32(uint256(5)), bytes32(uint256(7)), bytes32(uint256(9)), 0);
    }

    function test_attest() public {
        vm.prank(address(uint160(0xA000)));
        attestations.attest(990, keccak256("ucl 990"), bytes32(uint256(5)), bytes32(uint256(7)), bytes32(uint256(9)), 0);
    }
}

contract Gas4 is AttestationGas {
    function n() internal pure override returns (uint256) {
        return 4;
    }
}

contract Gas50 is AttestationGas {
    function n() internal pure override returns (uint256) {
        return 50;
    }
}
