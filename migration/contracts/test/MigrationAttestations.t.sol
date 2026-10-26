// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

import {MigrationAttestations, IValidatorSet} from "../src/MigrationAttestations.sol";
import {ValidatorRegistry, IValidatorRegistry} from "./ValidatorRegistryStub.sol";

/// Foundry's cheatcodes, the few these tests use (no forge-std in this repository).
interface Vm {
    function roll(uint256) external;
    function prank(address) external;
    function setBlockhash(uint256, bytes32) external;
    function expectRevert(bytes4) external;
    function expectRevert(bytes calldata) external;
}

/// T7c's contract part (pitau's docs/plans/ucl-migration.md §10): attestations count only from
/// operators of registered validators, for ucl's real hash of a recent block; a fresh quorum records
/// the earliest halt once; `shouldHalt` needs that block reached and the quorum still fresh.
contract MigrationAttestationsTest {
    Vm constant vm = Vm(address(uint160(uint256(keccak256("hevm cheat code")))));

    ValidatorRegistry registry;
    MigrationAttestations attestations;
    address[4] operators = [address(0xA1), address(0xA2), address(0xA3), address(0xA4)];
    bytes[4] keys;

    uint64 constant FRESHNESS = 50;
    uint64 constant MARGIN = 100;
    bytes32 constant STATE = bytes32(uint256(0x5));
    bytes32 constant TXS = bytes32(uint256(0x7));
    bytes32 constant RECEIPTS = bytes32(uint256(0x9));

    function setUp() public {
        IValidatorRegistry.Validator[] memory initial = new IValidatorRegistry.Validator[](4);
        for (uint256 i = 0; i < 4; i++) {
            keys[i] = abi.encodePacked(keccak256(abi.encode("validator", i)));
            initial[i] = IValidatorRegistry.Validator({keyType: 1, pubKey: keys[i]});
        }
        // ed25519 keys (32 bytes), up to 50 validators.
        registry = new ValidatorRegistry(50, 1 << 1, initial);
        attestations = new MigrationAttestations(IValidatorSet(address(registry)), FRESHNESS, MARGIN, address(this));
        for (uint256 i = 0; i < 4; i++) {
            attestations.setOperator(keccak256(keys[i]), operators[i]);
        }
        vm.roll(1_000);
        for (uint256 n = 744; n < 1_000; n++) {
            vm.setBlockhash(n, ucl(n));
        }
    }

    function ucl(uint256 n) internal pure returns (bytes32) {
        return keccak256(abi.encode("ucl block", n));
    }

    function attest(uint256 operator, uint64 n, bytes32 stateRoot) internal {
        vm.prank(operators[operator]);
        attestations.attest(n, ucl(n), stateRoot, TXS, RECEIPTS, 0);
    }

    function check(bool condition, string memory what) internal pure {
        require(condition, what);
    }

    function test_a_fresh_quorum_records_the_earliest_halt_once() public {
        attest(0, 990, STATE);
        attest(1, 990, STATE);
        check(attestations.earliestHalt() == 0, "two of four is no quorum");
        attest(2, 990, STATE);
        check(attestations.earliestHalt() == 1_000 + MARGIN, "three of four records it");

        vm.roll(1_050);
        vm.setBlockhash(1_040, ucl(1_040));
        attest(3, 1_040, STATE);
        check(attestations.earliestHalt() == 1_000 + MARGIN, "and it never moves");
    }

    function test_different_roots_do_not_add_up() public {
        attest(0, 990, STATE);
        attest(1, 990, STATE);
        attest(2, 990, bytes32(uint256(0x6)));
        check(attestations.earliestHalt() == 0, "a validator with other roots does not count");
        check(!attestations.freshAt(1_000), "no fresh quorum");
    }

    function test_stale_attestations_do_not_count() public {
        attest(0, 900, STATE);
        attest(1, 900, STATE);
        vm.roll(960);
        attest(2, 900, STATE);
        check(attestations.earliestHalt() == 0, "60 blocks old is past the freshness of 50");
    }

    function test_only_operators_of_registered_validators_attest_real_blocks() public {
        vm.prank(address(0xBAD));
        vm.expectRevert(abi.encodeWithSelector(MigrationAttestations.NotAnOperator.selector, address(0xBAD)));
        attestations.attest(990, ucl(990), STATE, TXS, RECEIPTS, 0);

        vm.prank(operators[0]);
        vm.expectRevert(
            abi.encodeWithSelector(MigrationAttestations.UnknownBlock.selector, uint64(990), keccak256("not ucl's"))
        );
        attestations.attest(990, keccak256("not ucl's"), STATE, TXS, RECEIPTS, 0);

        vm.prank(operators[0]);
        vm.expectRevert(abi.encodeWithSelector(MigrationAttestations.UnknownBlock.selector, uint64(1_000), ucl(1_000)));
        attestations.attest(1_000, ucl(1_000), STATE, TXS, RECEIPTS, 0);

        // A validator removed from the registry can no longer attest.
        registry.remove(keys[3]);
        vm.prank(operators[3]);
        vm.expectRevert(abi.encodeWithSelector(MigrationAttestations.NotAValidator.selector, keccak256(keys[3])));
        attestations.attest(990, ucl(990), STATE, TXS, RECEIPTS, 0);
    }

    function test_ucl_halts_after_the_first_block_past_the_margin_with_a_fresh_quorum() public {
        attest(0, 990, STATE);
        attest(1, 990, STATE);
        attest(2, 990, STATE);
        uint64 earliest = attestations.earliestHalt();
        check(!attestations.shouldHalt(earliest - 1), "not before the earliest halt");

        // The quorum attested block 990; at earliest - 1 = 1,099 it is 109 blocks old: stale.
        check(!attestations.shouldHalt(earliest), "not while the quorum is stale");

        // The followers attest again, at the head.
        vm.roll(earliest);
        for (uint256 n = earliest - 200; n < earliest; n++) {
            vm.setBlockhash(n, ucl(n));
        }
        attest(0, earliest - 1, STATE);
        attest(1, earliest - 1, STATE);
        attest(2, earliest - 1, STATE);
        check(attestations.shouldHalt(earliest), "fresh at earliest - 1: ucl halts after earliest");
        check(attestations.shouldHalt(earliest + 10), "and stays halted while fresh");
    }

    function test_the_margin_is_tunable_until_the_halt_is_recorded() public {
        attestations.setMargin(5);
        attest(0, 990, STATE);
        attest(1, 990, STATE);
        attest(2, 990, STATE);
        check(attestations.earliestHalt() == 1_005, "the tuned margin applies");
        vm.expectRevert(MigrationAttestations.HaltAlreadyRecorded.selector);
        attestations.setMargin(10);

        vm.prank(operators[0]);
        vm.expectRevert(MigrationAttestations.NotAdmin.selector);
        attestations.setOperator(keccak256(keys[0]), address(0xA9));
    }
}
