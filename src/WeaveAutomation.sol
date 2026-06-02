// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {AutomationCompatibleInterface} from
    "@chainlink/contracts/src/v0.8/automation/AutomationCompatible.sol";
import {IWeaveRegistry} from "./interfaces/IWeaveRegistry.sol";
import {IWeaveOracle}   from "./interfaces/IWeaveOracle.sol";
import {IBasket}        from "./interfaces/IBasket.sol";
import {Math}           from "@openzeppelin/contracts/utils/math/Math.sol";

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

    /// @notice Maximum acceptable slippage in bps applied to each sell leg
    /// of an automation-triggered rebalance. Computed against the oracle price
    /// at time of performUpkeep execution.
    /// Default 50 bps = 0.5%. Governance can tighten or relax as needed.
    uint256 public maxRebalanceSlippageBps;

    address public immutable governance;

    /// @notice tokenAmount (18 dec) * price (8 dec) / PRICE_SCALE = usdg (6 dec).
    uint256 private constant PRICE_SCALE = 1e20;

    error NotGovernance();
    error InvalidBatchSize();
    error ZeroAddress();
    error InvalidSlippage();

    event BatchSizeUpdated(uint256 newSize);
    event RebalanceTriggered(address indexed basket);
    event MaxRebalanceSlippageUpdated(uint256 bps);

    constructor(address _registry, address _governance) {
        if (_registry   == address(0)) revert ZeroAddress();
        if (_governance == address(0)) revert ZeroAddress();
        registry               = _registry;
        governance             = _governance;
        batchSize              = 50;
        maxRebalanceSlippageBps = 50; // 0.5% default
    }

    /// @notice Runs off-chain every block as an eth_call — no gas cost.
    /// checkData encodes (uint256 startIndex) so multiple upkeep jobs can each
    /// cover a non-overlapping slice of the basket array.
    function checkUpkeep(bytes calldata checkData)
        external
        view
        override
        returns (bool upkeepNeeded, bytes memory performData)
    {
        uint256 startIndex = checkData.length >= 32
            ? abi.decode(checkData, (uint256))
            : 0;

        IWeaveRegistry reg              = IWeaveRegistry(registry);
        IWeaveRegistry.BasketMeta[] memory allBaskets = reg.getAllBaskets();
        uint256 total                   = allBaskets.length;

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

            if (!basket.rebalancingEnabled()) continue;
            if (basket.suspended())           continue;
            if (basket.totalValueUsdg() < minAUM) continue;
            if (!basket.needsRebalancing())   continue;

            toRebalance[count] = basketAddr;
            ++count;
        }

        upkeepNeeded = count > 0;

        address[] memory trimmed = new address[](count);
        for (uint256 i = 0; i < count; ++i) {
            trimmed[i] = toRebalance[i];
        }

        performData = abi.encode(trimmed);
    }

    /// @notice Runs on-chain only when checkUpkeep returned true.
    /// Computes per-constituent minAmountsOut from oracle prices minus
    /// maxRebalanceSlippageBps before calling rebalance() — prevents sandwich
    /// attacks on automation-triggered rebalances on mainnet.
    function performUpkeep(bytes calldata performData) external override {
        address[] memory baskets = abi.decode(performData, (address[]));
        uint256 len              = baskets.length;
        IWeaveRegistry reg       = IWeaveRegistry(registry);

        for (uint256 i = 0; i < len; ++i) {
            IBasket basket = IBasket(baskets[i]);

            // On-chain double-check — conditions may have changed since checkUpkeep ran.
            if (!basket.rebalancingEnabled()) continue;
            if (basket.suspended())           continue;
            if (!basket.needsRebalancing())   continue;

            address[] memory consts  = basket.constituents();
            uint256   numConsts      = consts.length;
            uint256[] memory minAmountsOut = new uint256[](numConsts);

            // Compute minimum acceptable USDG out for each sell leg.
            // For buy legs the basket uses its own slippage via _buyConstituents.
            // Here we only need to protect sell legs in _rebalanceSell —
            // pass minUsdgOut per constituent computed from oracle price minus slippage.
            uint256[] memory balances = basket.constituentBalances();
            uint256 totalValue        = basket.totalValueUsdg();
            uint256[] memory targets  = basket.targetWeightsBps();

            for (uint256 j = 0; j < numConsts; ++j) {
                (uint256 price, ) = reg.getAssetPrice(consts[j]);

                uint256 currentValue = Math.mulDiv(balances[j], price, PRICE_SCALE);
                uint256 targetValue  = Math.mulDiv(totalValue, targets[j], 10_000);

                if (currentValue > targetValue) {
                    // This is an overweight constituent — it will be sold.
                    // Compute expected USDG from the sell and apply slippage tolerance.
                    uint256 usdgToRaise    = currentValue - targetValue;
                    uint256 minUsdgOut     = Math.mulDiv(
                        usdgToRaise,
                        10_000 - maxRebalanceSlippageBps,
                        10_000
                    );
                    minAmountsOut[j] = minUsdgOut;
                }
                // Underweight constituents (buy legs) get 0 — protected by
                // BasketImplementation._buyConstituents using registry.maxSwapSlippageBps.
            }

            basket.rebalance(minAmountsOut);

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

    /// @notice Set maximum slippage tolerance for automation-triggered rebalance sell legs.
    /// Capped at 500 bps (5%) — anything higher defeats the purpose of slippage protection.
    function setMaxRebalanceSlippage(uint256 bps) external {
        if (msg.sender != governance) revert NotGovernance();
        if (bps > 500)                revert InvalidSlippage();
        maxRebalanceSlippageBps = bps;
        emit MaxRebalanceSlippageUpdated(bps);
    }
}