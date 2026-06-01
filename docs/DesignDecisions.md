# Weave — System Design Document

**Author:** Yemi (Ikeh Chukwuka Favour) — OCD Labs

**Date:** May 30, 2026

**Status:** Implemented and deployed. All contracts, backend, and AI agent are live on Robinhood Chain testnet.

---

## 1. Scope

This document covers every smart contract, every interface, every backend service, every external integration, and every edge case in the Weave system. It does not cover the frontend, which is covered in a separate document. When this document says the system it means everything except the user-facing web application: the Solidity contracts deployed on Robinhood Chain, the Go backend service, the AI composition engine, the Chainlink Automation setup, and the DEX router abstraction.

---

## 2. Architecture Overview

Weave has four layers.

The contract layer handles all on-chain logic: basket deployment, deposit, redemption, rebalancing, fee collection, and creator revenue distribution. Every basket is a separate contract deployed via the minimal proxy pattern pointing to a shared implementation. Every basket also has an associated creator token contract implementing ERC-7641 for revenue sharing.

The automation layer handles the continuous monitoring of rebalancing-enabled baskets. A single Chainlink Automation upkeep job monitors all active baskets and triggers rebalancing trades through the DEX router when drift exceeds a basket's configured threshold.

The backend layer handles off-chain indexing, price caching, basket performance history, the stock catalogue service, and the AI composition endpoint. It is built in Go with SQLite and exposes an HTTP API to the frontend.

The AI layer is a TypeScript service that receives a natural language thesis from the frontend, reads the stock catalogue from the backend, calls an LLM provider (currently OpenAI GPT-4.1-mini) with a provider abstraction supporting OpenAI, Anthropic, and Ollama, to generate a basket composition proposal, and returns structured JSON to the frontend for human review before any on-chain action is taken.

<p align="center">
  <img src="https://i.imgur.com/fOFTeut.png" alt="weave-sequence-diagram" />
</p>

---

## 3. Contract Inventory

```
WeaveRegistry               — global config, supported assets, basket index
BasketFactory               — deploys basket proxies and creator tokens atomically
BasketImplementation        — shared logic contract for all basket proxies
BasketProxy                 — ERC-1167 minimal proxy, one per basket (IS the basket token)
CreatorToken                — ERC-7641 revenue share token, one per basket
WeaveAutomation             — Chainlink Automation compatible rebalancing monitor
MockSwapRouter              — testnet-only DEX substitute priced at Chainlink oracle rates
```

---

## 4. WeaveRegistry

The registry is the single source of truth for all protocol configuration and is deployed once. All other contracts read from it.

```solidity
contract WeaveRegistry {
    address public governance;
    address public pendingGovernance;
    address public basketFactory;
    address public automationContract;
    address public swapRouter;              // IWeaveRouter implementor, swappable
    address public protocolTreasury;
    address public usdg;                    // canonical USDG on Robinhood Chain

    uint256 public managementFeeBps;        // charged on each deposit and redemption, e.g. 50 = 0.5%
    uint256 public protocolShareBps;        // protocol's cut of fee, e.g. 2000 = 20%
    uint256 public creatorShareBps;         // creator's cut of fee, e.g. 8000 = 80%
    uint256 public minAUMForAutomation;     // minimum basket AUM for protocol-funded rebalancing
    uint256 public oracleStalenessSecs;     // max age of Chainlink price before reverting
    uint256 public minFirstDepositUsdg;     // prevents first-depositor manipulation
    uint256 public maxConstituents;         // max stocks per basket, e.g. 20
    uint256 public minWeightBps;            // minimum weight per constituent, e.g. 100 = 1%

    struct AssetConfig {
        address tokenAddress;
        address chainlinkFeed;
        string symbol;
        string name;
        string sector;
        bool active;
    }

    mapping(address => AssetConfig) public assets;        // tokenAddress → config
    address[] public supportedAssets;                     // enumerable list for catalogue
    mapping(address => bool) public isBasket;
    address[] public allBaskets;
    mapping(address => BasketMeta) public basketMeta;

    struct BasketMeta {
        address basket;
        address creatorToken;
        address creator;
        bool active;
        uint256 createdAt;
    }

    function addAsset(AssetConfig calldata config) external onlyGovernance;
    function deactivateAsset(address token) external onlyGovernance;
    function registerBasket(address basket, address creatorToken, address creator) external onlyFactory;
    function setSwapRouter(address router) external onlyGovernance;
    function setManagementFee(uint256 feeBps) external onlyGovernance;
    function setMinAUM(uint256 minAUM) external onlyGovernance;
    function nominateGovernance(address nominee) external onlyGovernance;
    function acceptGovernance() external;
    function getAllBaskets() external view returns (BasketMeta[] memory);
    function getSupportedAssets() external view returns (AssetConfig[] memory);
    function getAssetPrice(address token) external view returns (uint256 price, uint256 updatedAt);
}
```

