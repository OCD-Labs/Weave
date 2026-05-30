# Weave — Onchain Index Protocol for Tokenized Equities

**Built for the Arbitrum Open House London: Online Buildathon, 2026**

**Team:** Yemi (Ikeh Chukwuka Favour) — OCD Labs

---

## What Weave Is

Weave is a protocol that lets anyone compose a thematic basket of tokenized stocks and ETFs from Robinhood Chain's catalogue, publish it as a single investable onchain instrument, and earn a continuous share of the revenue that basket generates for as long as other investors hold it. Investors who buy into a basket get instant diversified exposure to any equity theme they believe in, through a single token that rebalances itself automatically if the creator chose to enable that, and that they can use anywhere else in DeFi as collateral or as a transfer of value. The protocol earns a management fee on all assets under management, and a meaningful portion of that fee flows directly to the person who designed the basket, continuously and in perpetuity, through a revenue sharing mechanism built into the basket token itself.

---

## The Problem

Every thematic index that exists today was designed by a financial institution, packaged as an ETF, and sold to retail investors who had no say in its composition and no share in its economics. If you believe that European defence companies are the most important equity theme of the next decade, your options are to find an ETF that roughly approximates that thesis, buy each stock individually and manage the portfolio yourself, or accept that the product you want simply does not exist. There is no mechanism for expressing a precise investment thesis as a composable financial instrument without institutional resources, regulatory approvals, and fund management infrastructure.

In DeFi, the situation is not meaningfully better. Existing index protocols like Index Coop operate on crypto assets and do not have access to tokenized equities. They also do not have a creator economy layer where the person who designed the index shares in its long-term success. Index creation is a closed activity managed by protocol governance, not an open marketplace where any investor can publish a thesis and let the market validate it with capital.

Robinhood Chain changes the underlying asset landscape entirely. With nearly two thousand tokenized stocks and ETFs now available on an EVM-compatible chain with low fees, real-time Chainlink price feeds, and planned mainnet launch in 2026, the raw material for a genuine equity index protocol now exists onchain for the first time. Weave is the protocol that turns that raw material into a creator economy for financial products.

---

## The AI Composition Engine

Index construction is the skill that separates a good ETF from a mediocre one. Selecting the right stocks, setting weights that reflect their relative importance to a thesis, and knowing which companies are direct plays versus indirect beneficiaries requires research that most investors with a genuine conviction simply do not have the stock-level depth to execute precisely.

Weave solves this with an AI composition engine that sits at the basket creation step. A user describes their investment thesis in plain language, something like "companies building the physical infrastructure for AI, focused on data centres, power, and semiconductor manufacturing outside the United States." The agent reads the full Robinhood Chain stock catalogue including sector classifications, market capitalisation data, and recent price performance sourced from Chainlink feeds, and outputs a proposed basket composition with target weights and a written rationale explaining why each stock was selected and what role it plays in the thesis. The user sees the full proposal before committing to anything. They can accept it as-is, remove any stock they disagree with, add one the agent missed, or adjust individual weights to reflect their own conviction levels. The agent handles the research. The human handles the final judgement call.

This is not just decoration as the agent is performing a genuine domain-specific financial research across a catalogue of two thousand stocks, cross-referencing sector data and price history against a natural language thesis, and producing actionable output with explicit reasoning. Without this layer, basket creation requires the kind of stock-by-stock knowledge that most people with valid investment theses do not possess. With it, the barrier to publishing a meaningful, well-constructed basket drops to having a clear idea and the willingness to review and approve the agent's proposal.

---

## How It Works

A basket creator opens the Weave interface and describes their investment thesis in natural language. The AI composition engine analyses the thesis against the full Robinhood Chain catalogue and returns a proposed basket with constituent stocks, target weights, and written rationale. The creator reviews the proposal, makes any adjustments they see fit, and then configures their basket's operational parameters. They decide whether to enable automatic rebalancing and if so at what drift threshold, which is the maximum percentage any constituent can deviate from its target weight before the basket automatically restores its original proportions. They seed the basket with an initial USDC deposit, which the protocol uses to purchase the constituent stocks in the configured proportions. The basket deploys, a basket token is minted representing ownership of the underlying position, and the basket appears in the Weave marketplace with its composition, thesis, and live performance data.

Other investors who find the basket compelling deposit USDC into it. The protocol uses their deposit to buy the same constituent stocks in the same proportions and mints additional basket tokens proportional to their contribution. Every basket token represents the same fractional ownership of the same underlying stock positions regardless of when it was purchased, because the protocol maintains the proportional relationship on every deposit. Investors can redeem their basket tokens at any time and receive USDC equal to the current value of their proportional underlying holdings, priced by Chainlink data feeds at the moment of redemption.

The basket creator receives a revenue share token at the time of basket creation. This token implements ERC-7641, which defines a standard interface for claiming proportional shares of a communal revenue pool. Every time the protocol collects management fees from the basket, a portion of those fees is deposited into the revenue pool associated with that basket's creator token. The creator can claim their accumulated revenue at any time by calling the claim function on their creator token. If the creator sells their creator token, the new holder inherits all future revenue from that basket. The creator token is itself transferable and tradeable, which means basket design as an activity has a secondary market. A basket that has accumulated significant AUM and generates meaningful ongoing fees is a valuable creator token, and the market will price it accordingly.

