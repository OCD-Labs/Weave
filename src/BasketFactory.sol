// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IBasketFactory}       from "./interfaces/IBasketFactory.sol";
import {IWeaveRegistry}       from "./interfaces/IWeaveRegistry.sol";
import {BasketImplementation} from "./BasketImplementation.sol";
import {CreatorToken}         from "./CreatorToken.sol";
import {IERC20}               from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20}            from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Clones}               from "@openzeppelin/contracts/proxy/Clones.sol";

/// @notice Deploys basket proxies and creator tokens atomically.
/// The only contract permitted to call registry.registerBasket().
/// All composition validation happens here so BasketImplementation.initialize()
/// can trust its inputs without redundant checks.
contract BasketFactory is IBasketFactory {
    using SafeERC20 for IERC20;
    using Clones    for address;

    address public immutable registry;
    address public immutable implementation;

    error ZeroAddress();
    error TooManyConstituents(uint256 got, uint256 max);
    error TooFewConstituents(uint256 got);
    error ArrayLengthMismatch();
    error WeightSumInvalid(uint256 got);
    error WeightTooLow(address token, uint256 weightBps);
    error WeightTooHigh(address token, uint256 weightBps);
    error AssetNotActive(address token);
    error InitialDepositTooLow(uint256 got, uint256 min);
    error InvalidDriftThreshold();
    error DuplicateConstituent(address token);

    constructor(address _registry, address _implementation) {
        if (_registry       == address(0)) revert ZeroAddress();
        if (_implementation == address(0)) revert ZeroAddress();
        registry       = _registry;
        implementation = _implementation;
    }

    function createBasket(
        string    calldata name,
        string    calldata symbol,
        string    calldata thesis,
        address[] calldata constituents,
        uint256[] calldata targetWeightsBps,
        bool      rebalancingEnabled,
        uint256   driftThresholdBps,
        uint256   initialDepositUsdg
    ) external override returns (address basket, address creatorToken) {
        IWeaveRegistry reg = IWeaveRegistry(registry);

        uint256 len = constituents.length;

        if (len < 3) revert TooFewConstituents(len);
        if (len > reg.maxConstituents()) revert TooManyConstituents(len, reg.maxConstituents());
        if (len != targetWeightsBps.length) revert ArrayLengthMismatch();

        uint256 minWeight = reg.minWeightBps();
        uint256 weightSum = 0;

        for (uint256 i = 0; i < len; ++i) {
            // Duplicate check: scan previous entries. O(n²) but n ≤ 20 so gas is fine.
            for (uint256 j = 0; j < i; ++j) {
                if (constituents[i] == constituents[j]) revert DuplicateConstituent(constituents[i]);
            }

            if (targetWeightsBps[i] < minWeight) {
                revert WeightTooLow(constituents[i], targetWeightsBps[i]);
            }
            if (targetWeightsBps[i] > 5_000) {
                revert WeightTooHigh(constituents[i], targetWeightsBps[i]);
            }

            // Verify the asset is registered and active in the catalogue.
            IWeaveRegistry.AssetConfig memory cfg = reg.assets(constituents[i]);
            if (!cfg.active) revert AssetNotActive(constituents[i]);

            weightSum += targetWeightsBps[i];
        }

        if (weightSum != 10_000) revert WeightSumInvalid(weightSum);

        if (initialDepositUsdg < reg.minFirstDepositUsdg()) {
            revert InitialDepositTooLow(initialDepositUsdg, reg.minFirstDepositUsdg());
        }

        if (rebalancingEnabled) {
            // Zero threshold means every tick triggers rebalancing — operationally impossible.
            // Above 5000 bps (50%) is nonsensical, a basket is already destroyed at that drift.
            if (driftThresholdBps == 0 || driftThresholdBps > 5_000) {
                revert InvalidDriftThreshold();
            }
        }

        // Deploy the basket proxy — a minimal ERC-1167 clone pointing to implementation.
        basket = implementation.clone();

        // Deploy the creator token for this basket.
        // Name and symbol are derived from the basket for easy identification.
        string memory ctName   = string(abi.encodePacked("Weave Creator: ", name));
        string memory ctSymbol = string(abi.encodePacked("wCT-", symbol));

        creatorToken = address(new CreatorToken(
            basket,
            reg.usdg(),
            msg.sender,
            ctName,
            ctSymbol
        ));

        BasketImplementation(basket).initialize(
            registry,
            creatorToken,
            name,
            symbol,
            thesis,
            constituents,
            targetWeightsBps,
            rebalancingEnabled,
            driftThresholdBps,
            msg.sender
        );

        // Register both contracts in the protocol registry.
        reg.registerBasket(basket, creatorToken, msg.sender);

        // Pull initial deposit from creator and route it through the basket's deposit function.
        // The creator receives the first basket tokens at the 1 USDG = 1 token initial price.
        IERC20(reg.usdg()).safeTransferFrom(msg.sender, address(this), initialDepositUsdg);
        IERC20(reg.usdg()).forceApprove(basket, initialDepositUsdg);

        BasketImplementation(basket).deposit(
            initialDepositUsdg,
            0,           // min tokens out: creator accepts whatever they get at initialization
            msg.sender
        );

        emit BasketCreated(
            basket,
            creatorToken,
            msg.sender,
            name,
            symbol,
            thesis,
            constituents,
            targetWeightsBps,
            rebalancingEnabled
        );
    }
}