`getAssetPrice` calls `latestRoundData()` on the Chainlink aggregator for the given token, validates that `updatedAt` is within `oracleStalenessSecs` of `block.timestamp`, and reverts with `StalePrice()` if not. This function is called by every basket operation that requires a price, ensuring stale data cannot be used in any valuation.

---

## 5. BasketFactory

The factory deploys a basket proxy and a creator token atomically in a single transaction. It is the only address with permission to call `registry.registerBasket()`.

```solidity
contract BasketFactory {
    address public immutable registry;
    address public immutable implementation;

    event BasketCreated(
        address indexed basket,
        address indexed creatorToken,
        address indexed creator,
        string name,
        bool rebalancingEnabled
    );

    function createBasket(
        string calldata name,
        string calldata symbol,
        string calldata thesis,
        address[] calldata constituents,
        uint256[] calldata targetWeightsBps,
        bool rebalancingEnabled,
        uint256 driftThresholdBps,
        uint256 initialDepositUsdg
    ) external returns (address basket, address creatorToken);
}
```

On `createBasket()`:

The factory validates that `constituents.length <= registry.maxConstituents`, that `constituents.length == targetWeightsBps.length`, that all weights sum to exactly 10,000, that every weight is at least `registry.minWeightBps`, that every constituent address is active in the registry, and that `initialDepositUsdg >= registry.minFirstDepositUsdg`. If rebalancing is enabled, it validates that `driftThresholdBps > 0` and `driftThresholdBps <= 5000` (50% max drift threshold makes no sense above that).

It then deploys a BasketProxy using OpenZeppelin's `Clones.clone(implementation)`, calls `basket.initialize(...)` with all parameters, deploys a `CreatorToken`, calls `registry.registerBasket(basket, creatorToken, msg.sender)`, and transfers `initialDepositUsdg` worth of USDG from the caller to fund the first deposit into the basket. The creator receives the initial basket tokens from that first deposit and the full supply of creator tokens.

---

## 6. BasketImplementation

This is the logic contract that every BasketProxy delegates to. It is also the ERC-20 basket token. It handles deposits, redemptions, rebalancing, and fee collection.

**Storage layout (must never be reordered between deployments):**

```solidity
// Slot 0: bool _initialized
// Slot 1: bool _locked (reentrancy)
// ERC-20 storage (OpenZeppelin UpgradeableERC20 base):
// Slot 2: string _name
// Slot 3: string _symbol
// Slot 4: mapping(address => uint256) _balances
// Slot 5: mapping(address => mapping(address => uint256)) _allowances
// Slot 6: uint256 _totalSupply
// Weave basket storage:
// Slot 7: address registry
// Slot 8: address creatorToken
// Slot 9: string thesis
// Slot 10: address[] constituents
// Slot 11: uint256[] targetWeightsBps
// Slot 12: uint256[] constituentBalances
// Slot 13: bool rebalancingEnabled
// Slot 14: uint256 driftThresholdBps
// Slot 15: uint256 lastFeeCollectionTimestamp
// Slot 16: address creator
// Slot 17: bool suspended (set true if a constituent becomes inactive)
```

**Initialization:**

```solidity
function initialize(
    address _registry,
    address _creatorToken,
    string calldata _name,
    string calldata _symbol,
    string calldata _thesis,
    address[] calldata _constituents,
    uint256[] calldata _targetWeightsBps,
    bool _rebalancingEnabled,
    uint256 _driftThresholdBps,
    address _creator
) external;
```

Called exactly once immediately after proxy deployment. Sets all storage fields and marks `_initialized = true`.

**Core functions:**

