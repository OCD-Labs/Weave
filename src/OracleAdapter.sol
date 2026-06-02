// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IWeaveOracle} from "./interfaces/IWeaveOracle.sol";

/// @notice Testnet price oracle implementing the full Chainlink AggregatorV3Interface
/// return shape. Governance sets prices manually to simulate market movement.
/// On mainnet, replace with a thin wrapper around the real Chainlink aggregator
/// that forwards latestRoundData() — the interface is identical.
contract OracleAdapter is IWeaveOracle {
    address public immutable owner;

    int256   private _price;
    uint256  private _updatedAt;
    uint80   private _roundId;
    uint8    private immutable _decimals;
    string   private _description;

    error OnlyOwner();
    error InvalidPrice();

    event PriceUpdated(int256 price, uint256 updatedAt, uint80 roundId);

    constructor(string memory desc, int256 initialPrice) {
        if (initialPrice <= 0) revert InvalidPrice();
        owner        = msg.sender;
        _decimals    = 8;
        _description = desc;
        _price       = initialPrice;
        _updatedAt   = block.timestamp;
        _roundId     = 1;
    }

    /// @notice Update price to simulate market movement on testnet.
    /// On mainnet this function does not exist — Chainlink updates automatically.
    function setPrice(int256 newPrice) external {
        if (msg.sender != owner) revert OnlyOwner();
        if (newPrice <= 0)       revert InvalidPrice();
        unchecked { ++_roundId; }
        _price     = newPrice;
        _updatedAt = block.timestamp;
        emit PriceUpdated(newPrice, block.timestamp, _roundId);
    }

    /// @notice Returns full Chainlink AggregatorV3Interface-compatible round data.
    /// roundId and answeredInRound increment on every setPrice call.
    /// startedAt equals updatedAt since we have no concept of round start on testnet.
    function latestPrice()
        external
        view
        override
        returns (
            uint80  roundId,
            int256  answer,
            uint256 startedAt,
            uint256 updatedAt,
            uint80  answeredInRound
        )
    {
        return (_roundId, _price, _updatedAt, _updatedAt, _roundId);
    }

    function decimals() external view override returns (uint8) {
        return _decimals;
    }

    function description() external view override returns (string memory) {
        return _description;
    }
}