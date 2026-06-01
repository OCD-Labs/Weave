# Weave — Onchain Index Protocol for Tokenized Equities

> Built for the Arbitrum Open House London: Online Buildathon, 2026
> 
> **Team:** Yemi (Ikeh Chukwuka Favour) — OCD Labs

Weave lets anyone compose a thematic basket of tokenized stocks from Robinhood Chain's catalogue, publish it as a single investable onchain instrument, and earn a continuous share of the revenue that basket generates for as long as other investors hold it. Investors get instant diversified exposure to any equity theme through a single token that rebalances itself automatically, usable anywhere in DeFi as collateral or a transfer of value. See [project description](./docs/ProjectDescription.md) for more detailed information

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

- **Anyone can create a basket** — describe a thesis in plain language, the AI agent researches the catalogue and proposes a composition, you review and deploy it onchain.
- **Creators earn perpetually** — every basket generates a management fee. 80% of that fee flows continuously to the basket creator through ERC-7641 revenue sharing.
- **Investors get DeFi-native exposure** — basket tokens are standard ERC-20s usable as collateral, transferable, and redeemable at any time at current NAV.
- **Rebalancing is a design choice** — creators choose at launch whether their basket maintains target weights automatically via Chainlink Automation or lets winners run freely.

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        Frontend / Wallet                     │
└──────────────────────────┬──────────────────────────────────┘
                           │
          ┌────────────────▼────────────────┐
          │         Go Backend (HTTP API)    │
          │         server/ — port 8080      │
          │  SQLite · Indexer · Price Poller │
          └────┬───────────────────┬─────────┘
               │                   │
    ┌──────────▼──────┐   ┌────────▼──────────┐
    │  TypeScript      │   │  Robinhood Chain   │
    │  AI Agent        │   │  Testnet (46630)   │
    │  agent/          │   │                    │
    │  port 3001       │   │  WeaveRegistry     │
    │                  │   │  BasketFactory     │
    │  OpenAI /        │   │  BasketProxy       │
    │  Anthropic /     │   │  CreatorToken      │
    │  Ollama          │   │  WeaveAutomation   │
    └──────────────────┘   │  MockSwapRouter    │
                           │  MockOracle ×5     │
                           └────────────────────┘
```

**Four layers:**

| Layer | Technology | Responsibility |
|---|---|---|
| Contracts | Solidity 0.8.24, Foundry | Basket deployment, deposits, redemptions, rebalancing, fee distribution, ERC-7641 revenue sharing |
| Backend | Go 1.22, SQLite | Event indexing, price polling, HTTP API, AI proxy |
| AI Agent | TypeScript, Express | Natural language thesis → basket composition via LLM |
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
| `MockSwapRouter` | Testnet DEX substitute. Executes swaps at oracle prices with zero slippage |
| `MockOracle` | Testnet price feed. Implements `IWeaveOracle`. Swappable for real Chainlink feeds on mainnet |

### Key Design Decisions

**ERC-1167 minimal proxy pattern.** Every basket is a proxy clone pointing to a shared `BasketImplementation` logic contract. Deploying a basket costs ~300k gas instead of 3M+ gas for a full deployment. The storage layout in `BasketImplementation` is fixed and documented — never reorder fields between deployments.

**ERC-7641 creator revenue sharing.** The creator token implements ERC-7641 (Draft) with USDG as the revenue token rather than ETH. Revenue snapshots are triggered automatically by the basket on every fee collection. Holders claim proportional USDG at any time. The snapshot mechanism uses block-level balance checkpoints so transfers between snapshots are correctly handled — whoever held the tokens at the snapshot block owns that revenue.

**Fee mechanics.** Management fees are charged as a flat percentage on each deposit and redemption — not as a time-based AUM dilution. This keeps fee logic transparent and predictable. The fee is split 20% to the protocol treasury and 80% to the basket creator through the ERC-7641 snapshot mechanism.

**NAV pricing bug found in testing.** An early version of `deposit()` read `totalValueUsdg` after buying constituents, which caused the denominator to include the depositor's own tokens — systematically underpricing all deposits after the first. The fix snapshots `supplyBefore` and `totalValueBefore` before `_buyConstituents` runs and mints against those pre-purchase values. This is the standard and correct approach used by every production AMM and index fund.

**Oracle abstraction.** `IWeaveOracle` wraps either a real Chainlink `AggregatorV3Interface` feed or a `MockOracle`. Real Chainlink feed addresses for Robinhood Chain testnet stock tokens are not yet published — the registry's `oracle` field per asset is set to `MockOracle` contracts on testnet and will be updated via governance once Chainlink publishes addresses. No contract changes required.

### Building and Testing

```bash
# Install dependencies
make install