```solidity
// Deposit USDG into the basket.
// Collects management fee on the deposit amount first.
// Buys each constituent in target-weight proportions using the DEX router.
// Mints basket tokens to receiver proportional to their contribution to total basket value.
// First depositor receives tokens at a fixed 1 USDG : 1 token ratio.
function deposit(
    uint256 usdgAmount,
    uint256 minBasketTokensOut,
    address receiver
) external nonReentrant returns (uint256 basketTokensMinted);

// Redeem basket tokens for USDG.
// Collects management fee on the redemption amount first.
// Burns basket tokens.
// Sells proportional underlying holdings through DEX router.
// Returns net USDG to receiver.
function redeem(
    uint256 basketTokenAmount,
    uint256 minUsdgOut,
    address receiver
) external nonReentrant returns (uint256 usdgReturned);

// Rebalance the basket to restore target weights.
// Reverts if rebalancingEnabled is false.
// Reverts if no constituent's drift exceeds driftThresholdBps.
// Permissionless: anyone can call. Protocol automation calls it when funded.
// Sells overweight constituents, buys underweight ones via DEX router.
function rebalance(uint256[] calldata minAmountsOut) external nonReentrant;

// Collect accrued management fees.
// Called internally at the start of deposit and redeem.
// Can also be called externally by anyone to trigger a fee collection.
// Distributes fee USDG to protocol treasury and creator token revenue pool.
function collectFees() external nonReentrant;

// View: current total basket value in USDG (sum of constituent balances * Chainlink prices)
function totalValueUsdg() external view returns (uint256);

// View: current value of one basket token in USDG
function navPerToken() external view returns (uint256);

// View: current weight of each constituent in bps based on live Chainlink prices
function currentWeightsBps() external view returns (uint256[] memory);

// View: maximum drift across all constituents
function maxDrift() external view returns (uint256);

// View: whether any constituent currently exceeds drift threshold
function needsRebalancing() external view returns (bool);

// View: full basket state in one call (for frontend)
function basketState() external view returns (
    address[] memory constituentsOut,
    uint256[] memory targetWeightsOut,
    uint256[] memory currentWeightsOut,
    uint256[] memory balancesOut,
    uint256 totalValueOut,
    uint256 navOut,
    bool rebalancingEnabledOut,
    uint256 driftThresholdBpsOut,
    uint256 maxDriftOut
);
```

---

## 7. Fee Collection Mechanics

The management fee is charged as a percentage of USDG on each deposit and each redemption rather than as a time-based AUM dilution. This avoids the complexity of minting new basket tokens over time and keeps the fee logic transparent and predictable.

On every deposit:

```
fee_usdg = deposit_usdg * managementFeeBps / 10_000
net_deposit_usdg = deposit_usdg - fee_usdg
protocol_cut = fee_usdg * protocolShareBps / 10_000
creator_cut = fee_usdg - protocol_cut

transfer protocol_cut → registry.protocolTreasury
call creatorToken.snapshotRevenue(creator_cut) with USDG transfer
buy constituents using net_deposit_usdg only
mint basket tokens based on net_deposit_usdg contribution
```

On every redemption:

```
gross_usdg_value = basket_tokens_being_redeemed * navPerToken
sell constituents proportional to gross_usdg_value
fee_usdg = gross_usdg_value * managementFeeBps / 10_000
net_usdg = gross_usdg_value - fee_usdg
protocol_cut = fee_usdg * protocolShareBps / 10_000
creator_cut = fee_usdg - protocol_cut

transfer protocol_cut → registry.protocolTreasury
call creatorToken.snapshotRevenue(creator_cut) with USDG transfer
transfer net_usdg → receiver
burn basket tokens
```

The fee does not compound. Every deposit and redemption pays the flat fee once on that transaction. Users who hold basket tokens without transacting pay no fee until they redeem.

---

## 8. Basket Token NAV and Minting Math

**Total basket value:**

```
totalValueUsdg = sum over all constituents:
    constituentBalances[i] * chainlinkPrice(constituents[i]) / 1e8
```

Chainlink prices for stock feeds are 8-decimal. Constituent balances are 18-decimal (standard ERC-20). The result is in USDG 6-decimal terms. All intermediate multiplications use `uint256` with no overflow risk at realistic portfolio sizes.

**NAV per token:**

```
navPerToken = totalValueUsdg * 1e18 / basketToken.totalSupply()
```

Expressed in USDG per basket token with 18 decimal precision.

**Basket tokens minted on deposit:**

```
// After fee deduction, net_deposit_usdg is available to invest.
// If total supply is zero (first depositor):
basket_tokens_minted = net_deposit_usdg * 1e12
// (converts 6-decimal USDG to 18-decimal basket token, establishing 1:1 initial price)

// If total supply is non-zero:
basket_tokens_minted = net_deposit_usdg * 1e12 * totalSupply / totalValueUsdg
```

The first depositor establishes a 1:1 ratio of 1 USDG to 1 basket token. All subsequent depositors are priced against the current NAV, so their share of the pool is proportional to their contribution relative to the existing pool value. There is no first-depositor donation attack risk here because the basket immediately deploys deposits into constituent purchases rather than holding USDG, so directly transferring constituent tokens to the basket without going through `deposit()` would change the basket value but all existing holders benefit proportionally from that donation. A malicious actor gains nothing from it.

**Constituent purchase proportions on deposit:**

```
// For each constituent i:
usdg_to_spend_on_i = net_deposit_usdg * targetWeightsBps[i] / 10_000
constituent_tokens_received = swapRouter.swapExactUSDGForToken(
    constituents[i],
    usdg_to_spend_on_i,
    minAmountsOut[i]   // caller-specified slippage protection
)
constituentBalances[i] += constituent_tokens_received
```

**Current weight calculation:**

```
// For each constituent i:
value_i = constituentBalances[i] * chainlinkPrice(constituents[i]) / 1e8
currentWeightBps[i] = value_i * 10_000 / totalValueUsdg
drift_i = abs(currentWeightBps[i] - targetWeightsBps[i])
```

