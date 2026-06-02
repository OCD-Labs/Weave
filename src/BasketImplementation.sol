// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IBasket}         from "./interfaces/IBasket.sol";
import {IWeaveRegistry}  from "./interfaces/IWeaveRegistry.sol";
import {ICreatorToken}   from "./interfaces/ICreatorToken.sol";
import {IWeaveRouter}    from "./interfaces/IWeaveRouter.sol";
import {ERC20}           from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {IERC20}          from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20}       from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Math}            from "@openzeppelin/contracts/utils/math/Math.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";

/// @notice Shared logic contract for all basket proxies.
/// Each BasketProxy (ERC-1167 clone) delegates here.
/// This contract IS the ERC-20 basket token — no separate token contract per basket.
///
/// STORAGE LAYOUT — never reorder. Proxy compatibility depends on slot stability.
/// Slot 0:  bool _initialized
/// Slot 1:  bool _locked         (ReentrancyGuard)
/// ERC-20 base occupies slots 2-6 (OZ ERC20 internal storage)
/// Slot 7:  address _registry
/// Slot 8:  address _creatorToken
/// Slot 9:  string  _thesis
/// Slot 10: address[] _constituents
/// Slot 11: uint256[] _targetWeightsBps
/// Slot 12: uint256[] _constituentBalances
/// Slot 13: bool _rebalancingEnabled
/// Slot 14: uint256 _driftThresholdBps
/// Slot 15: address _creator
/// Slot 16: bool _suspended
contract BasketImplementation is IBasket, ERC20, ReentrancyGuard {
    using SafeERC20 for IERC20;

    bool private _initialized;

    address public override registry;
    address public override creatorToken;
    string  private _thesis;

    address[] private _constituents;
    uint256[] private _targetWeightsBps;
    uint256[] private _constituentBalances;

    bool    public override rebalancingEnabled;
    uint256 public override driftThresholdBps;
    address public creator;
    bool    public override suspended;

    /// @notice Converts 6-decimal USDG to 18-decimal basket tokens on first deposit.
    /// usdg (6 dec) * 1e12 = basket token (18 dec). Establishes 1 USDG = 1 basket token initial price.
    uint256 private constant USDG_TO_TOKEN_SCALE = 1e12;

    /// @notice Used in NAV: tokenAmount (18 dec) * price (8 dec) / PRICE_SCALE = usdg (6 dec).
    /// 18 + 8 - 20 = 6. Floors toward zero at every division site.
    uint256 private constant PRICE_SCALE = 1e20;

    error AlreadyInitialized();
    error NotInitialized();
    error BasketSuspended();
    error ProtocolPaused();
    error RebalancingNotEnabled();
    error DriftThresholdNotMet();
    error InvalidComposition();
    error WeightSumInvalid();
    error MinDepositNotMet();
    error InsufficientSlippage();
    error ZeroAmount();
    error ConstituentNotActive(address token);
    error ZeroAddress();

    event Deposited(
        address indexed investor,
        uint256 usdgAmount,
        uint256 basketTokensMinted,
        uint256 feeUsdg
    );
    event Redeemed(
        address indexed investor,
        uint256 basketTokensBurned,
        uint256 usdgReturned,
        uint256 feeUsdg
    );
    event Rebalanced(address indexed triggeredBy);
    event Suspended();

    /// @notice The implementation contract itself is never used directly —
    /// all interaction goes through proxies. The ERC20 name/symbol here are
    /// placeholders; each proxy gets its own via initialize().
    constructor() ERC20("Weave Basket Implementation", "WBASKET-IMPL") {
        _initialized = true;
    }

    /// @notice Called exactly once by BasketFactory immediately after clone deployment.
    function initialize(
        address _registry,
        address _creatorToken,
        string calldata name_,
        string calldata symbol_,
        string calldata thesis_,
        address[] calldata constituents_,
        uint256[] calldata targetWeightsBps_,
        bool _rebalancingEnabled,
        uint256 _driftThresholdBps,
        address _creator
    ) external {
        if (_initialized) revert AlreadyInitialized();
        if (_registry    == address(0)) revert ZeroAddress();
        if (_creatorToken == address(0)) revert ZeroAddress();
        if (_creator     == address(0)) revert ZeroAddress();
        _initialized = true;

        registry       = _registry;
        creatorToken   = _creatorToken;
        _thesis        = thesis_;
        rebalancingEnabled = _rebalancingEnabled;
        driftThresholdBps  = _driftThresholdBps;
        creator        = _creator;

        for (uint256 i = 0; i < constituents_.length; ++i) {
            _constituents.push(constituents_[i]);
            _targetWeightsBps.push(targetWeightsBps_[i]);
            _constituentBalances.push(0);
        }

        _setNameAndSymbol(name_, symbol_);
    }

    string private _basketName;
    string private _basketSymbol;

    function _setNameAndSymbol(
        string calldata name_,
        string calldata symbol_
    ) internal {
        _basketName   = name_;
        _basketSymbol = symbol_;
    }

    function name() public view override returns (string memory) {
        bytes memory n = bytes(_basketName);
        return n.length > 0 ? _basketName : super.name();
    }

    function symbol() public view override returns (string memory) {
        bytes memory s = bytes(_basketSymbol);
        return s.length > 0 ? _basketSymbol : super.symbol();
    }

    // ── Core functions ────────────────────────────────────────────────────────

    /// @notice Deposit USDG into the basket.
    /// Fee is deducted first, then net USDG buys constituents in target proportions.
    /// Basket tokens are minted proportional to the depositor's contribution to total NAV.
    /// Reverts if the protocol is paused or the basket is suspended.
    function deposit(
        uint256 usdgAmount,
        uint256 minBasketTokensOut,
        address receiver
    ) external override nonReentrant returns (uint256 basketTokensMinted) {
        if (usdgAmount == 0) revert ZeroAmount();
        if (suspended)       revert BasketSuspended();

        IWeaveRegistry reg = IWeaveRegistry(registry);

        // Protocol-level pause check — governance can freeze all deposits instantly.
        if (reg.paused()) revert ProtocolPaused();

        _checkConstituentsActive();

        IERC20(reg.usdg()).safeTransferFrom(msg.sender, address(this), usdgAmount);

        uint256 feeUsdg = _collectFee(usdgAmount, reg);
        uint256 netUsdg = usdgAmount - feeUsdg;

        // Snapshot BEFORE buying so the depositor's own purchase does not
        // inflate the denominator and dilute their share.
        uint256 supplyBefore     = totalSupply();
        uint256 totalValueBefore = supplyBefore == 0 ? 0 : _totalValueUsdg(reg);

        _buyConstituents(netUsdg, reg);

        if (supplyBefore == 0) {
            basketTokensMinted = netUsdg * USDG_TO_TOKEN_SCALE;
        } else {
            basketTokensMinted = Math.mulDiv(netUsdg, supplyBefore, totalValueBefore);
        }

        // Check slippage BEFORE minting — revert costs less gas than reverting after state change.
        if (basketTokensMinted < minBasketTokensOut) revert InsufficientSlippage();

        _mint(receiver, basketTokensMinted);

        emit Deposited(receiver, usdgAmount, basketTokensMinted, feeUsdg);
    }

    /// @notice Redeem basket tokens for USDG.
    /// Burns tokens, sells proportional underlying holdings, deducts fee, sends net USDG.
    /// Reverts if the protocol is paused or the basket is suspended.
    function redeem(
        uint256 basketTokenAmount,
        uint256 minUsdgOut,
        address receiver
    ) external override nonReentrant returns (uint256 usdgReturned) {
        if (basketTokenAmount == 0) revert ZeroAmount();
        if (suspended)             revert BasketSuspended();

        IWeaveRegistry reg = IWeaveRegistry(registry);

        // Protocol-level pause check.
        if (reg.paused()) revert ProtocolPaused();

        uint256 supply = totalSupply();
        uint256 nav    = _totalValueUsdg(reg);

        uint256 grossUsdg = Math.mulDiv(basketTokenAmount, nav, supply);

        // Burn first — reduces supply before any external calls (CEI pattern).
        _burn(msg.sender, basketTokenAmount);

        uint256 usdgFromSales = _sellConstituents(basketTokenAmount, supply, reg);

        uint256 feeUsdg = Math.mulDiv(grossUsdg, reg.managementFeeBps(), 10_000);
        uint256 netUsdg = usdgFromSales > feeUsdg ? usdgFromSales - feeUsdg : 0;

        if (feeUsdg > 0) _distributeFee(feeUsdg, reg);

        if (netUsdg < minUsdgOut) revert InsufficientSlippage();

        IERC20(reg.usdg()).safeTransfer(receiver, netUsdg);
        usdgReturned = netUsdg;

        emit Redeemed(msg.sender, basketTokenAmount, usdgReturned, feeUsdg);
    }

    /// @notice Rebalance constituents back to target weights.
    /// Permissionless — anyone can call. Automation calls it when protocol-funded.
    /// Rebalancing is NOT blocked by protocol pause — reducing drift during a pause is safe.
    function rebalance(
        uint256[] calldata minAmountsOut
    ) external override nonReentrant {
        if (!rebalancingEnabled) revert RebalancingNotEnabled();
        if (suspended)           revert BasketSuspended();

        IWeaveRegistry reg  = IWeaveRegistry(registry);
        uint256 totalValue  = _totalValueUsdg(reg);
        uint256 len         = _constituents.length;
        uint256 minTradeSize = reg.minRebalanceTradeSizeUsdg();

        int256[] memory deltas = new int256[](len);
        bool needsIt = false;

        for (uint256 i = 0; i < len; ++i) {
            (uint256 price, ) = reg.getAssetPrice(_constituents[i]);
            uint256 currentValue = Math.mulDiv(_constituentBalances[i], price, PRICE_SCALE);
            uint256 targetValue  = Math.mulDiv(totalValue, _targetWeightsBps[i], 10_000);

            if (currentValue > targetValue) {
                uint256 delta = currentValue - targetValue;
                // Skip dust trades below the minimum rebalance size.
                if (delta >= minTradeSize) {
                    // forge-lint: disable-next-line(unsafe-typecast)
                    deltas[i] = int256(delta);
                    uint256 currentWeightBps = Math.mulDiv(currentValue, 10_000, totalValue);
                    uint256 drift = currentWeightBps > _targetWeightsBps[i]
                        ? currentWeightBps - _targetWeightsBps[i]
                        : _targetWeightsBps[i] - currentWeightBps;
                    if (drift >= driftThresholdBps) needsIt = true;
                }
            } else {
                uint256 delta = targetValue - currentValue;
                if (delta >= minTradeSize) {
                    // forge-lint: disable-next-line(unsafe-typecast)
                    deltas[i] = -int256(delta);
                }
            }
        }

        if (!needsIt) revert DriftThresholdNotMet();

        _rebalanceSell(deltas, minAmountsOut, reg);
        _rebalanceBuy(deltas, reg);

        emit Rebalanced(msg.sender);
    }

    /// @notice Phase 1 of rebalance: sell all overweight positions to accumulate USDG.
    function _rebalanceSell(
        int256[] memory deltas,
        uint256[] calldata minAmountsOut,
        IWeaveRegistry reg
    ) internal {
        address router = reg.swapRouter();
        uint256 len    = _constituents.length;

        for (uint256 i = 0; i < len; ++i) {
            if (deltas[i] <= 0) continue;

            (uint256 price, ) = reg.getAssetPrice(_constituents[i]);
            uint256 usdgToRaise = uint256(deltas[i]);
            uint256 tokenToSell = Math.mulDiv(usdgToRaise, PRICE_SCALE, price);
            if (tokenToSell == 0) continue;
            if (tokenToSell > _constituentBalances[i])
                tokenToSell = _constituentBalances[i];

            uint256 minOut = minAmountsOut.length > i ? minAmountsOut[i] : 0;

            IERC20(_constituents[i]).forceApprove(router, tokenToSell);
            uint256 usdgFromSell = IWeaveRouter(router).swapExactTokenForUSDG(
                _constituents[i],
                tokenToSell,
                minOut,
                address(this)
            );
            if (usdgFromSell == 0) continue;

            _constituentBalances[i] -= tokenToSell;
        }
    }

    /// @notice Phase 2 of rebalance: buy all underweight positions with accumulated USDG.
    function _rebalanceBuy(
        int256[] memory deltas,
        IWeaveRegistry reg
    ) internal {
        address router   = reg.swapRouter();
        address usdg_    = reg.usdg();
        uint256 len      = _constituents.length;
        uint256 available = IERC20(usdg_).balanceOf(address(this));

        for (uint256 i = 0; i < len; ++i) {
            if (deltas[i] >= 0) continue;

            uint256 usdgNeeded = uint256(-deltas[i]);
            if (usdgNeeded > available) usdgNeeded = available;
            if (usdgNeeded == 0) continue;

            IERC20(usdg_).forceApprove(router, usdgNeeded);
            uint256 tokensReceived = IWeaveRouter(router).swapExactUSDGForToken(
                _constituents[i],
                usdgNeeded,
                0,
                address(this)
            );

            _constituentBalances[i] += tokensReceived;
            available -= usdgNeeded;
        }
    }

    /// @notice External collectFees distributes whatever free USDG is sitting
    /// in the basket — handles dust accumulation from rebalancing rounding.
    function collectFees() external override nonReentrant {
        IWeaveRegistry reg    = IWeaveRegistry(registry);
        uint256 usdgBalance   = IERC20(reg.usdg()).balanceOf(address(this));
        if (usdgBalance == 0) return;
        _distributeFee(usdgBalance, reg);
    }

    // ── View functions ────────────────────────────────────────────────────────

    function totalValueUsdg() external view override returns (uint256) {
        return _totalValueUsdg(IWeaveRegistry(registry));
    }

    function navPerToken() external view override returns (uint256) {
        uint256 supply = totalSupply();
        if (supply == 0) return 0;
        return Math.mulDiv(_totalValueUsdg(IWeaveRegistry(registry)), 1e18, supply);
    }

    function currentWeightsBps() external view override returns (uint256[] memory) {
        IWeaveRegistry reg = IWeaveRegistry(registry);
        return _currentWeightsBps(reg, _totalValueUsdg(reg));
    }

    function maxDrift() external view override returns (uint256) {
        IWeaveRegistry reg  = IWeaveRegistry(registry);
        uint256 totalValue  = _totalValueUsdg(reg);
        uint256 len         = _constituents.length;
        uint256 maxD        = 0;

        for (uint256 i = 0; i < len; ++i) {
            (uint256 price, ) = reg.getAssetPrice(_constituents[i]);
            uint256 currentValue     = Math.mulDiv(_constituentBalances[i], price, PRICE_SCALE);
            uint256 currentWeightBps = totalValue > 0
                ? Math.mulDiv(currentValue, 10_000, totalValue)
                : 0;
            uint256 drift = currentWeightBps > _targetWeightsBps[i]
                ? currentWeightBps - _targetWeightsBps[i]
                : _targetWeightsBps[i] - currentWeightBps;
            if (drift > maxD) maxD = drift;
        }

        return maxD;
    }

    function needsRebalancing() external view override returns (bool) {
        if (!rebalancingEnabled || suspended) return false;
        IWeaveRegistry reg  = IWeaveRegistry(registry);
        uint256 totalValue  = _totalValueUsdg(reg);
        uint256 len         = _constituents.length;

        for (uint256 i = 0; i < len; ++i) {
            (uint256 price, ) = reg.getAssetPrice(_constituents[i]);
            uint256 currentValue     = Math.mulDiv(_constituentBalances[i], price, PRICE_SCALE);
            uint256 currentWeightBps = totalValue > 0
                ? Math.mulDiv(currentValue, 10_000, totalValue)
                : 0;
            uint256 drift = currentWeightBps > _targetWeightsBps[i]
                ? currentWeightBps - _targetWeightsBps[i]
                : _targetWeightsBps[i] - currentWeightBps;
            if (drift >= driftThresholdBps) return true;
        }

        return false;
    }

    function constituents() external view override returns (address[] memory) {
        return _constituents;
    }

    function targetWeightsBps() external view override returns (uint256[] memory) {
        return _targetWeightsBps;
    }

    function constituentBalances() external view override returns (uint256[] memory) {
        return _constituentBalances;
    }

    function thesis() external view override returns (string memory) {
        return _thesis;
    }

    function basketState()
        external
        view
        override
        returns (
            address[] memory constituentsOut,
            uint256[] memory targetWeightsOut,
            uint256[] memory currentWeightsOut,
            uint256[] memory balancesOut,
            uint256 totalValueOut,
            uint256 navOut,
            bool rebalancingEnabledOut,
            uint256 driftThresholdBpsOut,
            uint256 maxDriftOut
        )
    {
        IWeaveRegistry reg = IWeaveRegistry(registry);
        totalValueOut      = _totalValueUsdg(reg);
        uint256 supply     = totalSupply();

        constituentsOut      = _constituents;
        targetWeightsOut     = _targetWeightsBps;
        balancesOut          = _constituentBalances;
        currentWeightsOut    = _currentWeightsBps(reg, totalValueOut);
        rebalancingEnabledOut = rebalancingEnabled;
        driftThresholdBpsOut = driftThresholdBps;
        navOut               = supply > 0 ? Math.mulDiv(totalValueOut, 1e18, supply) : 0;
        maxDriftOut          = _computeMaxDrift(currentWeightsOut);
    }

    // ── Internal helpers ──────────────────────────────────────────────────────

    function _computeMaxDrift(
        uint256[] memory currentWeights
    ) internal view returns (uint256 maxD) {
        uint256 len = currentWeights.length;
        for (uint256 i = 0; i < len; ++i) {
            uint256 cw   = currentWeights[i];
            uint256 tw   = _targetWeightsBps[i];
            uint256 drift = cw > tw ? cw - tw : tw - cw;
            if (drift > maxD) maxD = drift;
        }
    }

    function _totalValueUsdg(IWeaveRegistry reg) internal view returns (uint256 total) {
        uint256 len = _constituents.length;
        for (uint256 i = 0; i < len; ++i) {
            (uint256 price, ) = reg.getAssetPrice(_constituents[i]);
            total += Math.mulDiv(_constituentBalances[i], price, PRICE_SCALE);
        }
    }

    function _currentWeightsBps(
        IWeaveRegistry reg,
        uint256 totalValue
    ) internal view returns (uint256[] memory weights) {
        uint256 len = _constituents.length;
        weights = new uint256[](len);
        if (totalValue == 0) return weights;

        for (uint256 i = 0; i < len; ++i) {
            (uint256 price, ) = reg.getAssetPrice(_constituents[i]);
            uint256 value = Math.mulDiv(_constituentBalances[i], price, PRICE_SCALE);
            weights[i]    = Math.mulDiv(value, 10_000, totalValue);
        }
    }

    function _collectFee(
        uint256 usdgAmount,
        IWeaveRegistry reg
    ) internal returns (uint256 feeUsdg) {
        uint256 feeBps = reg.managementFeeBps();
        if (feeBps == 0) return 0;
        feeUsdg = Math.mulDiv(usdgAmount, feeBps, 10_000);
        if (feeUsdg == 0) return 0;
        _distributeFee(feeUsdg, reg);
    }

    function _distributeFee(uint256 feeUsdg, IWeaveRegistry reg) internal {
        address usdg_      = reg.usdg();
        uint256 protocolCut = Math.mulDiv(feeUsdg, reg.protocolShareBps(), 10_000);
        uint256 creatorCut  = feeUsdg - protocolCut;

        if (protocolCut > 0) {
            IERC20(usdg_).safeTransfer(reg.protocolTreasury(), protocolCut);
        }

        if (creatorCut > 0) {
            IERC20(usdg_).forceApprove(creatorToken, creatorCut);
            ICreatorToken(creatorToken).snapshotRevenue(creatorCut);
        }
    }

    /// @notice Buy each constituent in target-weight proportions using netUsdg.
    /// Applies per-leg slippage protection using the oracle price and
    /// registry.maxSwapSlippageBps to compute minimum acceptable token output.
    function _buyConstituents(uint256 netUsdg, IWeaveRegistry reg) internal {
        address router        = reg.swapRouter();
        address usdg_         = reg.usdg();
        uint256 len           = _constituents.length;
        uint256 slippageBps   = reg.maxSwapSlippageBps();

        for (uint256 i = 0; i < len; ++i) {
            uint256 usdgForThis = Math.mulDiv(netUsdg, _targetWeightsBps[i], 10_000);
            if (usdgForThis == 0) continue;

            // Compute minimum acceptable token output from oracle price minus slippage.
            // This protects each individual swap leg on mainnet with a real DEX.
            (uint256 price, ) = reg.getAssetPrice(_constituents[i]);
            // expectedTokens = usdgForThis * PRICE_SCALE / price (floors toward zero)
            uint256 expectedTokens = Math.mulDiv(usdgForThis, PRICE_SCALE, price);
            // minTokenOut = expectedTokens * (10_000 - slippageBps) / 10_000
            uint256 minTokenOut    = Math.mulDiv(expectedTokens, 10_000 - slippageBps, 10_000);

            IERC20(usdg_).forceApprove(router, usdgForThis);
            uint256 received = IWeaveRouter(router).swapExactUSDGForToken(
                _constituents[i],
                usdgForThis,
                minTokenOut,
                address(this)
            );

            _constituentBalances[i] += received;
        }
    }

    function _sellConstituents(
        uint256 basketTokenAmount,
        uint256 supply,
        IWeaveRegistry reg
    ) internal returns (uint256 totalUsdg) {
        address router = reg.swapRouter();
        uint256 len    = _constituents.length;

        for (uint256 i = 0; i < len; ++i) {
            uint256 tokensToSell = Math.mulDiv(
                _constituentBalances[i],
                basketTokenAmount,
                supply
            );
            if (tokensToSell == 0) continue;

            IERC20(_constituents[i]).forceApprove(router, tokensToSell);
            uint256 usdgReceived = IWeaveRouter(router).swapExactTokenForUSDG(
                _constituents[i],
                tokensToSell,
                0,
                address(this)
            );

            _constituentBalances[i] -= tokensToSell;
            totalUsdg += usdgReceived;
        }
    }

    function _checkConstituentsActive() internal {
        IWeaveRegistry reg = IWeaveRegistry(registry);
        uint256 len        = _constituents.length;
        for (uint256 i = 0; i < len; ++i) {
            IWeaveRegistry.AssetConfig memory cfg = reg.assets(_constituents[i]);
            if (!cfg.active) {
                suspended = true;
                emit Suspended();
                revert BasketSuspended();
            }
        }
    }
}