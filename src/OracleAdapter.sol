// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IWeaveOracle} from "./interfaces/IWeaveOracle.sol";

/// @notice Stands in for a real Chainlink feed on testnet.
/// Governance (the deployer) sets prices manually so the rest of the system
/// exercises every code path without needing live oracle infrastructure.
contract MockOracle is IWeaveOracle {
    address public immutable owner;

    int256  private _price;
    uint256 private _updatedAt;
    uint8   private immutable _decimals;
    string  private _description;

    error OnlyOwner();
    error InvalidPrice();

    event PriceUpdated(int256 price, uint256 updatedAt);

    constructor(string memory desc, int256 initialPrice) {
        owner        = msg.sender;
        _decimals    = 8;          // matches Chainlink convention
        _description = desc;
        _price       = initialPrice;
        _updatedAt   = block.timestamp;
    }

    /// @notice Call this to simulate a price move during testing or demos.
    function setPrice(int256 newPrice) external {
        if (msg.sender != owner) revert OnlyOwner();
        if (newPrice <= 0) revert InvalidPrice();
        _price     = newPrice;
        _updatedAt = block.timestamp;
        emit PriceUpdated(newPrice, block.timestamp);
    }

    function latestPrice() external view override returns (int256 price, uint256 updatedAt) {
        return (_price, _updatedAt);
    }

    function decimals() external view override returns (uint8) {
        return _decimals;
    }

    function description() external view override returns (string memory) {
        return _description;
    }
}