**Rebalancing logic:**

```
// Identify overweight and underweight constituents
target_value_i = totalValueUsdg * targetWeightsBps[i] / 10_000
delta_i = current_value_i - target_value_i

// Positive delta: overweight → sell delta_i USDG worth of constituent i
// Negative delta: underweight → buy |delta_i| USDG worth of constituent i

// Sell all overweight positions first, accumulate USDG
// Then use accumulated USDG to buy all underweight positions
// This avoids needing to know trade ordering in advance
```

All rounding truncates toward zero. Residual USDG from rounding differences stays in the basket and is distributed proportionally on the next redemption.

---

## 9. CreatorToken (ERC-7641)

One creator token contract is deployed per basket. The creator token is an ERC-20 with a fixed initial supply of 1,000,000 units (18 decimals), all minted to the basket creator at deployment. It implements ERC-7641 to enable a revenue sharing claim mechanism against a USDG revenue pool.

```solidity
contract CreatorToken is ERC20, IERC7641 {
    address public immutable basket;
    address public immutable usdg;

    // Snapshot tracking
    uint256 public snapshotCount;
    // snapshotId → total USDG added to pool at that snapshot
    mapping(uint256 => uint256) public snapshotRevenue;
    // snapshotId → total creator token supply at snapshot
    mapping(uint256 => uint256) public snapshotSupply;
    // user → snapshotId → bool (whether they already claimed)
    mapping(address => mapping(uint256 => bool)) public claimed;

    // Total supply is fixed at deployment and never changes
    uint256 public constant INITIAL_SUPPLY = 1_000_000 * 1e18;

    constructor(address _basket, address _usdg, address _creator) {
        basket = _basket;
        usdg = _usdg;
        _mint(_creator, INITIAL_SUPPLY);
    }

    // Called by the basket when it distributes creator's share of management fee.
    // Receives USDG from the basket and records a new snapshot.
    // Only callable by the associated basket contract.
    function snapshotRevenue(uint256 usdgAmount) external onlyBasket;

    // ERC-7641: returns USDG claimable by account for a given snapshot
    function claimableRevenue(address account, uint256 snapshotId)
        external view returns (uint256);

    // ERC-7641: claims USDG for a given snapshot
    // Proportional to account's creator token balance at snapshot time
    function claim(uint256 snapshotId) external;

    // Convenience: claim all unclaimed snapshots up to current
    function claimAll() external;
}
```

The revenue sharing formula for any given snapshot:

```
claimable = snapshotRevenue[snapshotId] * balanceAtSnapshot / snapshotSupply[snapshotId]
```

Since creator token supply is fixed and never changes, `snapshotSupply[snapshotId]` is always `INITIAL_SUPPLY`. The balance at snapshot time is the holder's balance at the time of the snapshot call, recorded using a block-level snapshot similar to ERC-20Votes snapshot tracking.

If the creator sells half their creator tokens to an investor, all subsequent snapshots distribute 50% to the creator and 50% to the investor. Past snapshots that were already created but not yet claimed distribute based on the balance at the time of that specific snapshot. This is the correct and fair behaviour because whoever held the token at the moment the revenue was recorded owns that revenue.

---

## 10. DEX Router Abstraction

All token swap operations go through a single router interface so that the underlying DEX can be changed without modifying basket contracts.

```solidity
interface IWeaveRouter {
    // Swap exact USDG in for at least minTokenOut of token
    function swapExactUSDGForToken(
        address token,
        uint256 usdgIn,
        uint256 minTokenOut,
        address recipient
    ) external returns (uint256 tokenOut);

    // Swap exact tokenIn for at least minUsdgOut USDG
    function swapExactTokenForUSDG(
        address token,
        uint256 tokenIn,
        uint256 minUsdgOut,
        address recipient
    ) external returns (uint256 usdgOut);

    // Swap exact tokenIn for at least minOut of tokenOut (for rebalancing between constituents)
    function swapExactTokenForToken(
        address tokenIn,
        address tokenOut,
        uint256 amountIn,
        uint256 minOut,
        address recipient
    ) external returns (uint256 amountOut);

    // View: quote for USDG → token without executing
    function quoteUSDGForToken(address token, uint256 usdgIn)
        external view returns (uint256 tokenOut);

    // View: quote for token → USDG without executing
    function quoteTokenForUSDG(address token, uint256 tokenIn)
        external view returns (uint256 usdgOut);
}
```

