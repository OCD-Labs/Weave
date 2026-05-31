// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @notice Single price read interface — wraps either a real Chainlink AggregatorV3
/// feed or our MockOracle so the rest of the system never cares which it is.
/// All prices are 8-decimal USD, matching the Chainlink convention.
interface IWeaveOracle {
    /// @notice Returns the latest price and the timestamp it was last updated.
    /// The caller is responsible for staleness validation — this just fetches.
    function latestPrice() external view returns (int256 price, uint256 updatedAt);

    /// @notice Number of decimals in the returned price. Always 8 for our feeds.
    function decimals() external view returns (uint8);

    /// @notice Human-readable description of the feed, e.g. "TSLA / USD".
    function description() external view returns (string memory);
}