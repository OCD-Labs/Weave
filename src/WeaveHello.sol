// Temporary compile canary — delete once Section 1 is confirmed green.

// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

contract WeaveHello {
    string public constant CHAIN    = "Robinhood Chain Testnet";
    uint256 public constant CHAIN_ID = 46630;

    function hello() external pure returns (string memory) {
        return "Weave is alive";
    }
}