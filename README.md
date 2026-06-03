# Weave — Onchain Index Protocol for Tokenized Equities

> Built during the Arbitrum Open House London: Online Buildathon, 2026
>
> **Team:** Yemi (Ikeh Chukwuka Favour) — OCD Labs

Weave lets anyone compose a thematic basket of tokenized stocks from Robinhood Chain's catalogue, publish it as a single investable onchain instrument, and earn a continuous share of the revenue that basket generates for as long as other investors hold it. Investors get instant diversified exposure to any equity theme through a single token that rebalances itself automatically, usable anywhere in DeFi as collateral or a transfer of value. See [project description](./docs/ProjectDescription.md) for more detailed information.

---

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Smart Contracts](#smart-contracts)
- [Go Backend](#go-backend)
- [AI Composition Engine](#ai-composition-engine)
- [API Reference](#api-reference)
- [Local Development](#local-development)
- [Deployment](#deployment)
- [Environment Variables](#environment-variables)

---

## Overview

### The Problem

Every thematic index that exists today was designed by a financial institution, packaged as an ETF, and sold to retail investors who had no say in its composition and no share in its economics. There is no mechanism for expressing a precise investment thesis as a composable financial instrument without institutional resources, regulatory approvals, and fund management infrastructure.

### The Solution

Weave is a protocol that turns that raw material into a creator economy for financial products:

- **Anyone can create a basket** — describe a thesis in plain language, the AI engine researches the catalogue and proposes a composition, you review and deploy it onchain.
- **Creators earn perpetually** — every basket generates a management fee. 80% of that fee flows continuously to the basket creator through ERC-7641 revenue sharing.
- **Investors get DeFi-native exposure** — basket tokens are standard ERC-20s usable as collateral, transferable, and redeemable at any time at current NAV.
- **Rebalancing is a design choice** — creators choose at launch whether their basket maintains target weights automatically via Chainlink Automation or lets winners run freely.

---

## Architecture

### Component Architecture

<p align="center">
  <img src="https://i.imgur.com/4eTppzW.png" alt="weave-system-components-architecture" />
</p>


### Request Sequence

<p align="center">
  <img src="https://i.imgur.com/I13zZQA.png" alt="weave-sequence-diagram" />
</p>

**Three layers:**

| Layer | Technology | Responsibility |
|---|---|---|
| Contracts | Solidity 0.8.24, Foundry | Basket deployment, deposits, redemptions, rebalancing, fee distribution, ERC-7641 revenue sharing |
| Backend | Go 1.22, SQLite | Event indexing, price polling, NAV computation, HTTP API, AI composition engine |
| Automation | Chainlink Automation | Continuous drift monitoring and permissionless rebalancing |

---

## Smart Contracts

### Overview

All contracts are deployed on **Robinhood Chain Testnet (chain ID 46630)** and verified on [Blockscout](https://explorer.testnet.chain.robinhood.com).

| Contract | Purpose |
|---|---|
| `WeaveRegistry` | Single source of truth for protocol config, asset catalogue, basket index, and governance |
| `BasketFactory` | Deploys basket proxies and creator tokens atomically via ERC-1167 clone pattern |
| `BasketImplementation` | Shared logic for all basket proxies. Also the ERC-20 basket token |
| `CreatorToken` | ERC-7641 revenue share token. One per basket. Fixed supply of 1,000,000 units |
| `WeaveAutomation` | Chainlink Automation compatible upkeep. Monitors all rebalancing-enabled baskets |
| `SwapRouter` | Testnet DEX adapter. Executes swaps at oracle prices with a configurable spread |
| `OracleAdapter` | Testnet price oracle implementing `IWeaveOracle`. Drop-in replaceable with Chainlink feeds on mainnet |

### Key Design Decisions

**ERC-1167 minimal proxy pattern.** Every basket is a proxy clone pointing to a shared `BasketImplementation` logic contract. Deploying a basket costs ~300k gas instead of 3M+ gas for a full deployment. The storage layout in `BasketImplementation` is fixed and documented — never reorder fields between deployments.

**ERC-7641 creator revenue sharing.** The creator token implements ERC-7641 (Draft) with USDG as the revenue token. Revenue snapshots are triggered automatically by the basket on every fee collection. Holders claim proportional USDG at any time. The snapshot mechanism uses block-level balance checkpoints so transfers between snapshots are correctly handled — whoever held the tokens at the snapshot block owns that revenue.

**Fee mechanics.** Management fees are charged as a flat percentage on each deposit and redemption — not as a time-based AUM dilution. This keeps fee logic transparent and predictable. The fee is split 20% to the protocol treasury and 80% to the basket creator through the ERC-7641 snapshot mechanism.

**NAV pricing bug found in testing.** An early version of `deposit()` read `totalValueUsdg` after buying constituents, which caused the denominator to include the depositor's own tokens — systematically underpricing all deposits after the first. The fix snapshots `supplyBefore` and `totalValueBefore` before `_buyConstituents` runs and mints against those pre-purchase values. This is the standard and correct approach used by every production AMM and index fund.

**Oracle abstraction.** `IWeaveOracle` wraps either a real Chainlink `AggregatorV3Interface` feed or an `OracleAdapter`. Real Chainlink feed addresses for Robinhood Chain testnet stock tokens are not yet published — the registry's `oracle` field per asset is set to `OracleAdapter` contracts on testnet and will be updated via governance once Chainlink publishes addresses. No contract changes required.

**Protocol pause.** Governance can freeze all basket deposits and redemptions instantly via `registry.pauseAll()`. Rebalancing is deliberately not paused — reducing drift during a crisis is safe and desirable.

**Per-leg slippage protection.** Each constituent swap during a deposit is protected against oracle deviation using `registry.maxSwapSlippageBps`. Automation-triggered rebalance sell legs use `WeaveAutomation.maxRebalanceSlippageBps`. This protects against sandwich attacks on mainnet.

### Building and Testing

```bash
# Install dependencies
make install

# Compile
make build

# Run full test suite
make test

# Verbose output for debugging
make test-v

# Gas snapshot
make snapshot
```

### Deployment

```bash
# Set your environment variables
cp .env.example .env
# Fill in PRIVATE_KEY, RPC_URL, ALCHEMY_WS_URL

# Deploy all contracts to Robinhood Chain Testnet
make deploy

# Set oracle prices
make update-prices

# Fund the SwapRouter treasury
make fund-router

# Verify on Blockscout
make verify ADDR=0x... NAME=src/WeaveRegistry.sol:WeaveRegistry
```

The deployment script deploys in this order:
1. `WeaveRegistry`
2. `OracleAdapter` × 5 (one per stock, with initial prices)
3. `SwapRouter`
4. `BasketImplementation`
5. `BasketFactory`
6. `WeaveAutomation`
7. Wires everything into the registry
8. Registers all 5 stock assets

---

## Go Backend

The backend is a Go service with SQLite storage. It is read-only infrastructure — it holds no private keys, submits no transactions, and does not interact with any protocol contract in a write capacity. The AI composition engine runs inside this process.

### Components

**Indexer** (`server/indexer/indexer.go`)

On startup, syncs current chain state by calling `registry.getSupportedAssets()` and `registry.getAllBaskets()` directly via RPC, then seeds full basket metadata via `basketState()` calls. Scans historical events from the deploy block using a cursor stored in SQLite. Then subscribes to live events via Alchemy WebSocket. Events indexed:

- `BasketCreated` — registers new baskets with full composition data
- `Deposited` / `Redeemed` — investor activity
- `Rebalanced` — rebalancing history
- `RevenueSnapshoted` — creator fee distributions
- `AssetAdded` / `AssetDeactivated` — catalogue changes
- `Suspended` — basket suspension

**Price Poller** (`server/prices/prices.go`)

Reads `IWeaveOracle.latestRoundData()` for every asset that is a constituent of at least one active basket, every 60 seconds. Assets not held by any basket are not polled.

**NAV Poller** (`server/nav/nav.go`)

Calls `navPerToken()` and `totalValueUsdg()` on every non-suspended basket every 5 minutes via a 5-worker pool and writes to `nav_history`.

**AI Composition Engine** (`server/ai/compose.go`)

Receives a natural language thesis, reads the active catalogue from SQLite, calls OpenAI GPT-4.1-mini, validates the response against strict weight rules, and returns a structured JSON proposal. Retries once on validation failure with a stricter prompt.

**HTTP API** (`server/api/api.go`)

Eleven endpoints covering catalogue, baskets, portfolio, creator dashboard, and AI composition. Full interactive documentation at `GET /docs`.

**Database** (`server/db/`)

SQLite with WAL journal mode. WAL allows concurrent readers with a single writer, appropriate for a workload where the indexer writes continuously and HTTP handlers read simultaneously.

### Running

```bash
# Install Go dependencies
make go-deps

# Build binary
make go-build

# Run (requires .env)
make go-run
```

The backend starts at `http://localhost:8080`. Interactive API docs at `http://localhost:8080/docs`.

---

## AI Composition Engine

The AI composition engine is embedded in the Go backend (`server/ai/compose.go`). It calls **OpenAI GPT-4.1-mini** directly via the OpenAI REST API.

**Why GPT-4.1-mini?** GPT-4.1 was specifically trained for instruction-following reliability. On OpenAI's internal hard instruction eval, GPT-4.1 scores 49.1% vs GPT-4o's 29.2% — a 20-point gap that directly translates to fewer retry failures when the model must return exactly 10,000 bps summed weights with only valid catalogue addresses. GPT-4.1-mini carries most of that improvement at significantly lower cost.

### Validation

Every LLM response is validated before returning to the caller:
- All constituent addresses exist in the active catalogue
- Symbol matches the catalogue entry for each address
- No duplicate constituent addresses
- Weight sum equals exactly 10,000 bps
- 3–12 constituents
- No individual weight below 100 bps or above 5,000 bps
- Non-empty rationale for each constituent

If validation fails, the engine retries once with a stricter JSON-only prompt before returning a 502 to the frontend.

### Price Simulation

Oracle prices drift automatically on the deployed testnet deployment using a simulation script:

```bash
# Run price simulation (drifts each price ±5% from its current on-chain value)
set -a && source .env && set +a && \
  while true; do ./script/update-prices.sh || true; sleep 300; done >> /tmp/weave-prices.log 2>&1 &

# Monitor
tail -f /tmp/weave-prices.log
```

---

## API Reference

Interactive Swagger UI documentation is served by the Go backend at:

```
http://localhost:8080/docs
https://weave.up.railway.app/docs
```

The raw OpenAPI 3.0 specification is available at:

```
http://localhost:8080/openapi.json
```

### Quick Reference

| Method | Path | Description |
|---|---|---|
| `GET` | `/catalogue` | All active tokenized stock assets with oracle prices |
| `GET` | `/catalogue/:address` | Single asset by token address |
| `GET` | `/prices` | Latest oracle prices for constituent assets |
| `GET` | `/baskets` | All published baskets with NAV and AUM |
| `GET` | `/baskets/:address` | Full basket detail |
| `GET` | `/baskets/:address/performance` | NAV history time series |
| `GET` | `/baskets/:address/positions/:wallet` | Investor position in a basket |
| `GET` | `/positions/:wallet` | All positions for a wallet |
| `GET` | `/creator/:wallet` | Creator dashboard with claimable revenue |
| `GET` | `/creator-tokens/:address` | Creator token revenue snapshot history |
| `POST` | `/ai/compose` | AI basket composition from natural language thesis |

### Decimal Conventions

All token amounts are raw `uint256` strings to avoid JavaScript precision loss:

| Asset | Decimals | Raw example | Display |
|---|---|---|---|
| USDG | 6 | `"100000000"` | $100.00 |
| Basket tokens | 18 | `"1000000000000000000"` | 1.0 |
| Oracle prices | 8 | `"49701000000"` | $497.01 |
| Weights | bps | `5000` | 50% |

---

## Local Development

### Prerequisites

- [Foundry](https://getfoundry.sh/) — Solidity toolchain
- Go 1.22+
- SQLite3 (`sudo apt-get install sqlite3` on Ubuntu)
- An Alchemy account with a Robinhood Chain Testnet app
- An OpenAI API key

### First-Time Setup

```bash
# Clone
git clone git@github.com:OCD-Labs/Weave.git
cd Weave

# Install Forge dependencies
make install

# Install Go dependencies
make go-deps

# Copy environment template
cp .env.example .env
# Fill in: PRIVATE_KEY, RPC_URL, ALCHEMY_WS_URL, OPENAI_API_KEY
```

### Getting Testnet Tokens

1. Add Robinhood Chain Testnet to MetaMask:
   - RPC: `https://rpc.testnet.chain.robinhood.com`
   - Chain ID: `46630`
   - Symbol: `ETH`
   - Explorer: `https://explorer.testnet.chain.robinhood.com`

2. Get ETH and stock tokens from the Robinhood faucet:
   `https://faucet.testnet.chain.robinhood.com`

3. Get an Alchemy API key:
   `https://dashboard.alchemy.com` → Create App → Robinhood Chain Testnet

### Running

```bash
make go-run
```

The backend starts at `http://localhost:8080`.

---

## Deployment

### Contract Deployment (Robinhood Chain Testnet)

```bash
make deploy
```

After deployment, update `.env` with the printed contract addresses, then:

```bash
make update-prices   # set initial oracle prices
make fund-router     # fund the SwapRouter treasury
```

Verify all contracts:

```bash
make verify ADDR=<REGISTRY>    NAME=src/WeaveRegistry.sol:WeaveRegistry
make verify ADDR=<ROUTER>      NAME=src/SwapRouter.sol:SwapRouter
make verify ADDR=<IMPL>        NAME=src/BasketImplementation.sol:BasketImplementation
make verify ADDR=<FACTORY>     NAME=src/BasketFactory.sol:BasketFactory
make verify ADDR=<AUTOMATION>  NAME=src/WeaveAutomation.sol:WeaveAutomation
make verify ADDR=<ORACLE_TSLA> NAME=src/OracleAdapter.sol:OracleAdapter
```

---

## Environment Variables

Copy `.env.example` to `.env` and fill in all required values.

| Variable | Required | Description |
|---|---|---|
| `PRIVATE_KEY` | Yes | Deployer wallet private key (testnet only) |
| `DEPLOYER_ADDRESS` | Yes | Deployer wallet address |
| `RPC_URL` | Yes | Robinhood Chain testnet public RPC (`https://rpc.testnet.chain.robinhood.com`) |
| `ALCHEMY_WS_URL` | Yes | Alchemy WebSocket RPC for the live event subscription |
| `WEAVE_REGISTRY_ADDRESS` | Yes (after deploy) | Deployed registry address |
| `BASKET_FACTORY_ADDRESS` | Yes (after deploy) | Deployed factory address |
| `SWAP_ROUTER_ADDRESS` | Yes (after deploy) | Deployed SwapRouter address |
| `DEPLOY_BLOCK` | Yes (after deploy) | Block number of registry deployment for indexer cursor |
| `OPENAI_API_KEY` | Yes | OpenAI API key for the AI composition engine |
| `OPENAI_MODEL` | No | Defaults to `gpt-4.1-mini` |
| `DB_PATH` | No | SQLite path. Defaults to `./server/weave.db` |
| `API_PORT` | No | Backend HTTP port. Defaults to `8080` |
| `PRICE_POLL_INTERVAL_SECS` | No | Oracle price poll interval in seconds. Defaults to `60` |
| `NAV_POLL_INTERVAL_SECS` | No | NAV computation interval in seconds. Defaults to `300` |

*Built on Robinhood Chain testnet — an Arbitrum L2 designed for tokenized real-world assets. Robinhood Chain is the only blockchain with a catalogue of tokenized equities at meaningful scale, making Weave's equity index protocol possible for the first time.*