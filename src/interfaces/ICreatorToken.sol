// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @notice ERC-7641 surface for the creator token.
/// Revenue token is USDG (an ERC-20), not ETH, so we use the AltRevToken pattern.
interface ICreatorToken {
    /// @notice How much USDG account can claim for a given snapshot.
    function claimableRevenue(address account, uint256 snapshotId) external view returns (uint256);

    /// @notice Claim USDG for a single snapshot. Reverts if already claimed.
    function claim(uint256 snapshotId) external;

    /// @notice Claim all unclaimed snapshots up to current in one call.
    function claimAll() external;

    /// @notice Called by the basket when it distributes creator's fee cut.
    /// Receives USDG and records a new snapshot. Only the associated basket may call.
    function snapshotRevenue(uint256 usdgAmount) external;

    function snapshotCount() external view returns (uint256);
    function basket() external view returns (address);
    function usdg() external view returns (address);

    // ERC-7641 burn/redeemable — implemented but burn is optional for our use case
    function redeemableOnBurn(uint256 amount) external view returns (uint256);
    function burn(uint256 amount) external;
}