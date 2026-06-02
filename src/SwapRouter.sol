// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IWeaveRouter}   from "./interfaces/IWeaveRouter.sol";
import {IWeaveRegistry} from "./interfaces/IWeaveRegistry.sol";
import {IERC20}         from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20}      from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Math}           from "@openzeppelin/contracts/utils/math/Math.sol";

/// @notice Testnet DEX adapter implementing IWeaveRouter.
/// Executes swaps against a funded token treasury at oracle prices with a
/// configurable spread that simulates real DEX fees (default 30 bps = 0.3%).
/// On mainnet, replace by deploying a real DEX adapter implementing IWeaveRouter
/// and calling registry.setSwapRouter(newAdapter) — no other contract changes needed.
contract SwapRouter is IWeaveRouter {
    using SafeERC20 for IERC20;

    IWeaveRegistry public immutable registry;
    address        public immutable owner;

    /// @notice Spread deducted from every swap output to simulate DEX fees.
    /// 30 bps = 0.3% matches Uniswap v3's most common fee tier.
    /// Governance (owner) can adjust to simulate different market conditions.
    uint256 public spreadBps;

    /// @notice tokenAmount (18 dec) * price (8 dec) / PRICE_SCALE = usdg (6 dec).
    /// derivation: 18 + 8 - 20 = 6 ✓
    uint256 private constant PRICE_SCALE = 1e20;

    /// @notice Maximum spread governance can set. Prevents accidentally bricking swaps.
    uint256 private constant MAX_SPREAD_BPS = 500; // 5%

    error InsufficientOutput(uint256 got, uint256 min);
    error InsufficientLiquidity(address token, uint256 needed, uint256 available);
    error ZeroAmount();
    error OnlyOwner();
    error InvalidSpread();

    event Funded(address indexed token, uint256 amount);
    event Withdrawn(address indexed token, uint256 amount, address indexed to);
    event SpreadUpdated(uint256 bps);

    constructor(address _registry) {
        registry  = IWeaveRegistry(_registry);
        owner     = msg.sender;
        spreadBps = 30; // 0.3% default — matches Uniswap v3 standard tier
    }

    // ── Swap functions ────────────────────────────────────────────────────────

    function swapExactUSDGForToken(
        address token,
        uint256 usdgIn,
        uint256 minTokenOut,
        address recipient
    ) external override returns (uint256 tokenOut) {
        if (usdgIn == 0) revert ZeroAmount();

        IERC20(registry.usdg()).safeTransferFrom(msg.sender, address(this), usdgIn);

        tokenOut = quoteUSDGForToken(token, usdgIn);
        if (tokenOut < minTokenOut) revert InsufficientOutput(tokenOut, minTokenOut);

        uint256 available = IERC20(token).balanceOf(address(this));
        if (tokenOut > available) revert InsufficientLiquidity(token, tokenOut, available);

        IERC20(token).safeTransfer(recipient, tokenOut);
    }

    function swapExactTokenForUSDG(
        address token,
        uint256 tokenIn,
        uint256 minUsdgOut,
        address recipient
    ) external override returns (uint256 usdgOut) {
        if (tokenIn == 0) revert ZeroAmount();

        IERC20(token).safeTransferFrom(msg.sender, address(this), tokenIn);

        usdgOut = quoteTokenForUSDG(token, tokenIn);
        if (usdgOut < minUsdgOut) revert InsufficientOutput(usdgOut, minUsdgOut);

        address usdg = registry.usdg();
        uint256 available = IERC20(usdg).balanceOf(address(this));
        if (usdgOut > available) revert InsufficientLiquidity(usdg, usdgOut, available);

        IERC20(usdg).safeTransfer(recipient, usdgOut);
    }

    function swapExactTokenForToken(
        address tokenIn,
        address tokenOut,
        uint256 amountIn,
        uint256 minOut,
        address recipient
    ) external override returns (uint256 amountOut) {
        if (amountIn == 0) revert ZeroAmount();

        IERC20(tokenIn).safeTransferFrom(msg.sender, address(this), amountIn);

        // Route through USDG: tokenIn → USDG → tokenOut, spread applied once.
        // Two-hop routing matches how a real DEX aggregator would handle this.
        uint256 usdgIntermediate = quoteTokenForUSDG(tokenIn, amountIn);
        amountOut = quoteUSDGForToken(tokenOut, usdgIntermediate);

        if (amountOut < minOut) revert InsufficientOutput(amountOut, minOut);

        uint256 available = IERC20(tokenOut).balanceOf(address(this));
        if (amountOut > available) revert InsufficientLiquidity(tokenOut, amountOut, available);

        IERC20(tokenOut).safeTransfer(recipient, amountOut);
    }

    // ── Quote functions ───────────────────────────────────────────────────────

    /// @notice usdgIn (6 dec) → tokenOut (18 dec) at oracle price minus spread.
    /// spread simulates the DEX fee taken from the output side of the swap.
    function quoteUSDGForToken(address token, uint256 usdgIn)
        public
        view
        override
        returns (uint256 tokenOut)
    {
        (uint256 price,) = registry.getAssetPrice(token);
        // Raw output at oracle price: usdgIn * 1e20 / price → 18 dec
        uint256 raw = Math.mulDiv(usdgIn, PRICE_SCALE, price);
        // Deduct spread: raw * (10_000 - spreadBps) / 10_000
        tokenOut = Math.mulDiv(raw, 10_000 - spreadBps, 10_000);
    }

    /// @notice tokenIn (18 dec) → usdgOut (6 dec) at oracle price minus spread.
    function quoteTokenForUSDG(address token, uint256 tokenIn)
        public
        view
        override
        returns (uint256 usdgOut)
    {
        (uint256 price,) = registry.getAssetPrice(token);
        // Raw output at oracle price: tokenIn * price / 1e20 → 6 dec
        uint256 raw = Math.mulDiv(tokenIn, price, PRICE_SCALE);
        // Deduct spread
        usdgOut = Math.mulDiv(raw, 10_000 - spreadBps, 10_000);
    }

    // ── Treasury management ───────────────────────────────────────────────────

    /// @notice Fund the router treasury with a token.
    /// Call after deployment for each stock token and USDG.
    function fund(address token, uint256 amount) external {
        IERC20(token).safeTransferFrom(msg.sender, address(this), amount);
        emit Funded(token, amount);
    }

    /// @notice Withdraw a specific amount of a token back to owner.
    function withdraw(address token, uint256 amount) external {
        if (msg.sender != owner) revert OnlyOwner();
        uint256 bal    = IERC20(token).balanceOf(address(this));
        uint256 toSend = amount > bal ? bal : amount;
        IERC20(token).safeTransfer(owner, toSend);
        emit Withdrawn(token, toSend, owner);
    }

    /// @notice Withdraw all balances of every token back to owner in one call.
    function withdrawAll(address[] calldata tokens) external {
        if (msg.sender != owner) revert OnlyOwner();
        for (uint256 i = 0; i < tokens.length; ++i) {
            uint256 bal = IERC20(tokens[i]).balanceOf(address(this));
            if (bal == 0) continue;
            IERC20(tokens[i]).safeTransfer(owner, bal);
            emit Withdrawn(tokens[i], bal, owner);
        }
    }

    /// @notice Update the spread to simulate different DEX fee tiers.
    /// 10 bps = 0.1% (Uniswap v3 stable tier)
    /// 30 bps = 0.3% (Uniswap v3 standard tier)
    /// 100 bps = 1.0% (Uniswap v3 exotic tier)
    function setSpread(uint256 bps) external {
        if (msg.sender != owner)  revert OnlyOwner();
        if (bps > MAX_SPREAD_BPS) revert InvalidSpread();
        spreadBps = bps;
        emit SpreadUpdated(bps);
    }

    /// @notice Check how much of a token the router treasury holds.
    function balance(address token) external view returns (uint256) {
        return IERC20(token).balanceOf(address(this));
    }
}