---

## Rebalancing as a Design Choice

Weave treats rebalancing as an explicit design decision made by the basket creator at launch, not as a default behaviour imposed on all baskets. This distinction matters because different investment philosophies call for different approaches and the protocol should not make that choice on behalf of creators.

A basket creator who believes in strict disciplined weighting will enable rebalancing and set a drift threshold. When any constituent drifts beyond that threshold due to price movements, the rebalancing mechanism triggers automatically. On Robinhood Chain this is handled by Chainlink Automation, which monitors all rebalancing-enabled baskets continuously, identifies those with drift exceeding their threshold, and executes the rebalancing trades through the on-chain DEX. The cost of running Chainlink Automation for a basket is funded from that basket's management fee revenue, and only baskets above a minimum AUM threshold have their automation funded by the protocol. Baskets below that threshold can still be rebalanced by anyone who calls the permissionless rebalance function directly, giving investors in small baskets the ability to maintain their basket's weights if they care to.

A basket creator who believes that letting winners run is the correct strategy will disable rebalancing. Their basket launches with precise weights and those weights drift freely as prices move. A basket that started as equal-weight exposure to five AI companies will naturally become dominated by whichever company appreciated most. This reflects a genuine buy-and-hold philosophy, requires no automation, generates no rebalancing gas costs, and is a valid and deliberate investment approach rather than a limitation.

Both basket types live on the same protocol, attract deposits through the same interface, and generate revenue for their creators through the same ERC-7641 mechanism. The only difference is whether the underlying composition is actively maintained or allowed to drift.

---

## Why This Is Novel

Three things about Weave do not exist in combination anywhere today.

The first is the asset universe. Robinhood Chain is the only blockchain with a catalogue of nearly two thousand tokenized equities. Every existing onchain index protocol operates on crypto assets. Weave builds the first equity index layer on the only chain where equity index construction is possible at meaningful scale.

The second is the creator economy layer. Every basket published on Weave generates ongoing revenue for its creator through ERC-7641. The person who designed a successful thematic basket is rewarded proportionally and continuously for the duration of that basket's life, purely through the management fee revenue it attracts. This does not exist in traditional finance, where index creators are employees of the institution rather than independent parties earning directly from their intellectual contribution. It does not exist in current DeFi index protocols, which treat index creation as a governance activity rather than an entrepreneurial one.

The third is the AI composition engine that makes meaningful basket creation accessible to anyone with a genuine investment thesis, not just those with the stock-level research depth to execute one precisely. The combination of AI-assisted composition, creator revenue sharing, and a genuinely diverse equity catalogue is something that could not have been assembled on any existing chain before Robinhood Chain made the asset universe available.

---

## The Market

The global ETF market manages over fifteen trillion dollars in assets. The active segment of that market, specifically thematic ETFs that reflect specific investment theses, has grown at over twenty percent annually for the past five years. The addressable market for Weave is everyone who wants thematic equity exposure that is not currently packaged as an ETF, everyone who has a genuine investment thesis they want to express precisely, and everyone who wants to earn from designing financial products rather than just consuming them.

The revenue model is a management fee on AUM, charged continuously and collected automatically by the protocol. A portion flows to the protocol treasury. The remainder flows to basket creators through ERC-7641. As the Robinhood Chain ecosystem grows and the tokenized equity catalogue expands, the range of investable baskets that can be constructed grows proportionally. The protocol's revenue grows with total AUM, not with the number of baskets, which means scaling does not require proportionally more infrastructure.

---

## Technology

The basket contracts are written in Solidity and deployed on Robinhood Chain testnet, implementing ERC-7641 for creator revenue sharing and ERC-20 for basket tokens. A basket factory contract deploys new baskets using the ERC-1167 minimal proxy pattern and registers them in a protocol-wide registry. Chainlink Data Feeds provide real-time price feeds for all supported constituent stocks. Chainlink Automation monitors rebalancing-enabled baskets and triggers rebalancing when drift exceeds the configured threshold, funded from basket management fee revenue above a minimum AUM threshold. The DEX router handles all constituent purchases and rebalancing swaps on-chain. A lightweight backend indexer built in Go with SQLite stores basket metadata, composition history, performance history, and investor position data for fast frontend queries. The AI composition engine is a TypeScript agent that reads the Robinhood Chain catalogue metadata and Chainlink price data, runs the thesis-to-composition analysis using the Anthropic API, and returns structured proposals to the frontend. The frontend is a TypeScript web application connecting to Robinhood Chain through the user's wallet.

---

## Why Robinhood Chain, Why Now

Robinhood Chain has two thousand tokenized equities and a mandate to build DeFi infrastructure around them. Weave is exactly the product that demonstrates what two thousand onchain stocks enable that one hundred onchain tokens never could: genuine thematic index construction with meaningful diversification across sectors, geographies, and investment styles. No other product we know of does this.
