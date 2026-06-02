# Weave — Mainnet Migration TODO

Items on this list are testnet-specific decisions that must be revisited before
mainnet deployment. Nothing here is a bug or a design flaw but each entry is a
deliberate testnet simplification with a documented mainnet path.

---

## Contracts

### OracleAdapter.sol → direct Chainlink integration in WeaveRegistry
**Testnet:** OracleAdapter.sol implements IWeaveOracle with manual setPrice.

**Mainnet path:** Update WeaveRegistry.getAssetPrice() to call
AggregatorV3Interface(cfg.oracle).latestRoundData() directly. Store the
Chainlink aggregator address in AssetConfig.oracle. Deploy zero additional
contracts per asset — just register each stock with its Chainlink aggregator
address via registry.addAsset(). IWeaveOracle interface becomes testnet-only.

**Action:** Update WeaveRegistry.getAssetPrice() before mainnet. Remove
IWeaveOracle dependency from registry.

---

### SwapRouter.sol → UniswapV3DEXAdapter.sol (or equivalent)
**Testnet:** `SwapRouter.sol` implements `IWeaveRouter` using a funded token
treasury and executes swaps at oracle price with a configurable spread.

**Mainnet path:** Deploy a DEX adapter implementing `IWeaveRouter` that routes
swaps through whatever DEX has tokenized stock liquidity on Robinhood Chain
mainnet (Uniswap v3, Curve, or a Robinhood-native DEX). Update registry via
`registry.setSwapRouter(newAdapter)`. No changes to any other contract.

**Action:** Confirm which DEX has tokenized stock liquidity on Robinhood Chain
mainnet once announced. Implement and audit the adapter. Update registry.

---

### WeaveRegistry — governance EOA → multisig
**Testnet:** Governance is the deployer EOA for simplicity.

**Mainnet path:** Transfer governance to a Gnosis Safe multisig with a
meaningful threshold (e.g. 3-of-5) before any TVL accumulates.

**Action:** Deploy multisig. Call `nominateGovernance(multisig)` from EOA.
Call `acceptGovernance()` from multisig.

---

### WeaveRegistry — oracleStalenessSecs
**Testnet:** Set to 86400 (24 hours) because MockOracle only updates when
`setPrice` is called manually.

**Mainnet path:** Real Chainlink feeds update every few seconds. Reduce to
3600 (1 hour) or less depending on the feed's heartbeat.

**Action:** Check Chainlink heartbeat for each stock feed on Robinhood Chain
mainnet. Set `oracleStalenessSecs` via governance accordingly.

---

### Deploy script — price constants
**Testnet:** Initial oracle prices are read from `.env` at deploy time and
set via `OracleAdapter` constructor.

**Mainnet path:** Not applicable — `WeaveRegistry.getAssetPrice()` calls
Chainlink `AggregatorV3Interface.latestRoundData()` directly using the
aggregator address stored in `AssetConfig.oracle`. Prices are live from
block one. No `OracleAdapter` contracts deployed. No initial price needed
in the deploy script — just pass the Chainlink aggregator address as the
`oracle` field in each `registry.addAsset()` call.

**Action:** Replace `OracleAdapter` deploy steps in `Deploy.s.sol` with
direct Chainlink aggregator addresses per stock. Remove price env vars.

---

### WeaveAutomation — minAmounts zero in performUpkeep
**Testnet:** `performUpkeep` passes zero `minAmountsOut` to `rebalance()`.
Acceptable because `SwapRouter` executes at oracle price.

**Mainnet path:** Compute meaningful `minAmountsOut` from oracle prices minus
`maxRebalanceSlippageBps` before calling `rebalance()`. This prevents sandwich
attacks on automation-triggered rebalances.

**Action:** Implement slippage calculation in `performUpkeep` once
`maxRebalanceSlippageBps` is added to `WeaveAutomation` (planned in phase 1).

---

## Backend

### Indexer — backfill uses public RPC
**Testnet:** Alchemy free tier blocks wide `eth_getLogs` ranges. Backfill uses
the public Robinhood Chain RPC which has no range limit.

**Mainnet path:** Use Alchemy paid tier or another provider that supports
unlimited log range for reliable backfill.

**Action:** Upgrade Alchemy plan or switch provider for mainnet indexer.

---

## Infrastructure

### Railway SQLite → managed Postgres
**Testnet:** SQLite on a Railway persistent volume.

**Mainnet path:** Migrate to Postgres on Railway or a managed provider for
concurrent write safety, point-in-time recovery, and read replicas.

**Action:** Migrate schema and queries. SQLite-specific syntax (`INSERT OR
IGNORE`) needs review for Postgres compatibility.
