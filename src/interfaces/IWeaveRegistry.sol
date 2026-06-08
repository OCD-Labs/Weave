// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

interface IWeaveRegistry {
    struct AssetConfig {
        address tokenAddress;
        address oracle;       // IWeaveOracle implementor — OracleAdapter on testnet, Chainlink on mainnet
        string  symbol;
        string  name;
        string  sector;
        bool    active;
    }

    struct BasketMeta {
        address basket;
        address creatorToken;
        address creator;
        bool    active;
        uint256 createdAt;
    }

    // Asset catalogue

    function addAsset(AssetConfig calldata config) external;
    function deactivateAsset(address token) external;
    function reactivateAsset(address token) external;
    function assets(address token) external view returns (AssetConfig memory);
    function getSupportedAssets() external view returns (AssetConfig[] memory);

    /// @notice Reads the oracle for token, validates staleness and positivity.
    /// Reverts with StalePrice(token) if updatedAt is too old.
    /// Reverts with NegativePrice(token) if the feed returns <= 0.
    function getAssetPrice(address token)
        external
        view
        returns (uint256 price, uint256 updatedAt);

    // Basket registry

    function registerBasket(
        address basket,
        address creatorToken,
        address creator
    ) external;

    function isBasket(address basket) external view returns (bool);
    function getAllBaskets() external view returns (BasketMeta[] memory);
    function basketMeta(address basket) external view returns (BasketMeta memory);

    // Protocol state reads

    function usdg()                     external view returns (address);
    function swapRouter()               external view returns (address);
    function protocolTreasury()         external view returns (address);
    function managementFeeBps()         external view returns (uint256);
    function protocolShareBps()         external view returns (uint256);
    function creatorShareBps()          external view returns (uint256);
    function minAUMForAutomation()      external view returns (uint256);
    function oracleStalenessSecs()      external view returns (uint256);
    function minFirstDepositUsdg()      external view returns (uint256);
    function maxConstituents()          external view returns (uint256);
    function minWeightBps()             external view returns (uint256);
    function minRebalanceTradeSizeUsdg() external view returns (uint256);
    function maxSwapSlippageBps()       external view returns (uint256);
    function paused()                   external view returns (bool);

    // Governance setters

    function setSwapRouter(address router) external;
    function setManagementFee(uint256 feeBps) external;
    function setMinAUM(uint256 minAUM) external;
    function nominateGovernance(address nominee) external;
    function acceptGovernance() external;
    function setBasketFactory(address factory) external;
    function setAutomationContract(address automation) external;
    function pauseAll() external;
    function unpauseAll() external;
    function setMinRebalanceTradeSize(uint256 size) external;
    function setMaxSwapSlippage(uint256 bps) external;
}