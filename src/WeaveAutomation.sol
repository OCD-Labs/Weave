// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {AutomationCompatibleInterface} from
    "@chainlink/contracts/src/v0.8/automation/AutomationCompatible.sol";
import {IWeaveRegistry}  from "./interfaces/IWeaveRegistry.sol";
import {IBasket}         from "./interfaces/IBasket.sol";

/// @notice Chainlink Automation upkeep contract for Weave.
/// checkUpkeep runs off-chain on keeper nodes every block — zero gas cost to protocol.
/// performUpkeep runs on-chain only when checkUpkeep returns true.
/// Registered once as a custom logic upkeep on Chainlink Automation.
/// Multiple upkeep jobs can be registered with different checkData index ranges
/// to paginate across a large basket count as the protocol grows.
contract WeaveAutomation is AutomationCompatibleInterface {
    address public immutable registry;

    /// @notice Max baskets inspected per checkUpkeep call.
    /// Keeps the off-chain simulation within Chainlink's checkGasLimit.
    uint256 public batchSize;

    address public governance;

    error NotGovernance();
    error InvalidBatchSize();
    error ZeroAddress();

    event BatchSizeUpdated(uint256 newSize);
    event RebalanceTriggered(address indexed basket);

    constructor(address _registry, address _governance) {
        if (_registry   == address(0)) revert ZeroAddress();
        if (_governance == address(0)) revert ZeroAddress();
        registry   = _registry;
        governance = _governance;
        batchSize  = 50;    // sensible default; tune based on observed checkGasLimit usage
    }

    /// @notice Runs off-chain every block as an eth_call — no gas cost.
    /// checkData encodes (uint256 startIndex) so multiple upkeep jobs can each
    /// cover a non-overlapping slice of the basket array.
    /// Returns the list of baskets that need rebalancing as performData.
    function checkUpkeep(bytes calldata checkData)
        external
        view
        override
        returns (bool upkeepNeeded, bytes memory performData)
    {
        // Decode the starting index for this upkeep job's slice.
        // Default to 0 if checkData is empty (single-job setup).
        uint256 startIndex = checkData.length >= 32
            ? abi.decode(checkData, (uint256))
            : 0;

        IWeaveRegistry reg              = IWeaveRegistry(registry);
        IWeaveRegistry.BasketMeta[] memory allBaskets = reg.getAllBaskets();
        uint256 total                   = allBaskets.length;

        // Scratch array — sized to worst case, trimmed before encoding.
        address[] memory toRebalance    = new address[](batchSize);
        uint256 count                   = 0;
        uint256 minAUM                  = reg.minAUMForAutomation();

        for (
            uint256 i = startIndex;
            i < total && count < batchSize;
            ++i
        ) {
            address basketAddr = allBaskets[i].basket;
            IBasket basket     = IBasket(basketAddr);

            // Skip baskets that don't qualify for protocol-funded automation.
            if (!basket.rebalancingEnabled()) continue;
            if (basket.suspended())           continue;
            if (basket.totalValueUsdg() < minAUM) continue;
            if (!basket.needsRebalancing())   continue;

            toRebalance[count] = basketAddr;
            ++count;
        }

        upkeepNeeded = count > 0;

        // Trim the scratch array to the actual count before encoding.
        // Encoding a full-sized array with trailing zero addresses wastes calldata gas.
        address[] memory trimmed = new address[](count);
        for (uint256 i = 0; i < count; ++i) {
            trimmed[i] = toRebalance[i];
        }

        performData = abi.encode(trimmed);
    }

    /// @notice Runs on-chain only when checkUpkeep returned true.
    /// Re-validates every basket before calling rebalance() — stale performData
    /// from a delayed keeper execution could otherwise trigger unnecessary trades.
    function performUpkeep(bytes calldata performData) external override {
        address[] memory baskets = abi.decode(performData, (address[]));
        uint256 len = baskets.length;

        for (uint256 i = 0; i < len; ++i) {
            IBasket basket = IBasket(baskets[i]);

            // On-chain double-check — conditions may have changed since checkUpkeep ran.
            if (!basket.rebalancingEnabled()) continue;
            if (basket.suspended())           continue;
            if (!basket.needsRebalancing())   continue;

            // Pass empty minAmountsOut — protocol-initiated rebalances accept oracle pricing.
            // The MockSwapRouter on testnet executes at exact oracle prices anyway.
            // On mainnet, slippage protection comes from the DEX router's own mechanics.
            uint256[] memory minAmounts = new uint256[](basket.constituents().length);

            // External call is last — checks-effects pattern holds because rebalance()
            // itself is nonReentrant and we have no state to update here.
            basket.rebalance(minAmounts);

            emit RebalanceTriggered(baskets[i]);
        }
    }

    /// @notice Tune batchSize if checkUpkeep starts hitting Chainlink's gas limit.
    function setBatchSize(uint256 newSize) external {
        if (msg.sender != governance) revert NotGovernance();
        if (newSize == 0)             revert InvalidBatchSize();
        batchSize = newSize;
        emit BatchSizeUpdated(newSize);
    }
}