**MockSwapRouter for testnet.** Since Robinhood Chain testnet has no DEX with real liquidity, the MockSwapRouter simulates swaps at Chainlink oracle prices with zero slippage. It holds a treasury of test USDG and test stock tokens funded by the team. When a basket calls `swapExactUSDGForToken`, the mock reads the Chainlink price and transfers the corresponding token amount from its treasury. This allows the full deposit and rebalancing flow to function on testnet without real DEX liquidity. The mock is explicitly documented as a testnet component and the registry swap router address is updated to the real DEX router on mainnet.

---

## 11. WeaveAutomation

Chainlink Automation compatible contract that monitors all rebalancing-enabled baskets.

```solidity
contract WeaveAutomation is AutomationCompatibleInterface {
    address public immutable registry;
    uint256 public batchSize;     // max baskets to check per upkeep call, default 50

    // checkUpkeep runs off-chain on Chainlink keeper nodes, no gas cost to protocol
    function checkUpkeep(bytes calldata checkData)
        external view override
        returns (bool upkeepNeeded, bytes memory performData)
    {
        address[] memory allBaskets = IWeaveRegistry(registry).getAllBaskets();
        address[] memory toRebalance = new address[](allBaskets.length);
        uint256 count = 0;

        for (uint256 i = 0; i < allBaskets.length && count < batchSize; i++) {
            IBasket basket = IBasket(allBaskets[i].basket);
            if (
                basket.rebalancingEnabled() &&
                !basket.suspended() &&
                basket.totalValueUsdg() >= IWeaveRegistry(registry).minAUMForAutomation() &&
                basket.needsRebalancing()
            ) {
                toRebalance[count] = address(basket);
                count++;
            }
        }

        upkeepNeeded = count > 0;
        performData = abi.encode(toRebalance, count);
    }

    // performUpkeep runs on-chain only when checkUpkeep returned true
    function performUpkeep(bytes calldata performData) external override {
        (address[] memory baskets, uint256 count) =
            abi.decode(performData, (address[], uint256));

        for (uint256 i = 0; i < count; i++) {
            IBasket basket = IBasket(baskets[i]);
            // Double-check on-chain before executing, stale perform data protection
            if (basket.rebalancingEnabled() && basket.needsRebalancing()) {
                uint256[] memory minAmounts = new uint256[](basket.constituents().length);
                // minAmounts all zero: protocol-initiated rebalancing accepts oracle-priced execution
                // Slippage protection still comes from MockSwapRouter/real router behaviour
                basket.rebalance(minAmounts);
            }
        }
    }
}
```

The Chainlink Automation upkeep is registered once by the protocol and funded from the protocol treasury. The `batchSize` parameter prevents the `checkUpkeep` off-chain simulation from exceeding Chainlink's check gas limit. As the number of baskets grows, additional upkeep jobs can be registered each covering a different index range of the baskets array, with `checkData` encoding which slice each job covers.

Baskets below `minAUMForAutomation` are excluded from the automated job but can still be rebalanced by any caller directly. Investors in small baskets who want their weights maintained can call `basket.rebalance()` themselves and pay the gas.

---

## 12. Edge Cases and Their Handling

**Constituent becomes inactive.** Governance calls `registry.deactivateAsset(token)`. The basket is flagged as `suspended = true` by the next interaction that reads the asset config. Suspended baskets cannot accept new deposits. Existing holders can still redeem. Rebalancing is suspended. The basket can be reactivated by governance if the asset is restored, or a migration path can be built where the creator deploys a new basket excluding the deactivated constituent. There is no forced unwinding.

**All Chainlink prices are valid but one feed is stale.** Any operation that reads the stale feed's price reverts with `StalePrice(address token)`. The specific feed address is included in the error so the caller knows which asset is problematic. Operations that only read other feeds can still proceed if no stale feed is involved, but any valuation of total basket value necessarily reads all feeds and therefore reverts if any is stale.

**Deposit when DEX has insufficient liquidity for one constituent.** The DEX router call reverts. The entire deposit transaction reverts. The user's USDG is returned. The basket state is unchanged. This is the correct safe failure mode. The user should retry when liquidity improves or reduce their deposit size to stay within available liquidity depth.

**Rebalancing trades result in dust.** After a rebalancing sequence, rounding means the basket may hold tiny USDG residuals that could not be fully deployed into constituent positions. These residuals accumulate in the basket contract as USDG. On the next deposit, this USDG is included in the basket's constituent purchase calculation. It is never stuck.

**Creator transfers or sells creator token.** The ERC-7641 snapshot mechanism tracks balances at snapshot time. If the creator transfers creator tokens between two snapshots, the new holder earns revenue from all future snapshots where they hold the tokens. Past unclaimed snapshots remain claimable by whoever held the tokens at those snapshot times. There is no economic leakage in either direction.

**Two baskets with identical compositions.** Allowed. There is no duplication prevention. Both baskets compete for capital on their own merits. Their basket tokens are different contracts with different addresses and different performance histories. Investors choose between them based on creator reputation, age, track record, and any weight differences. This is the correct market behaviour.