# Compile
make build

# Run full test suite with fuzz testing
make test

# Verbose output for debugging
make test-v

# Gas snapshot
make snapshot
```

The test suite covers 84 test cases across all five contract files including:
- Full happy path from registry setup through basket creation, deposit, rebalance, and redemption
- First-deposit NAV initialisation
- Management fee split between protocol treasury and creator token revenue pool
- Drift threshold enforcement and rebalancing trigger logic
- Staleness revert on oracle failure
- Constituent weight validation at basket creation
- Creator token revenue claim flow across multiple snapshots with partial ownership
- Fuzz tests for fee invariants, weight sum invariants, and claimable revenue bounds

### Deployment

```bash
# Set your environment variables
cp .env.example .env
# Fill in PRIVATE_KEY, ALCHEMY_RPC_URL, ALCHEMY_WS_URL

# Deploy all contracts to Robinhood Chain Testnet
make deploy

# Verify on Blockscout
make verify ADDR=0x... NAME=src/WeaveRegistry.sol:WeaveRegistry
```

The deployment script deploys in this order:
1. `WeaveRegistry`
2. `MockOracle` × 5 (one per stock)
3. `MockSwapRouter`
4. `BasketImplementation`
5. `BasketFactory`
6. `WeaveAutomation`
7. Wires everything into the registry
8. Registers all 5 stock assets

### Funding the MockSwapRouter

After deployment, the router needs a token treasury to fill swap orders:

```bash
# Send stock tokens to the router (5 units each, 18 decimal)
cast send $TSLA_ADDRESS "transfer(address,uint256)" $MOCK_SWAP_ROUTER_ADDRESS 5000000000000000000 --rpc-url $RPC_URL --private-key $PRIVATE_KEY
# Repeat for AMZN, PLTR, NFLX, AMD

# Send USDG to the router (100 units, 6 decimal)
cast send $USDG_ADDRESS "transfer(address,uint256)" $MOCK_SWAP_ROUTER_ADDRESS 100000000 --rpc-url $RPC_URL --private-key $PRIVATE_KEY
```

---

## Go Backend

The backend is a Go service with SQLite storage. It is read-only infrastructure — it holds no private keys, submits no transactions, and does not interact with any protocol contract in a write capacity.

### Components

**Indexer** (`server/indexer/indexer.go`)

Subscribes to all registry and basket events via WebSocket (`eth_subscribe`). Processes events in order and writes to SQLite. Events indexed:

- `BasketCreated` — registers new baskets
- `Deposited` / `Redeemed` — investor activity
- `Rebalanced` — rebalancing history
- `RevenueSnapshoted` — creator fee distributions
- `AssetAdded` / `AssetDeactivated` — catalogue changes
- `Suspended` — basket suspension

On startup the indexer backfills historical events from the deployment block so restarts don't lose data.

**Price Poller** (`server/prices/prices.go`)

Reads `IWeaveOracle.latestPrice()` for every active asset every 60 seconds and writes to `price_history`. Reads asset addresses from SQLite (maintained by the indexer) rather than making a complex ABI call to the registry.

**HTTP API** (`server/api/api.go`)

Eleven endpoints covering catalogue, baskets, portfolio, creator dashboard, and AI composition proxy. Full interactive documentation available at `GET /docs`.

**Database** (`server/db/`)

SQLite with WAL journal mode. WAL allows multiple concurrent readers with a single writer, which is the correct mode for a workload where the indexer writes and the HTTP handlers read simultaneously.

### Running

```bash
# Install Go dependencies
make go-deps

# Build binary
make go-build

# Run (requires .env)
make go-run
```

The backend starts at `http://localhost:8080`. Interactive API docs are at `http://localhost:8080/docs`.

---

## AI Composition Engine

The agent is a TypeScript Express service that receives a natural language investment thesis, reads the stock catalogue from the Go backend, calls an LLM, validates the response against strict weight rules, and returns a structured JSON proposal.

### Provider Architecture

