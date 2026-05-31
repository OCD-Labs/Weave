// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @notice DEX router abstraction — lets governance swap in a real DEX on mainnet
/// without touching any basket contract. MockSwapRouter implements this on testnet.
interface IWeaveRouter {
    /// @notice Sell exact usdgIn, receive at least minTokenOut of token.
    function swapExactUSDGForToken(
        address token,
        uint256 usdgIn,
        uint256 minTokenOut,
        address recipient
    ) external returns (uint256 tokenOut);

    /// @notice Sell exact tokenIn, receive at least minUsdgOut USDG.
    function swapExactTokenForUSDG(
        address token,
        uint256 tokenIn,
        uint256 minUsdgOut,
        address recipient
    ) external returns (uint256 usdgOut);

    /// @notice Sell tokenIn for tokenOut — used during rebalancing between constituents.
    function swapExactTokenForToken(
        address tokenIn,
        address tokenOut,
        uint256 amountIn,
        uint256 minOut,
        address recipient
    ) external returns (uint256 amountOut);

    function quoteUSDGForToken(address token, uint256 usdgIn)
        external view returns (uint256 tokenOut);

    function quoteTokenForUSDG(address token, uint256 tokenIn)
        external view returns (uint256 usdgOut);
}