**First depositor minimum not met.** The factory reverts if `initialDepositUsdg < registry.minFirstDepositUsdg`. The creator must fund the basket above the minimum at launch. This ensures the basket has a meaningful NAV from the start and prevents economically trivial baskets from being listed.

**Rebalancing-enabled basket falls below minAUM after initial deployment.** It falls out of the automated Chainlink job eligibility check. Rebalancing continues to be possible permissionlessly but is no longer triggered automatically. If AUM recovers above the minimum, the basket is automatically re-included in the next checkUpkeep iteration.

**Stock split where Robinhood adjusts token supply.** If Robinhood doubles the token supply in a 2:1 split, the basket's constituent balance doubles automatically (since the basket holds the tokens and split credits all holders). The Chainlink price halves simultaneously to reflect the split. The product `balance * price` remains constant, so `totalValueUsdg` and all NAV calculations are unaffected. No special handling is needed.

**Stock split where Robinhood adjusts price without touching supply.** Same result: the basket's token count is unchanged, the Chainlink price halves, the product is unchanged.

**Basket with zero rebalancing threshold.** The factory rejects this. Zero threshold would mean any movement at all triggers rebalancing, which is operationally impossible and economically destructive. The minimum threshold is validated at basket creation.

**Fee collection when basket has zero AUM.** If a basket has no deposits, `totalValueUsdg = 0` and `fee_usdg = 0`. No USDG moves. No snapshot is created on the creator token. This is correct and requires no special handling.

**Reentrancy through ERC-20 token callbacks.** All state-mutating basket functions use `ReentrancyGuard`. External calls to token contracts and the DEX router are always the last operation after all state changes. The checks-effects-interactions pattern is enforced strictly.

---

## 13. Backend Service Architecture

The backend is a Go service with SQLite storage. It holds no private keys, submits no transactions, and does not interact with any protocol contract in a write capacity. It is read-only infrastructure for the frontend and the AI agent.

### 13.1 Database Schema

```sql
CREATE TABLE baskets (
    address TEXT PRIMARY KEY,
    creator_token_address TEXT NOT NULL,
    creator_address TEXT NOT NULL,
    name TEXT NOT NULL,
    symbol TEXT NOT NULL,
    thesis TEXT NOT NULL,
    rebalancing_enabled INTEGER NOT NULL,
    drift_threshold_bps INTEGER,
    created_at INTEGER NOT NULL,
    created_tx TEXT NOT NULL,
    suspended INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE basket_constituents (
    basket_address TEXT NOT NULL,
    stock_address TEXT NOT NULL,
    symbol TEXT NOT NULL,
    target_weight_bps INTEGER NOT NULL,
    display_order INTEGER NOT NULL,
    PRIMARY KEY (basket_address, stock_address)
);

CREATE TABLE deposits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address TEXT NOT NULL,
    investor_address TEXT NOT NULL,
    usdg_amount TEXT NOT NULL,
    basket_tokens_minted TEXT NOT NULL,
    fee_usdg TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    tx_hash TEXT NOT NULL
);

CREATE TABLE redemptions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address TEXT NOT NULL,
    investor_address TEXT NOT NULL,
    basket_tokens_burned TEXT NOT NULL,
    usdg_returned TEXT NOT NULL,
    fee_usdg TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    tx_hash TEXT NOT NULL
);

CREATE TABLE rebalances (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address TEXT NOT NULL,
    triggered_by TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    tx_hash TEXT NOT NULL
);

CREATE TABLE fee_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address TEXT NOT NULL,
    snapshot_id INTEGER NOT NULL,
    usdg_amount TEXT NOT NULL,
    timestamp INTEGER NOT NULL,
    tx_hash TEXT NOT NULL
);

CREATE TABLE supported_assets (
    address TEXT PRIMARY KEY,
    symbol TEXT NOT NULL,
    name TEXT NOT NULL,
    sector TEXT NOT NULL,
    oracle_address TEXT NOT NULL,
    is_active INTEGER NOT NULL DEFAULT 1,
    added_at INTEGER NOT NULL
);

CREATE TABLE price_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    stock_address TEXT NOT NULL,
    price_usdg TEXT NOT NULL,
    timestamp INTEGER NOT NULL
);

CREATE TABLE nav_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address TEXT NOT NULL,
    nav_per_token TEXT NOT NULL,
    total_value_usdg TEXT NOT NULL,
    timestamp INTEGER NOT NULL
);
```

### 13.2 Indexer

The indexer subscribes to all relevant contract events using `ethclient` in Go with an `ethereum.FilterQuery` built from the WeaveRegistry address and all basket addresses. It processes events in order and writes to SQLite. Events indexed:

