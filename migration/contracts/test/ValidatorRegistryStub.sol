// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

/// @title The interface pitau reads the validator set through
/// @notice pitau calls these two views, and nothing else (`docs/architecture/validator-registry.md`).
/// Both must be pure functions of the registry's own storage: no calls to other contracts, no
/// `block.*`, since pitau re-reads them only after a transaction that wrote the registry's storage.
/// Voting power is not the registry's to set: every validator has the same power, fixed by the
/// protocol.
interface IValidatorRegistry {
    /// `keyType` 1 is ed25519 (32-byte keys), 2 is ML-DSA-65 (1,952-byte keys).
    struct Validator {
        uint8 keyType;
        bytes pubKey;
    }

    /// Increments whenever `validators()` changes. pitau enforces it: a transaction that changes
    /// the set without raising `version()`, or that lowers it, is invalid.
    function version() external view returns (uint64);

    /// The validator set, in no particular order.
    function validators() external view returns (Validator[] memory);
}

/// @title A protocol stub of the validator registry
/// @notice Adds and removes validators with no access control: anyone can change the validator set
/// of a chain running this contract. It exists so the protocol can be built and tested; the
/// production registry replaces it and keeps `IValidatorRegistry` and the churn rule.
/// @dev It refuses what pitau would refuse, so a bad change reverts here rather than making the
/// transaction invalid: a key of the wrong length or of a type the chain does not allow, a
/// duplicate, an unknown key, more than `maxValidators` validators, and any change after which the
/// validators present at the start of the block are two thirds or less of the set before or after
/// it (the churn rule; every validator has the same power). pitau checks the same rule after every
/// transaction that writes this contract's storage.
contract ValidatorRegistry is IValidatorRegistry {
    uint8 public constant ED25519 = 1;
    uint8 public constant ML_DSA_65 = 2;

    /// The largest set this registry holds.
    uint256 public immutable maxValidators;
    /// Bit `t` set: key type `t` is allowed (the chain's `pub_key_types`).
    uint256 public immutable keyTypeMask;

    Validator[] private _validators;
    /// `keccak256(pubKey)` to its index in `_validators`, plus one; 0 for an unknown key.
    mapping(bytes32 => uint256) private _slot;
    uint64 private _version;

    /// Per block churn accounting: the block it is for, the number of validators at that block's
    /// start, and how many of those the block has removed so far.
    uint256 private _churnBlock;
    uint256 private _startCount;
    uint256 private _removedFromStart;
    /// For a key the current block changed: that block, and whether the key was a validator at its
    /// start. Recorded at the block's first change of the key.
    struct Start {
        uint64 block;
        bool present;
    }
    mapping(bytes32 => Start) private _start;

    event ValidatorAdded(bytes32 indexed keyHash, uint8 keyType, bytes pubKey);
    event ValidatorRemoved(bytes32 indexed keyHash);

    error BadKeyType(uint8 keyType);
    error BadKeyLength(uint8 keyType, uint256 length);
    error AlreadyRegistered(bytes32 keyHash);
    error NotRegistered(bytes32 keyHash);
    error TooManyValidators(uint256 max);
    /// After the change `stayed` of the `startCount` validators the block started with would remain
    /// in a set of `count`; both ratios must stay above two thirds.
    error ChurnLimit(uint256 stayed, uint256 startCount, uint256 count);
    error NoValidators();

    /// @param maxValidators_ The largest set (the genesis rule `maxValidators`).
    /// @param keyTypeMask_ Bit `t` allows key type `t` (the chain's `pub_key_types`).
    /// @param initial The genesis validators.
    constructor(uint256 maxValidators_, uint256 keyTypeMask_, Validator[] memory initial) {
        if (initial.length == 0) revert NoValidators();
        if (initial.length > maxValidators_) revert TooManyValidators(maxValidators_);
        maxValidators = maxValidators_;
        keyTypeMask = keyTypeMask_;
        for (uint256 i = 0; i < initial.length; i++) {
            Validator memory v = initial[i];
            _checkKey(v.keyType, v.pubKey);
            bytes32 keyHash = keccak256(v.pubKey);
            if (_slot[keyHash] != 0) revert AlreadyRegistered(keyHash);
            _validators.push(v);
            _slot[keyHash] = _validators.length;
        }
        // The genesis set is version 0; no churn applies to it.
    }

    function version() external view returns (uint64) {
        return _version;
    }

    function validators() external view returns (Validator[] memory) {
        return _validators;
    }

    /// The number of validators.
    function count() external view returns (uint256) {
        return _validators.length;
    }

    /// Whether `pubKey` is a validator.
    function isValidator(bytes calldata pubKey) external view returns (bool) {
        return _slot[keccak256(pubKey)] != 0;
    }

    /// Adds a validator.
    function add(uint8 keyType, bytes calldata pubKey) external {
        _checkKey(keyType, pubKey);
        bytes32 keyHash = keccak256(pubKey);
        if (_slot[keyHash] != 0) revert AlreadyRegistered(keyHash);
        if (_validators.length >= maxValidators) revert TooManyValidators(maxValidators);
        Start memory start = _touch(keyHash, false);
        // A validator of the block's start, removed and now back, stays after all.
        if (start.present) _removedFromStart -= 1;
        _validators.push(Validator({keyType: keyType, pubKey: pubKey}));
        _slot[keyHash] = _validators.length;
        _checkChurn();
        _version += 1;
        emit ValidatorAdded(keyHash, keyType, pubKey);
    }

    /// Removes a validator.
    function remove(bytes calldata pubKey) external {
        bytes32 keyHash = keccak256(pubKey);
        uint256 slot = _slot[keyHash];
        if (slot == 0) revert NotRegistered(keyHash);
        Start memory start = _touch(keyHash, true);
        if (start.present) _removedFromStart += 1;
        uint256 index = slot - 1;
        uint256 last = _validators.length - 1;
        if (index != last) {
            _validators[index] = _validators[last];
            _slot[keccak256(_validators[index].pubKey)] = slot;
        }
        _validators.pop();
        delete _slot[keyHash];
        _checkChurn();
        _version += 1;
        emit ValidatorRemoved(keyHash);
    }

    /// Starts the current block's churn accounting if needed, and returns `keyHash`'s record for this
    /// block, creating it with `presentNow` (whether the key is a validator before this change) if
    /// the block has not changed the key yet.
    function _touch(bytes32 keyHash, bool presentNow) private returns (Start memory start) {
        if (_churnBlock != block.number) {
            _churnBlock = block.number;
            _startCount = _validators.length;
            _removedFromStart = 0;
        }
        start = _start[keyHash];
        if (start.block != block.number) {
            start = Start({block: uint64(block.number), present: presentNow});
            _start[keyHash] = start;
        }
    }

    function _checkChurn() private view {
        uint256 stayed = _startCount - _removedFromStart;
        uint256 count_ = _validators.length;
        if (3 * stayed <= 2 * _startCount || 3 * stayed <= 2 * count_) {
            revert ChurnLimit(stayed, _startCount, count_);
        }
    }

    function _checkKey(uint8 keyType, bytes memory pubKey) private view {
        if (keyType != ED25519 && keyType != ML_DSA_65) revert BadKeyType(keyType);
        if ((keyTypeMask >> keyType) & 1 == 0) revert BadKeyType(keyType);
        uint256 expected = keyType == ED25519 ? 32 : 1952;
        if (pubKey.length != expected) revert BadKeyLength(keyType, pubKey.length);
    }
}
