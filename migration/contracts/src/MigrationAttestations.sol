// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

/// @notice The view of pitau's validator registry this contract reads: the validator set.
interface IValidatorSet {
    struct Validator {
        uint8 keyType;
        bytes pubKey;
    }

    function validators() external view returns (Validator[] memory);
}

/// @title The attestations that let ucl hand over to pitau
/// @notice Every pitau validator that follows ucl attests, from its operator address, the block it
/// is at and the roots its own node computed for it (pitau's docs/plans/ucl-migration.md §7.4). When
/// more than two thirds of the registry's validators agree on a recent block, the contract records
/// the earliest block ucl may halt after, once. ucl halts after the first block h at or past it
/// whose parent's state still holds a fresh quorum (`shouldHalt`), so it never halts before the
/// validators have caught up.
/// @dev ucl's EVM verifies secp256k1 only, so each validator's consensus key (ed25519 or ML-DSA-65,
/// in the registry) has an operator address that signs for it; the map is this contract's own.
contract MigrationAttestations {
    struct Attestation {
        uint64 number;
        uint64 verifiedFrom;
        bytes32 digest;
    }

    /// The registry whose validators attest.
    IValidatorSet public immutable registry;
    /// How old an attestation may be and still count: a block `b` has a fresh quorum when more than
    /// two thirds of the validators attested the same block `N` with `b - N <= freshness`.
    uint64 public immutable freshness;

    address public admin;
    /// The blocks between the first fresh quorum and the earliest halt.
    uint64 public margin;
    /// The earliest block ucl may halt after; zero until the first fresh quorum.
    uint64 public earliestHalt;

    /// A validator's key hash (`keccak256(pubKey)`) to its operator, and back.
    mapping(bytes32 => address) public operatorOf;
    mapping(address => bytes32) public keyOf;
    /// Each validator's latest attestation.
    mapping(bytes32 => Attestation) public latest;

    event Attested(
        bytes32 indexed keyHash,
        uint64 number,
        bytes32 uclHash,
        bytes32 stateRoot,
        bytes32 transactionsRoot,
        bytes32 receiptsRoot,
        uint64 verifiedFrom
    );
    event EarliestHaltSet(uint64 earliestHalt);
    event OperatorSet(bytes32 indexed keyHash, address operator);
    event MarginSet(uint64 margin);
    event AdminSet(address admin);

    error NotAdmin();
    error NotAnOperator(address sender);
    error NotAValidator(bytes32 keyHash);
    error UnknownBlock(uint64 number, bytes32 uclHash);
    error OperatorTaken(address operator);
    error HaltAlreadyRecorded();

    modifier onlyAdmin() {
        if (msg.sender != admin) revert NotAdmin();
        _;
    }

    constructor(IValidatorSet registry_, uint64 freshness_, uint64 margin_, address admin_) {
        registry = registry_;
        freshness = freshness_;
        margin = margin_;
        admin = admin_;
        emit AdminSet(admin_);
        emit MarginSet(margin_);
    }

    /// Names the operator that attests for the validator with key hash `keyHash`; the zero address
    /// removes it.
    function setOperator(bytes32 keyHash, address operator) external onlyAdmin {
        if (operator != address(0) && keyOf[operator] != bytes32(0) && keyOf[operator] != keyHash) {
            revert OperatorTaken(operator);
        }
        address previous = operatorOf[keyHash];
        if (previous != address(0)) delete keyOf[previous];
        operatorOf[keyHash] = operator;
        if (operator != address(0)) keyOf[operator] = keyHash;
        emit OperatorSet(keyHash, operator);
    }

    /// Changes the margin, until the earliest halt is recorded: the team tunes it in rehearsals.
    function setMargin(uint64 margin_) external onlyAdmin {
        if (earliestHalt != 0) revert HaltAlreadyRecorded();
        margin = margin_;
        emit MarginSet(margin_);
    }

    function setAdmin(address admin_) external onlyAdmin {
        admin = admin_;
        emit AdminSet(admin_);
    }

    /// Attests block `number` of ucl: ucl's hash of it and the roots the validator's own node
    /// computed for it. `verifiedFrom` is the first block the node verified itself (0 after a
    /// replay from block 1). Refused unless `uclHash` is ucl's canonical hash of `number`, which
    /// ucl's EVM knows for its last 256 blocks only: an accepted attestation is a recent one.
    function attest(
        uint64 number,
        bytes32 uclHash,
        bytes32 stateRoot,
        bytes32 transactionsRoot,
        bytes32 receiptsRoot,
        uint64 verifiedFrom
    ) external {
        bytes32 key = keyOf[msg.sender];
        if (key == bytes32(0)) revert NotAnOperator(msg.sender);
        if (number >= block.number || blockhash(number) != uclHash || uclHash == bytes32(0)) {
            revert UnknownBlock(number, uclHash);
        }
        if (!_isValidator(key)) revert NotAValidator(key);

        latest[key] = Attestation({
            number: number,
            verifiedFrom: verifiedFrom,
            digest: keccak256(abi.encode(number, uclHash, stateRoot, transactionsRoot, receiptsRoot))
        });
        emit Attested(key, number, uclHash, stateRoot, transactionsRoot, receiptsRoot, verifiedFrom);

        if (earliestHalt == 0 && freshAt(block.number)) {
            earliestHalt = uint64(block.number) + margin;
            emit EarliestHaltSet(earliestHalt);
        }
    }

    /// Whether more than two thirds of the registry's validators have, among their latest
    /// attestations, the same block and roots, attested within `freshness` blocks before `b`.
    function freshAt(uint256 b) public view returns (bool) {
        IValidatorSet.Validator[] memory set = registry.validators();
        uint256 n = set.length;
        if (n == 0) return false;
        bytes32[] memory digests = new bytes32[](n);
        for (uint256 i = 0; i < n; i++) {
            Attestation memory a = latest[keccak256(set[i].pubKey)];
            if (a.digest != bytes32(0) && a.number <= b && b - a.number <= freshness) {
                digests[i] = a.digest;
            }
        }
        uint256 best = 0;
        for (uint256 i = 0; i < n; i++) {
            if (digests[i] == bytes32(0)) continue;
            uint256 same = 0;
            for (uint256 j = 0; j < n; j++) {
                if (digests[j] == digests[i]) same++;
            }
            if (same > best) best = same;
        }
        return 3 * best > 2 * n;
    }

    /// Whether ucl halts after block `h`. ucl and pitau evaluate it on the state of block `h - 1`
    /// (an `eth_call` at that block): the earliest halt must be recorded and reached, and the quorum
    /// fresh at `h - 1`. The first `h` for which it holds is the cutover block H.
    function shouldHalt(uint256 h) external view returns (bool) {
        return earliestHalt != 0 && h >= earliestHalt && h >= 1 && freshAt(h - 1);
    }

    function _isValidator(bytes32 key) private view returns (bool) {
        IValidatorSet.Validator[] memory set = registry.validators();
        for (uint256 i = 0; i < set.length; i++) {
            if (keccak256(set[i].pubKey) == key) return true;
        }
        return false;
    }
}