The agent uses a clean provider abstraction. Switching LLM providers requires changing one environment variable — zero code changes:

```bash
# Use OpenAI (recommended — best instruction following for structured output)
LLM_PROVIDER=openai
OPENAI_API_KEY=sk-...
OPENAI_MODEL=gpt-4.1-mini   # best cost/quality tradeoff for constrained JSON output

# Use Anthropic
LLM_PROVIDER=anthropic
ANTHROPIC_API_KEY=sk-ant-...

# Use Ollama (free, local, no API key)
LLM_PROVIDER=ollama
OLLAMA_MODEL=llama3.2
```

**Why `gpt-4.1-mini`?** GPT-4.1 was specifically trained for instruction-following reliability. On OpenAI's internal hard instruction eval, GPT-4.1 scores 49.1% vs GPT-4o's 29.2% — a 20-point gap that directly translates to fewer retry failures when the model must return exactly 10,000 bps summed weights with only valid catalogue addresses. GPT-4.1-mini carries most of that improvement at 6× lower cost than GPT-4o.

### Validation

Every LLM response is validated before returning to the caller:
- JSON schema conformance via Zod
- All constituent addresses exist in the active catalogue
- Symbol matches the catalogue entry for each address
- No duplicate constituent addresses
- Weight sum equals exactly 10,000 bps
- No individual weight below 100 bps or above 5,000 bps

If validation fails, the agent retries once with a stricter JSON-only prompt before returning an error.

### Running

```bash
# Install dependencies
make ts-install

# Typecheck
make ts-typecheck

# Run with ts-node (development)
make ts-dev

# Compile and run (production)
make ts-build && make start
```

The agent starts at `http://localhost:3001`. The Go backend proxies `/ai/compose` requests to it.

### Ollama Setup (free local LLM)

```bash
# macOS
brew install ollama
ollama pull llama3.2

# Linux
curl -fsSL https://ollama.com/install.sh | sh
ollama pull llama3.2

# Verify
curl http://localhost:11434/api/tags
```

---

## API Reference

Interactive Swagger UI documentation is served by the Go backend at:

```
http://localhost:8080/docs
```

The raw OpenAPI 3.0 specification is available at:

```
http://localhost:8080/openapi.json
```

Import the OpenAPI spec into Postman, Insomnia, or any OpenAPI-compatible tool for a full client experience.

### Quick Reference

| Method | Path | Description |
|---|---|---|
| `GET` | `/catalogue` | All active tokenized stock assets with oracle prices |
| `GET` | `/catalogue/:address` | Single asset by token address |
| `GET` | `/prices` | Latest oracle prices for all assets |
| `GET` | `/baskets` | All published baskets with NAV and AUM |
| `GET` | `/baskets/:address` | Single basket details |
| `GET` | `/baskets/:address/performance` | NAV history time series |
| `GET` | `/baskets/:address/positions/:wallet` | Investor position in a basket |
| `GET` | `/positions/:wallet` | All positions for a wallet |
| `GET` | `/creator/:wallet` | Creator dashboard |
| `GET` | `/creator-tokens/:address` | Creator token revenue history |
| `POST` | `/ai/compose` | AI basket composition from natural language thesis |

### Decimal Conventions

All token amounts are raw `uint256` strings to avoid JavaScript precision loss:

| Asset | Decimals | Raw example | Display |
|---|---|---|---|
| USDG | 6 | `"100000000"` | $100.00 |
| Basket tokens | 18 | `"1000000000000000000"` | 1.0 |
| Oracle prices | 8 | `"11000000000"` | $110.00 |
| Weights | bps | `5000` | 50% |

---

## Local Development

### Prerequisites

