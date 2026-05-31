// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IBasket {
    function deposit(uint256 usdgAmount, uint256 minBasketTokensOut, address receiver)
        external
        returns (uint256 basketTokensMinted);

    function redeem(uint256 basketTokenAmount, uint256 minUsdgOut, address receiver)
        external
        returns (uint256 usdgReturned);

    /// @notice Permissionless — anyone can call. Automation calls it when funded.
    function rebalance(uint256[] calldata minAmountsOut) external;

    function collectFees() external;

    function totalValueUsdg() external view returns (uint256);
    function navPerToken() external view returns (uint256);
    function currentWeightsBps() external view returns (uint256[] memory);
    function maxDrift() external view returns (uint256);
    function needsRebalancing() external view returns (bool);

    function constituents() external view returns (address[] memory);
    function targetWeightsBps() external view returns (uint256[] memory);
    function constituentBalances() external view returns (uint256[] memory);
    function rebalancingEnabled() external view returns (bool);
    function driftThresholdBps() external view returns (uint256);
    function suspended() external view returns (bool);
    function creatorToken() external view returns (address);
    function registry() external view returns (address);
    function thesis() external view returns (string memory);

    /// @notice Returns the full basket state in one call for frontend/backend use.
    function basketState()
        external
        view
        returns (
            address[] memory constituentsOut,
            uint256[] memory targetWeightsOut,
            uint256[] memory currentWeightsOut,
            uint256[] memory balancesOut,
            uint256          totalValueOut,
            uint256          navOut,
            bool             rebalancingEnabledOut,
            uint256          driftThresholdBpsOut,
            uint256          maxDriftOut
        );
}