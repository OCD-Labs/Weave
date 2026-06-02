// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IWeaveRouter}    from "./interfaces/IWeaveRouter.sol";
import {IWeaveRegistry}  from "./interfaces/IWeaveRegistry.sol";
import {IERC20}          from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20}       from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Math}            from "@openzeppelin/contracts/utils/math/Math.sol";

/// @notice Testnet-only DEX substitute. Executes at oracle prices, zero slippage.
/// Holds a treasury of test USDG and stock tokens funded by the deployer.
/// Replace with a real DEX router address in registry.setSwapRouter() on mainnet.
contract MockSwapRouter is IWeaveRouter {
    using SafeERC20 for IERC20;

    IWeaveRegistry public immutable registry;
    address public immutable owner;

    // 8-decimal oracle price × this factor → 6-decimal USDG amount
    // tokenAmount (18 dec) * price (8 dec) / 1e20 = usdgAmount (6 dec)
    // derivation: 18 + 8 - 20 = 6 ✓
    uint256 private constant PRICE_SCALE = 1e20;

    error InsufficientOutput(uint256 got, uint256 min);
    error ZeroAmount();
    error OnlyOwner();

    event Funded(address indexed token, uint256 amount);
    event Withdrawn(address indexed token, uint256 amount, address indexed to);

    constructor(address _registry) {
        registry = IWeaveRegistry(_registry);
        owner    = msg.sender;
    }

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

        IERC20(registry.usdg()).safeTransfer(recipient, usdgOut);
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

        // Route through USDG: tokenIn → USDG → tokenOut, all at oracle prices.
        uint256 usdgIntermediate = quoteTokenForUSDG(tokenIn, amountIn);
        amountOut = quoteUSDGForToken(tokenOut, usdgIntermediate);

        if (amountOut < minOut) revert InsufficientOutput(amountOut, minOut);

        IERC20(tokenOut).safeTransfer(recipient, amountOut);
    }

    /// @notice usdgIn (6 dec) → tokenOut (18 dec) at oracle price (8 dec).
    function quoteUSDGForToken(address token, uint256 usdgIn)
        public
        view
        override
        returns (uint256 tokenOut)
    {
        (uint256 price,) = registry.getAssetPrice(token);
        // usdgIn * 1e20 / price  →  (6 dec * 1e20) / 8 dec = 18 dec  ✓
        // floors toward zero — any residual stays in the router treasury
        tokenOut = Math.mulDiv(usdgIn, PRICE_SCALE, price);
    }

    /// @notice tokenIn (18 dec) → usdgOut (6 dec) at oracle price (8 dec).
    function quoteTokenForUSDG(address token, uint256 tokenIn)
        public
        view
        override
        returns (uint256 usdgOut)
    {
        (uint256 price,) = registry.getAssetPrice(token);
        // tokenIn * price / 1e20  →  (18 dec * 8 dec) / 1e20 = 6 dec  ✓
        // floors toward zero — dust stays in the router treasury
        usdgOut = Math.mulDiv(tokenIn, price, PRICE_SCALE);
    }

    /// @notice Fund the router with a test token. Call this after deployment
    /// for each stock token and USDG so swaps have liquidity.
    function fund(address token, uint256 amount) external {
        IERC20(token).safeTransferFrom(msg.sender, address(this), amount);
        emit Funded(token, amount);
    }

    /// @notice Withdraw a specific token back to owner.
    /// Use this if deployment fails or you need your test tokens back.
    function withdraw(address token, uint256 amount) external {
        if (msg.sender != owner) revert OnlyOwner();
        uint256 bal = IERC20(token).balanceOf(address(this));
        uint256 toSend = amount > bal ? bal : amount;
        IERC20(token).safeTransfer(owner, toSend);
        emit Withdrawn(token, toSend, owner);
    }

    /// @notice Withdraw ALL of every token back to owner in one call.
    /// Pass the list of token addresses you funded. Cleans up everything at once.
    function withdrawAll(address[] calldata tokens) external {
        if (msg.sender != owner) revert OnlyOwner();
        for (uint256 i = 0; i < tokens.length; ++i) {
            uint256 bal = IERC20(tokens[i]).balanceOf(address(this));
            if (bal == 0) continue;
            IERC20(tokens[i]).safeTransfer(owner, bal);
            emit Withdrawn(tokens[i], bal, owner);
        }
    }

    /// @notice Check how much of a token the router holds.
    function balance(address token) external view returns (uint256) {
        return IERC20(token).balanceOf(address(this));
    }
}