```
BasketCreated(address basket, address creatorToken, address creator, string name, bool rebalancingEnabled)
Deposited(address indexed basket, address indexed investor, uint256 usdgAmount, uint256 basketTokensMinted, uint256 feeUsdg)
Redeemed(address indexed basket, address indexed investor, uint256 basketTokensBurned, uint256 usdgReturned, uint256 feeUsdg)
Rebalanced(address indexed basket, address indexed triggeredBy)
FeeSnapshot(address indexed basket, uint256 snapshotId, uint256 usdgAmount)
AssetAdded(address indexed token, string symbol, string sector)
AssetDeactivated(address indexed token)
BasketSuspended(address indexed basket)
```

The indexer maintains a cursor of the last processed block in SQLite and resumes from that block on restart. It uses the Alchemy RPC endpoint for Robinhood Chain.

A separate price polling goroutine reads Chainlink price feeds every 60 seconds for all active supported assets and writes to the `price_history` table. A NAV computation goroutine reads current prices and basket constituent balances every 5 minutes and writes to `nav_history`. These are best-effort background processes and never block API responses.

### 13.3 API Endpoints

```
GET /baskets
  Returns all baskets with current metrics including NAV, AUM, constituent count, creator

GET /baskets/:address
  Returns full basket state: constituents, weights, current weights from last price read,
  NAV history, deposit/redemption history, creator token address, rebalancing config

GET /baskets/:address/positions/:wallet
  Returns the investor's current basket token balance, current USDG value, entry cost basis
  computed from deposit history, unrealised PnL

GET /baskets/:address/performance
  Returns NAV history time series at hourly granularity for the past 30 days

GET /catalogue
  Returns all active supported assets with symbol, name, sector, current Chainlink price,
  24h price change, market cap (if available from external data source)

GET /catalogue/:address
  Returns single asset metadata

GET /prices
  Returns current Chainlink prices for all active supported assets

GET /creator-tokens/:address
  Returns creator token for a basket: holder distribution, all snapshots,
  total revenue distributed to date

POST /ai/compose
  Body: { "thesis": "natural language string" }
  Returns: { "constituents": [{address, symbol, weight_bps, rationale}],
             "overall_rationale": string, "risk_notes": string }
  This endpoint calls the AI composition engine and returns its proposal.
  The on-chain basket is NOT created here. The frontend uses this response
  to populate the basket creation form for human review before submission.

GET /positions/:walletAddress
  Computes portfolio summary from deposit/redemption event history + current NAV data.
  Returns PortfolioSummary as defined in section 5.1.

GET /creator/:walletAddress
  Returns all baskets created by the wallet, creator token balances,
  unclaimed snapshots, and revenue history.
  Returns CreatorDashboard as defined in section 6.1.
```

---

## 14. AI Composition Engine

The AI engine is a TypeScript service that runs alongside the Go backend. It exposes a single endpoint consumed by the Go backend's `/ai/compose` proxy route.

**Flow:**

1. Go backend receives `POST /ai/compose` with the thesis string
2. Go backend forwards the request to the TypeScript AI service with the thesis and catalogue data
3. The TypeScript AI service fetches the full catalogue from the Go backend database including symbol, name, sector, and current price for every active asset
4. TypeScript service constructs a prompt for the OpenAI API
5. OpenAI API returns a JSON proposal
6. TypeScript service validates the proposal (weights sum to 10,000, all addresses exist in catalogue, min weights respected)
7. Validated proposal is returned to Go backend which returns it to the frontend

**System prompt to Anthropic API:**

```
You are a financial analyst building thematic equity baskets from a specific catalogue of
tokenized stocks. You will receive a natural language investment thesis and a JSON catalogue
of available stocks with their symbols, names, sectors, and current prices.

Your task is to select 3 to 12 stocks from the catalogue that best represent the thesis,
assign each a target weight in basis points that sum to exactly 10,000, and provide a
one-sentence rationale for each inclusion.

Rules:
- Only select stocks that appear in the provided catalogue
- Weights must be integers and must sum to exactly 10,000
- No single weight may be less than 100 (1%) or more than 5000 (50%)
- Do not include more than 12 constituents
- Do not include fewer than 3 constituents
- Prefer direct plays over indirect beneficiaries unless the thesis specifically calls for breadth
- Weight by conviction and relevance to the thesis, not by market cap alone

Return ONLY valid JSON matching this schema, no preamble or explanation outside the JSON:
{
  "constituents": [
    {
      "address": "0x...",
      "symbol": "AAPL",
      "weight_bps": 2000,
      "rationale": "one sentence explaining why this stock fits the thesis"
    }
  ],
  "overall_rationale": "two to three sentences explaining the basket's overall construction logic",
  "risk_notes": "one to two sentences noting the key risks or concentration exposures"
}
```

The TypeScript service parses the response, validates all fields, and rejects any response that does not conform to the schema or violates the weight rules. If the Anthropic API returns invalid JSON or an invalid proposal, the service retries once with a prompt reminder about the JSON requirement before returning an error to the frontend.

