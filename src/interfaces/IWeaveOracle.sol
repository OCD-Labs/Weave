// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @notice Price read interface compatible with Chainlink AggregatorV3Interface.
/// Returns the full round data shape so OracleAdapter (testnet) and a direct
/// Chainlink wrapper (mainnet) are drop-in interchangeable with zero backend
/// or contract changes.
/// All prices are 8-decimal USD matching the Chainlink convention.
interface IWeaveOracle {
    /// @notice Returns the full Chainlink-compatible round data.
    /// Matches AggregatorV3Interface.latestRoundData() exactly so a mainnet
    /// wrapper needs only to forward that call.
    /// @return roundId         The round ID of this price update.
    /// @return answer          The price in 8-decimal USD. Always positive for valid feeds.
    /// @return startedAt       Timestamp when the round started.
    /// @return updatedAt       Timestamp when the round was last updated. Used for staleness checks.
    /// @return answeredInRound The round in which the answer was computed.
    function latestRoundData()
        external
        view
        returns (
            uint80  roundId,
            int256  answer,
            uint256 startedAt,
            uint256 updatedAt,
            uint80  answeredInRound
        );

    /// @notice Number of decimals in the returned price. Always 8 for our feeds.
    function decimals() external view returns (uint8);

    /// @notice Human-readable description of the feed, e.g. "TSLA / USD".
    function description() external view returns (string memory);
}