- [Foundry](https://getfoundry.sh/) — Solidity toolchain
- Go 1.22+
- Node.js 20+
- SQLite3 (`sudo apt-get install sqlite3` on Ubuntu)
- An Alchemy account with a Robinhood Chain Testnet app

### First-Time Setup

```bash
# Clone
git clone git@github.com:OCD-Labs/Weave.git
cd Weave

# Install Forge dependencies
make install

# Install Node dependencies
make ts-install

# Install Go dependencies
make go-deps

# Copy environment template
cp .env.example .env
# Fill in: PRIVATE_KEY, ALCHEMY_RPC_URL, ALCHEMY_WS_URL, OPENAI_API_KEY
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

### Running All Services

Open three terminal tabs:

```bash
# Tab 1 — Go backend
make go-run

# Tab 2 — TypeScript AI agent
make ts-dev

# Tab 3 — Smoke test
curl -s http://localhost:8080/catalogue | python3 -m json.tool
curl -s http://localhost:3001/health | python3 -m json.tool
```

### Makefile Reference

```bash
# Solidity
make install        # install forge dependencies
make build          # compile all contracts
make test           # run test suite (84 tests, fuzz included)
make test-v         # verbose test output
make deploy         # deploy to Robinhood Chain testnet
make verify ADDR=0x... NAME=src/Foo.sol:Foo

# Go backend
make go-deps        # download Go modules
make go-build       # compile binary to bin/weave-backend
make go-run         # run the backend

# TypeScript agent
make ts-install     # npm install
make ts-typecheck   # tsc --noEmit
make ts-dev         # run with ts-node
make ts-build       # compile to dist/

# Emergency token recovery
make router-withdraw-all
make router-withdraw TOKEN=0x... AMOUNT=...
make basket-redeem BASKET_ADDRESS=0x... BASKET_TOKEN_AMOUNT=...
make creator-claim CREATOR_TOKEN_ADDRESS=0x...

# Misc
make env            # print active environment variables
make clean          # remove build artifacts
make fmt            # format Solidity
make snapshot       # update gas snapshot
```

---

## Deployment

### Contract Deployment (Robinhood Chain Testnet)

```bash
make deploy
```

After deployment, update `.env` with the printed contract addresses, then fund the `MockSwapRouter`:

```bash
make router-withdraw-all   # if you need tokens back first
# then fund router with cast send commands (see Smart Contracts section above)
```

Verify all contracts:

```bash
make verify ADDR=<REGISTRY>     NAME=src/WeaveRegistry.sol:WeaveRegistry
make verify ADDR=<ROUTER>       NAME=src/MockSwapRouter.sol:MockSwapRouter
make verify ADDR=<IMPL>         NAME=src/BasketImplementation.sol:BasketImplementation
make verify ADDR=<FACTORY>      NAME=src/BasketFactory.sol:BasketFactory
make verify ADDR=<AUTOMATION>   NAME=src/WeaveAutomation.sol:WeaveAutomation
make verify ADDR=<ORACLE_TSLA>  NAME=src/MockOracle.sol:MockOracle
```

---

## Environment Variables

Copy `.env.example` to `.env` and fill in all required values. Never commit `.env` to git.

| Variable | Required | Description |
|---|---|---|
| `PRIVATE_KEY` | Yes | Deployer wallet private key (testnet only) |
| `DEPLOYER_ADDRESS` | Yes | Deployer wallet address |
| `RPC_URL` | Yes | Robinhood Chain testnet public RPC |
| `ALCHEMY_RPC_URL` | Yes | Alchemy HTTP RPC for the backend |
| `ALCHEMY_WS_URL` | Yes | Alchemy WebSocket RPC for the indexer |
| `WEAVE_REGISTRY_ADDRESS` | Yes (after deploy) | Deployed registry address |
| `BASKET_FACTORY_ADDRESS` | Yes (after deploy) | Deployed factory address |
| `MOCK_SWAP_ROUTER_ADDRESS` | Yes (after deploy) | Deployed router address |
| `LLM_PROVIDER` | Yes | `openai`, `anthropic`, or `ollama` |
| `OPENAI_API_KEY` | If using OpenAI | OpenAI API key |
| `OPENAI_MODEL` | No | Defaults to `gpt-4.1-mini` |
| `ANTHROPIC_API_KEY` | If using Anthropic | Anthropic API key |
| `OLLAMA_BASE_URL` | No | Defaults to `http://localhost:11434` |
| `OLLAMA_MODEL` | No | Defaults to `llama3.2` |
| `DB_PATH` | No | SQLite path. Defaults to `./weave.db` |
| `API_PORT` | No | Backend port. Defaults to `8080` |
| `AI_SERVICE_PORT` | No | Agent port. Defaults to `3001` |
| `PRICE_POLL_INTERVAL_SECS` | No | Oracle poll interval. Defaults to `60` |

*Built on Robinhood Chain testnet — an Arbitrum L2 designed for tokenized real-world assets. Robinhood Chain is the only blockchain with a catalogue of tokenized equities at meaningful scale, making Weave's equity index protocol possible for the first time.*