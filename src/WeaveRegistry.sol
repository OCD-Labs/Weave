// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

import {IWeaveRegistry} from "./interfaces/IWeaveRegistry.sol";
import {IWeaveOracle}   from "./interfaces/IWeaveOracle.sol";
import {Math}           from "@openzeppelin/contracts/utils/math/Math.sol";

/// @notice Global config and asset catalogue for the Weave protocol.
/// Every basket, factory, and automation contract reads from here.
/// Governance is a two-step transfer — nominate then accept — so a typo
/// can't permanently lock the protocol.
contract WeaveRegistry is IWeaveRegistry {
    address public governance;
    address public pendingGovernance;

    address public basketFactory;
    address public automationContract;
    address public override swapRouter;
    address public override protocolTreasury;
    address public override usdg;

    /// @notice Fee charged as bps of USDG on every deposit and redemption.
    uint256 public override managementFeeBps;

    /// @notice Protocol's share of the management fee in bps (e.g. 2000 = 20%).
    uint256 public override protocolShareBps;

    /// @notice Creator's share of the management fee in bps (e.g. 8000 = 80%).
    /// Must equal 10_000 - protocolShareBps. Enforced on setManagementFee.
    uint256 public override creatorShareBps;

    /// @notice Baskets below this USDG value are excluded from protocol-funded automation.
    uint256 public override minAUMForAutomation;

    /// @notice Max age in seconds of an oracle price before reads revert with StalePrice.
    uint256 public override oracleStalenessSecs;

    /// @notice Minimum USDG the basket creator must seed on deployment.
    /// Prevents economically trivial baskets from being listed.
    uint256 public override minFirstDepositUsdg;

    /// @notice Maximum number of constituent stocks per basket.
    uint256 public override maxConstituents;

    /// @notice Minimum weight per constituent in bps (e.g. 100 = 1%).
    uint256 public override minWeightBps;

    /// @notice tokenAddress → asset config. Oracle field is IWeaveOracle implementor.
    mapping(address => AssetConfig) private _assets;

    /// @notice Ordered list of all ever-added token addresses for enumeration.
    address[] private _supportedAssets;

    mapping(address => bool)       private _isBasket;
    address[]                      private _allBaskets;
    mapping(address => BasketMeta) private _basketMeta;

    error NotGovernance();
    error NotFactory();
    error NotPendingGovernance();
    error ZeroAddress();
    error AssetAlreadyExists(address token);
    error AssetNotFound(address token);
    error AssetNotActive(address token);
    error BasketAlreadyRegistered(address basket);
    error StalePrice(address token);
    error NegativePrice(address token);
    error InvalidFeeSplit();
    error InvalidFeeBps();
    error InvalidParameter();

    event GovernanceNominated(address indexed nominee);
    event GovernanceAccepted(address indexed newGovernance);
    event AssetAdded(address indexed token, string symbol, string sector);
    event AssetDeactivated(address indexed token);
    event BasketRegistered(address indexed basket, address indexed creatorToken, address indexed creator);
    event BasketSuspended(address indexed basket);
    event SwapRouterUpdated(address indexed router);
    event ManagementFeeUpdated(uint256 feeBps, uint256 protocolShareBps, uint256 creatorShareBps);
    event MinAUMUpdated(uint256 minAUM);
    event BasketFactoryUpdated(address indexed factory);
    event AutomationContractUpdated(address indexed automation);

    modifier onlyGovernance() {
        if (msg.sender != governance) revert NotGovernance();
        _;
    }

    modifier onlyFactory() {
        if (msg.sender != basketFactory) revert NotFactory();
        _;
    }

    constructor(
        address _governance,
        address _usdg,
        address _protocolTreasury,
        uint256 _managementFeeBps,
        uint256 _protocolShareBps,
        uint256 _minAUMForAutomation,
        uint256 _oracleStalenessSecs,
        uint256 _minFirstDepositUsdg,
        uint256 _maxConstituents,
        uint256 _minWeightBps
    ) {
        if (_governance      == address(0)) revert ZeroAddress();
        if (_usdg            == address(0)) revert ZeroAddress();
        if (_protocolTreasury == address(0)) revert ZeroAddress();
        if (_managementFeeBps > 1_000)      revert InvalidFeeBps();   // cap at 10%
        if (_protocolShareBps > 10_000)     revert InvalidFeeSplit();
        if (_maxConstituents == 0)          revert InvalidParameter();
        if (_minWeightBps    == 0)          revert InvalidParameter();

        governance            = _governance;
        usdg                  = _usdg;
        protocolTreasury      = _protocolTreasury;
        managementFeeBps      = _managementFeeBps;
        protocolShareBps      = _protocolShareBps;
        creatorShareBps       = 10_000 - _protocolShareBps;
        minAUMForAutomation   = _minAUMForAutomation;
        oracleStalenessSecs   = _oracleStalenessSecs;
        minFirstDepositUsdg   = _minFirstDepositUsdg;
        maxConstituents       = _maxConstituents;
        minWeightBps          = _minWeightBps;
    }

    /// @notice Only governance can add assets — bad entries corrupt all basket valuations.
    function addAsset(AssetConfig calldata config) external override onlyGovernance {
        if (config.tokenAddress == address(0)) revert ZeroAddress();
        if (config.oracle       == address(0)) revert ZeroAddress();
        if (_assets[config.tokenAddress].tokenAddress != address(0)) {
            revert AssetAlreadyExists(config.tokenAddress);
        }

        _assets[config.tokenAddress] = config;
        _supportedAssets.push(config.tokenAddress);

        emit AssetAdded(config.tokenAddress, config.symbol, config.sector);
    }

    /// @notice Deactivating an asset suspends all baskets that hold it on their next interaction.
    function deactivateAsset(address token) external override onlyGovernance {
        if (_assets[token].tokenAddress == address(0)) revert AssetNotFound(token);
        _assets[token].active = false;
        emit AssetDeactivated(token);
    }

    function assets(address token) external view override returns (AssetConfig memory) {
        return _assets[token];
    }

    function getSupportedAssets() external view override returns (AssetConfig[] memory) {
        uint256 len = _supportedAssets.length;
        AssetConfig[] memory result = new AssetConfig[](len);
        for (uint256 i = 0; i < len; ++i) {
            result[i] = _assets[_supportedAssets[i]];
        }
        return result;
    }

    /// @notice Called by every basket operation that needs a price.
    /// Reverts if the oracle hasn't been updated within oracleStalenessSecs.
    /// Casting int256 → uint256 is safe here because we revert on non-positive prices.
    function getAssetPrice(address token)
        external
        view
        override
        returns (uint256 price, uint256 updatedAt)
    {
        AssetConfig storage cfg = _assets[token];
        if (cfg.tokenAddress == address(0)) revert AssetNotFound(token);
        if (!cfg.active)                    revert AssetNotActive(token);

        int256 rawPrice;
        (rawPrice, updatedAt) = IWeaveOracle(cfg.oracle).latestPrice();

        // Non-positive price from a feed means something is badly wrong — reject it.
        if (rawPrice <= 0) revert NegativePrice(token);

        // Staleness check: if the feed hasn't updated within the window, all valuations
        // using this price would be wrong. Fail hard rather than silently mis-price.
        // forge-lint: disable-next-line(block-timestamp)
        if (block.timestamp - updatedAt > oracleStalenessSecs) revert StalePrice(token);

        // Safe: rawPrice > 0 is guaranteed by the revert above, so uint256 cast cannot truncate.
        // forge-lint: disable-next-line(unsafe-typecast)
        price = uint256(rawPrice);
    }

    /// @notice Only BasketFactory can register — prevents arbitrary contracts claiming basket status.
    function registerBasket(
        address basket,
        address creatorToken,
        address creator
    ) external override onlyFactory {
        if (basket       == address(0)) revert ZeroAddress();
        if (creatorToken == address(0)) revert ZeroAddress();
        if (creator      == address(0)) revert ZeroAddress();
        if (_isBasket[basket])          revert BasketAlreadyRegistered(basket);

        _isBasket[basket] = true;
        _allBaskets.push(basket);
        _basketMeta[basket] = BasketMeta({
            basket:       basket,
            creatorToken: creatorToken,
            creator:      creator,
            active:       true,
            createdAt:    block.timestamp
        });

        emit BasketRegistered(basket, creatorToken, creator);
    }

    function isBasket(address basket) external view override returns (bool) {
        return _isBasket[basket];
    }

    function getAllBaskets() external view override returns (BasketMeta[] memory) {
        uint256 len = _allBaskets.length;
        BasketMeta[] memory result = new BasketMeta[](len);
        for (uint256 i = 0; i < len; ++i) {
            result[i] = _basketMeta[_allBaskets[i]];
        }
        return result;
    }

    function basketMeta(address basket) external view override returns (BasketMeta memory) {
        return _basketMeta[basket];
    }

    function setSwapRouter(address router) external override onlyGovernance {
        if (router == address(0)) revert ZeroAddress();
        swapRouter = router;
        emit SwapRouterUpdated(router);
    }

    /// @notice Fee split must always sum to 10_000 bps. Protocol + creator = 100%.
    function setManagementFee(uint256 feeBps) external override onlyGovernance {
        if (feeBps > 1_000) revert InvalidFeeBps();   // cap at 10%
        managementFeeBps = feeBps;
        emit ManagementFeeUpdated(feeBps, protocolShareBps, creatorShareBps);
    }

    /// @notice Update the protocol/creator fee split independently of the fee rate.
    function setFeeSplit(uint256 _protocolShareBps) external onlyGovernance {
        if (_protocolShareBps > 10_000) revert InvalidFeeSplit();
        protocolShareBps = _protocolShareBps;
        creatorShareBps  = 10_000 - _protocolShareBps;
        emit ManagementFeeUpdated(managementFeeBps, _protocolShareBps, creatorShareBps);
    }

    function setMinAUM(uint256 minAUM) external override onlyGovernance {
        minAUMForAutomation = minAUM;
        emit MinAUMUpdated(minAUM);
    }

    function setOracleStaleness(uint256 secs) external onlyGovernance {
        if (secs == 0) revert InvalidParameter();
        oracleStalenessSecs = secs;
    }

    function setMinFirstDeposit(uint256 amount) external onlyGovernance {
        if (amount == 0) revert InvalidParameter();
        minFirstDepositUsdg = amount;
    }

    function setMaxConstituents(uint256 max) external onlyGovernance {
        if (max == 0) revert InvalidParameter();
        maxConstituents = max;
    }

    function setMinWeightBps(uint256 bps) external onlyGovernance {
        if (bps == 0 || bps >= 10_000) revert InvalidParameter();
        minWeightBps = bps;
    }

    function setBasketFactory(address factory) external override onlyGovernance {
        if (factory == address(0)) revert ZeroAddress();
        basketFactory = factory;
        emit BasketFactoryUpdated(factory);
    }

    function setAutomationContract(address automation) external override onlyGovernance {
        if (automation == address(0)) revert ZeroAddress();
        automationContract = automation;
        emit AutomationContractUpdated(automation);
    }

    function setProtocolTreasury(address treasury) external onlyGovernance {
        if (treasury == address(0)) revert ZeroAddress();
        protocolTreasury = treasury;
    }

    /// @notice Step 1: current governance nominates a successor.
    function nominateGovernance(address nominee) external override onlyGovernance {
        if (nominee == address(0)) revert ZeroAddress();
        pendingGovernance = nominee;
        emit GovernanceNominated(nominee);
    }

    /// @notice Step 2: the nominee accepts, completing the transfer.
    /// The nominee must call this themselves — prevents accidental transfers to wrong address.
    function acceptGovernance() external override {
        if (msg.sender != pendingGovernance) revert NotPendingGovernance();
        governance        = pendingGovernance;
        pendingGovernance = address(0);
        emit GovernanceAccepted(governance);
    }
}