---

## 15. External Integrations

**Chainlink Data Feeds.** Every supported stock has a Chainlink price feed address stored 
in `registry.assets[token].chainlinkFeed`. All price reads call `latestRoundData()` which 
returns `(roundId, answer, startedAt, updatedAt, answeredInRound)`. The `answer` field is 
the price in 8-decimal USD. The `updatedAt` field is validated against 
`block.timestamp - registry.oracleStalenessSecs` before any value is used. On testnet, 
`MockOracle` contracts implementing `IWeaveOracle` substitute for real Chainlink feeds since 
Chainlink has not yet published feed addresses for Robinhood Chain testnet. The registry 
stores the oracle address per asset and the basket contracts call `IWeaveOracle.latestPrice()` 
regardless of whether the underlying oracle is a `MockOracle` or a real Chainlink aggregator, 
meaning the swap to live Chainlink feeds on mainnet requires only a governance call to 
`registry.addAsset()` with the correct feed address — no contract changes needed.

**Alchemy RPC.** Alchemy is a confirmed infrastructure partner on Robinhood Chain testnet. 
The Go backend uses Alchemy's Robinhood Chain RPC endpoint for all event subscription and state reads. 
The basket contracts interact with on-chain state directly, no backend dependency.

**OpenAI API.** The AI composition engine calls `gpt-4.1-mini` via OpenAI API. 
The prompt is structured to produce JSON output deterministically. 
The engine does not stream responses and waits for the full completion before validating. 
API key is stored as an environment variable and never exposed to the frontend.

**Chainlink Automation.** The WeaveAutomation contract is registered as a custom logic upkeep on Chainlink Automation. 
The upkeep is funded with LINK from the protocol treasury. 
The protocol monitors the LINK balance of the upkeep and tops it up when it falls below a threshold. 
Chainlink's docs confirm that custom logic upkeeps support `checkData` for parameterising which baskets each job monitors, 
enabling pagination across multiple jobs as the basket count grows.

---

## 16. Deployment Order

```
1. WeaveRegistry(governance, usdg, protocolTreasury, managementFeeBps, ...)
2. MockSwapRouter(registry)   ← testnet only
3. BasketImplementation()     ← logic contract, no constructor args
4. BasketFactory(registry, implementation)
5. WeaveAutomation(registry)
6. Registry.setBasketFactory(basketFactory)
7. Registry.setSwapRouter(mockSwapRouter)   ← testnet; real DEX on mainnet
8. Registry.setAutomationContract(weaveAutomation)
9. For each supported asset:
   Registry.addAsset(AssetConfig { tokenAddress, chainlinkFeed, symbol, name, sector })
10. Register WeaveAutomation as a Chainlink Automation upkeep
11. Fund the upkeep with LINK from protocol treasury
12. BasketFactory.createBasket(...)  ← first basket, protocol-seeded to demonstrate the flow
```

---

## 17. Security Considerations

**Reentrancy.** Every state-mutating basket function uses OpenZeppelin's `ReentrancyGuard`. All external calls are last in the execution sequence. The ERC-7641 `claim()` function also uses a reentrancy guard since it sends USDG to the caller.

**Oracle manipulation.** The protocol uses Chainlink decentralised price feeds, not AMM spot prices. Chainlink's multi-validator median pricing is resistant to single-block manipulation. Flash loans cannot move Chainlink prices. Stale price protection reverts any operation where the price has not been updated within the staleness threshold.

**Weight sum validation.** The factory enforces that weights sum to exactly 10,000 bps at basket creation. Individual basket contracts do not re-validate this in every operation since weights are immutable after deployment. If rebalancing changes effective weights over time, that is by design and governed by the drift threshold.

**Slippage.** All DEX swaps expose a `minAmountsOut` parameter. Deposits and redemptions pass these from the caller. Protocol-initiated rebalances pass zero minimums but the MockSwapRouter on testnet executes at oracle prices, and on mainnet the real DEX has its own slippage mechanics. Governance can set a `maxRebalanceSlippageBps` parameter that the automation contract enforces when constructing the rebalance call.

**Governance key.** Two-step transfer for all governance actions. Governance is an EOA for the hackathon and should be a multisig in production.

**Creator token economics.** The creator token supply is fixed at deployment and never changes. No minting after creation is possible. Governance has no power over creator token balances or revenue distributions. This is enforced by the constructor and the absence of any minting function in the creator token contract.

**No upgradeability.** Basket contracts are not upgradeable. The implementation contract can be updated for new baskets by governance changing the factory's implementation address. Existing basket proxies continue using the implementation they were deployed against. If a critical bug is found in the implementation, governance can deploy a new implementation and all new baskets use it, while existing baskets continue operating safely on the old one.
