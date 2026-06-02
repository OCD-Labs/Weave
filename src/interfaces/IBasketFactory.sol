// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IBasketFactory {
    event BasketCreated(
        address indexed basket,
        address indexed creatorToken,
        address indexed creator,
        string  name,
        string  symbol,
        string  thesis,
        address[] constituents,
        uint256[] targetWeightsBps,
        bool    rebalancingEnabled
    );

    /// @notice Validates composition, deploys basket proxy + creator token atomically,
    /// registers both in the registry, and executes the initial deposit.
    function createBasket(
        string    calldata name,
        string    calldata symbol,
        string    calldata thesis,
        address[] calldata constituents,
        uint256[] calldata targetWeightsBps,
        bool      rebalancingEnabled,
        uint256   driftThresholdBps,
        uint256   initialDepositUsdg
    ) external returns (address basket, address creatorToken);
}