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
    address public immutable override usdg;

    uint256 public override managementFeeBps;
    uint256 public override protocolShareBps;
    uint256 public override creatorShareBps;
    uint256 public override minAUMForAutomation;
    uint256 public override oracleStalenessSecs;
    uint256 public override minFirstDepositUsdg;
    uint256 public override maxConstituents;
    uint256 public override minWeightBps;

    /// @notice Protocol-level pause. When true, all basket deposits and
    /// redemptions revert. Allows governance to freeze the protocol instantly
    /// in response to a critical oracle failure or exploit attempt.
    bool public paused;

    /// @notice Minimum USDG value of a rebalancing trade leg. Legs below
    /// this threshold are skipped to avoid wasting gas on dust positions.
    /// Denominated in 6-decimal USDG (e.g. 1_000_000 = $1).
    uint256 public override minRebalanceTradeSizeUsdg;

    /// @notice Maximum acceptable slippage in bps on individual constituent
    /// swaps during deposit and automation-triggered rebalancing.
    /// Applied per swap leg against the oracle quote. Default 100 bps = 1%.
    uint256 public override maxSwapSlippageBps;

    mapping(address => AssetConfig) private _assets;
    address[]                       private _supportedAssets;
    mapping(address => bool)        private _isBasket;
    address[]                       private _allBaskets;
    mapping(address => BasketMeta)  private _basketMeta;

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
    error ProtocolPaused();

    event GovernanceNominated(address indexed nominee);
    event GovernanceAccepted(address indexed newGovernance);
    event AssetAdded(address indexed token, string symbol, string sector);
    event AssetDeactivated(address indexed token);
    event BasketRegistered(
        address indexed basket,
        address indexed creatorToken,
        address indexed creator
    );
    event BasketSuspended(address indexed basket);
    event SwapRouterUpdated(address indexed router);
    event ManagementFeeUpdated(
        uint256 feeBps,
        uint256 protocolShareBps,
        uint256 creatorShareBps
    );
    event MinAUMUpdated(uint256 minAUM);
    event BasketFactoryUpdated(address indexed factory);
    event AutomationContractUpdated(address indexed automation);
    event ProtocolPausedEvent(address indexed by);
    event ProtocolUnpausedEvent(address indexed by);
    event MinRebalanceTradeSizeUpdated(uint256 size);
    event MaxSwapSlippageUpdated(uint256 bps);
    event OracleStalenessSecs(uint256 secs);

    modifier onlyGovernance() {
        if (msg.sender != governance) revert NotGovernance();
        _;
    }

    modifier onlyFactory() {
        if (msg.sender != basketFactory) revert NotFactory();
        _;
    }

    /// @notice Reverts if the protocol is paused. Applied to deposit and
    /// redeem entry points on every basket. Not applied to rebalancing —
    /// rebalancing during a pause is acceptable since it reduces risk.
    modifier whenNotPaused() {
        if (paused) revert ProtocolPaused();
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
        uint256 _minWeightBps,
        uint256 _minRebalanceTradeSizeUsdg,
        uint256 _maxSwapSlippageBps
    ) {
        if (_governance       == address(0)) revert ZeroAddress();
        if (_usdg             == address(0)) revert ZeroAddress();
        if (_protocolTreasury == address(0)) revert ZeroAddress();
        if (_managementFeeBps > 1_000)       revert InvalidFeeBps();
        if (_protocolShareBps > 10_000)      revert InvalidFeeSplit();
        if (_maxConstituents  == 0)          revert InvalidParameter();
        if (_minWeightBps     == 0)          revert InvalidParameter();
        if (_maxSwapSlippageBps > 1_000)     revert InvalidParameter(); // cap at 10%

        governance                  = _governance;
        usdg                        = _usdg;
        protocolTreasury            = _protocolTreasury;
        managementFeeBps            = _managementFeeBps;
        protocolShareBps            = _protocolShareBps;
        creatorShareBps             = 10_000 - _protocolShareBps;
        minAUMForAutomation         = _minAUMForAutomation;
        oracleStalenessSecs         = _oracleStalenessSecs;
        minFirstDepositUsdg         = _minFirstDepositUsdg;
        maxConstituents             = _maxConstituents;
        minWeightBps                = _minWeightBps;
        minRebalanceTradeSizeUsdg   = _minRebalanceTradeSizeUsdg;
        maxSwapSlippageBps          = _maxSwapSlippageBps;
    }

    // ── Asset catalogue ───────────────────────────────────────────────────────

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

    function deactivateAsset(address token) external override onlyGovernance {
        if (_assets[token].tokenAddress == address(0)) revert AssetNotFound(token);
        _assets[token].active = false;
        emit AssetDeactivated(token);
    }

    function assets(address token)
        external
        view
        override
        returns (AssetConfig memory)
    {
        return _assets[token];
    }

    function getSupportedAssets()
        external
        view
        override
        returns (AssetConfig[] memory)
    {
        uint256 len = _supportedAssets.length;
        AssetConfig[] memory result = new AssetConfig[](len);
        for (uint256 i = 0; i < len; ++i) {
            result[i] = _assets[_supportedAssets[i]];
        }
        return result;
    }

    /// @notice Called by every basket operation that needs a price.
    /// Returns the full Chainlink-compatible round data shape so the same
    /// interface works on both testnet (OracleAdapter) and mainnet
    /// (direct Chainlink AggregatorV3Interface). Reverts on stale or
    /// non-positive prices — no silent mis-pricing ever.
    function getAssetPrice(address token)
        external
        view
        override
        returns (uint256 price, uint256 updatedAt)
    {
        AssetConfig storage cfg = _assets[token];
        if (cfg.tokenAddress == address(0)) revert AssetNotFound(token);
        if (!cfg.active)                    revert AssetNotActive(token);

        (
            /* roundId */,
            int256 answer,
            /* startedAt */,
            uint256 _updatedAt,
            /* answeredInRound */
        ) = IWeaveOracle(cfg.oracle).latestPrice();

        if (answer <= 0) revert NegativePrice(token);

        // forge-lint: disable-next-line(block-timestamp)
        if (block.timestamp - _updatedAt > oracleStalenessSecs)
            revert StalePrice(token);

        // Safe: answer > 0 guaranteed above.
        // forge-lint: disable-next-line(unsafe-typecast)
        price     = uint256(answer);
        updatedAt = _updatedAt;
    }

    // ── Basket registry ───────────────────────────────────────────────────────

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

    function getAllBaskets()
        external
        view
        override
        returns (BasketMeta[] memory)
    {
        uint256 len = _allBaskets.length;
        BasketMeta[] memory result = new BasketMeta[](len);
        for (uint256 i = 0; i < len; ++i) {
            result[i] = _basketMeta[_allBaskets[i]];
        }
        return result;
    }

    function basketMeta(address basket)
        external
        view
        override
        returns (BasketMeta memory)
    {
        return _basketMeta[basket];
    }

    // ── Protocol pause ────────────────────────────────────────────────────────

    /// @notice Immediately freezes all basket deposits and redemptions.
    /// Use in response to a critical oracle failure or active exploit.
    /// Rebalancing is NOT paused — reducing drift during a pause is safe.
    function pauseAll() external onlyGovernance {
        paused = true;
        emit ProtocolPausedEvent(msg.sender);
    }

    /// @notice Resumes normal protocol operation after a pause.
    function unpauseAll() external onlyGovernance {
        paused = false;
        emit ProtocolUnpausedEvent(msg.sender);
    }

    // ── Governance setters ────────────────────────────────────────────────────

    function setSwapRouter(address router) external override onlyGovernance {
        if (router == address(0)) revert ZeroAddress();
        swapRouter = router;
        emit SwapRouterUpdated(router);
    }

    function setManagementFee(uint256 feeBps) external override onlyGovernance {
        if (feeBps > 1_000) revert InvalidFeeBps();
        managementFeeBps = feeBps;
        emit ManagementFeeUpdated(feeBps, protocolShareBps, creatorShareBps);
    }

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
        emit OracleStalenessSecs(secs);
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

    function setMinRebalanceTradeSize(uint256 size) external onlyGovernance {
        minRebalanceTradeSizeUsdg = size;
        emit MinRebalanceTradeSizeUpdated(size);
    }

    function setMaxSwapSlippage(uint256 bps) external onlyGovernance {
        if (bps > 1_000) revert InvalidParameter(); // cap at 10%
        maxSwapSlippageBps = bps;
        emit MaxSwapSlippageUpdated(bps);
    }

    function setBasketFactory(address factory) external override onlyGovernance {
        if (factory == address(0)) revert ZeroAddress();
        basketFactory = factory;
        emit BasketFactoryUpdated(factory);
    }

    function setAutomationContract(address automation)
        external
        override
        onlyGovernance
    {
        if (automation == address(0)) revert ZeroAddress();
        automationContract = automation;
        emit AutomationContractUpdated(automation);
    }

    function setProtocolTreasury(address treasury) external onlyGovernance {
        if (treasury == address(0)) revert ZeroAddress();
        protocolTreasury = treasury;
    }

    function nominateGovernance(address nominee)
        external
        override
        onlyGovernance
    {
        if (nominee == address(0)) revert ZeroAddress();
        pendingGovernance = nominee;
        emit GovernanceNominated(nominee);
    }

    function acceptGovernance() external override {
        if (msg.sender != pendingGovernance) revert NotPendingGovernance();
        governance        = pendingGovernance;
        pendingGovernance = address(0);
        emit GovernanceAccepted(governance);
    }
}