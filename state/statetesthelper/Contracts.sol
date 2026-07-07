// SPDX-License-Identifier: MIT
pragma solidity 0.8.24;

// Balances mirrors the existing parallel-verification test contract:
// slot 0 = totalBalance, slot 1 = balances mapping.
contract Balances {
    uint256 public totalBalance;               // slot 0
    mapping(address => uint256) public balances; // slot 1

    event BalanceChanged(address indexed account, uint256 newBalance);

    function incBalance(address a, uint256 v) public {
        balances[a] += v;
        emit BalanceChanged(a, balances[a]);
    }

    function decBalance(address a, uint256 v) public {
        require(balances[a] >= v);
        balances[a] -= v;
        emit BalanceChanged(a, balances[a]);
    }

    function updateTotalBalance(address a) public {
        totalBalance += balances[a];
    }

    function transfer(address from, address to, uint256 v) public {
        decBalance(from, v);
        incBalance(to, v);
    }
}

// Router forwards via CALL, so writes land in the Balances contract's storage.
// tx.To == Router but the dirtied account is Balances.
contract Router {
    Balances public bal; // slot 0

    constructor(address b) {
        bal = Balances(b);
    }

    function routerInc(address a, uint256 v) public {
        bal.incBalance(a, v);
    }

    function routerTransfer(address from, address to, uint256 v) public {
        bal.transfer(from, to, v);
    }
}

// Proxy forwards via DELEGATECALL, so writes land in the Proxy's OWN storage.
// Storage layout must match Balances for slots 0/1.
contract Proxy {
    uint256 public totalBalance;                 // slot 0 (matches Balances)
    mapping(address => uint256) public balances; // slot 1 (matches Balances)
    address public impl;                         // slot 2

    constructor(address i) {
        impl = i;
    }

    function pinc(address a, uint256 v) public {
        (bool ok, ) = impl.delegatecall(
            abi.encodeWithSignature("incBalance(address,uint256)", a, v)
        );
        require(ok);
    }
}

// A -> B -> C deep chain: one tx dirties three contracts' storage.
contract CLeaf {
    uint256 public v; // slot 0
    function bump(uint256 x) public { v += x; }
}

contract BMid {
    CLeaf public c; // slot 0
    uint256 public v; // slot 1
    constructor(address _c) { c = CLeaf(_c); }
    function bump(uint256 x) public { v += x; c.bump(x); }
}

contract ATop {
    BMid public b; // slot 0
    uint256 public v; // slot 1
    constructor(address _b) { b = BMid(_b); }
    function bump(uint256 x) public { v += x; b.bump(x); }
}

// Factory uses CREATE to deploy a fresh Balances; created address is
// CreateAddress(factory, factoryNonce). First create -> nonce 1.
contract Factory {
    address public last; // slot 0
    function make() public returns (address) {
        Balances b = new Balances();
        last = address(b);
        return address(b);
    }
}

// Killable: selfdestruct sending remaining balance to `to`.
contract Killable {
    uint256 public x; // slot 0
    function setX(uint256 v) public { x = v; }
    function boom(address payable to) public { selfdestruct(to); }
    receive() external payable {}
}

// Create2Factory deploys Killable via CREATE2, so the same salt always yields the same address.
// After the child selfdestructs, make2 with the same salt redeploys at that address
// (the metamorphic-contract pattern).
contract Create2Factory {
    address public last; // slot 0
    function make2(bytes32 salt) public returns (address) {
        Killable k = new Killable{salt: salt}();
        last = address(k);
        return address(k);
    }
}

// TransientCounter exercises EIP-1153 transient storage (TSTORE/TLOAD via assembly).
// Transient slot 0 must read as 0 at the start of every tx, so `persisted` (slot 0)
// accumulates what each call actually observed: any cross-tx transient leak or broken
// inner-call revert changes persistent storage and therefore the block's state root.
contract TransientCounter {
    uint256 public persisted; // slot 0

    event TransientObserved(uint256 previous, uint256 next);

    // bump the transient counter and fold the result into persistent storage.
    function tbump(uint256 x) public {
        uint256 next;
        assembly {
            next := add(tload(0), x)
            tstore(0, next)
        }
        persisted += next;
        emit TransientObserved(next - x, next);
    }

    // two bumps in one tx: transient state must survive across calls *within* a tx,
    // so this adds x + 2x to persisted while two separate tbump(x) txs add x + x.
    function tbumpTwice(uint256 x) public {
        tbump(x);
        tbump(x);
    }

    // helper for bumpAfterRevertedTStore: writes transient storage then reverts.
    function storeAndRevert(uint256 x) external {
        assembly { tstore(0, x) }
        revert();
    }

    // the reverted inner call's TSTORE must be rolled back, so tload reads 0;
    // +1 guarantees the tx always writes storage and stays in the conflict chain.
    function bumpAfterRevertedTStore(uint256 x) public {
        try this.storeAndRevert(x) {} catch {}
        uint256 cur;
        assembly { cur := tload(0) }
        persisted += cur + 1;
    }
}
