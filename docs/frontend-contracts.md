# Weave — Frontend Contract Integration Reference

> For the frontend developer. Every direct contract read and write the UI performs,
> with exact function signatures, argument types, return types, and notes on
> when to call each one. Read this alongside the API reference — most data
> comes from the backend API, contract calls are only for live reads and write operations.

---

## Table of Contents

- [Network Setup](#network-setup)
- [Contract ABIs Needed](#contract-abis-needed)
- [Decimal Reference](#decimal-reference)
- [Read Calls (View Functions)](#read-calls-view-functions)
- [Write Calls (Transactions)](#write-calls-transactions)
- [Event Listening](#event-listening)
- [Error Handling](#error-handling)
- [Call Patterns by Page](#call-patterns-by-page)
- [Contract Addresses](#contract-addresses)

---

## Network Setup

```typescript
const robinhoodChainTestnet = {
  id: 46630,
  name: 'Robinhood Chain Testnet',
  network: 'robinhood-testnet',
  nativeCurrency: { name: 'Ether', symbol: 'ETH', decimals: 18 },
  rpcUrls: {
    default: { http: ['https://rpc.testnet.chain.robinhood.com'] },
    public:  { http: ['https://rpc.testnet.chain.robinhood.com'] },
  },
  blockExplorers: {
    default: {
      name: 'Blockscout',
      url: 'https://explorer.testnet.chain.robinhood.com',
    },
  },
}
```

---

## Contract ABIs Needed

Generate these from the Solidity source using `forge inspect`:

```bash
forge inspect src/WeaveRegistry.sol:WeaveRegistry abi > abis/WeaveRegistry.json
forge inspect src/BasketImplementation.sol:BasketImplementation abi > abis/BasketImplementation.json
forge inspect src/BasketFactory.sol:BasketFactory abi > abis/BasketFactory.json
forge inspect src/CreatorToken.sol:CreatorToken abi > abis/CreatorToken.json
```

Also need standard `ERC20.json` for USDG approval calls.

---

## Decimal Reference

Never do arithmetic on JavaScript numbers for financial values.
Always use `BigInt` or a library like `viem` until the final display step.

| Token | Decimals | Conversion |
|---|---|---|
| USDG | 6 | `amount / 1e6` → USD display |
| Basket tokens | 18 | `amount / 1e18` → token display |
| Oracle prices | 8 | `price / 1e8` → USD display |
| NAV per token | 18 | `nav / 1e18` → USD per basket token |
| Weights | basis points | `bps / 100` → percentage display |
| Creator tokens | 18 | `amount / 1e18` → display |

---

## Read Calls (View Functions)

These are `eth_call` — free, no gas, no wallet signature required.
Call these to get live on-chain state to cross-check or supplement backend data.

---

### WeaveRegistry

**Contract address:** `WEAVE_REGISTRY_ADDRESS` from env

---

#### `managementFeeBps()`

Returns the protocol management fee rate.

```typescript
const feeBps = await readContract({
  address: REGISTRY_ADDRESS,
  abi: WeaveRegistryABI,
  functionName: 'managementFeeBps',
})
// feeBps: bigint — e.g. 50n = 0.5%
// Use for: fee preview on deposit and create basket forms
```

---

#### `minFirstDepositUsdg()`

Returns the minimum initial deposit required to create a basket.

```typescript
const minDeposit = await readContract({
  address: REGISTRY_ADDRESS,
  abi: WeaveRegistryABI,
  functionName: 'minFirstDepositUsdg',
})
// minDeposit: bigint — 6-decimal USDG
// Use for: create basket form validation
```

---

#### `maxConstituents()`

Returns the maximum number of stocks allowed per basket.

```typescript
const max = await readContract({
  address: REGISTRY_ADDRESS,
  abi: WeaveRegistryABI,
  functionName: 'maxConstituents',
})
// max: bigint — currently 20n
// Use for: create basket form validation
```

---

#### `minWeightBps()`

Returns the minimum weight per constituent in basis points.

```typescript
const minWeight = await readContract({
  address: REGISTRY_ADDRESS,
  abi: WeaveRegistryABI,
  functionName: 'minWeightBps',
})
// minWeight: bigint — currently 100n = 1%
// Use for: weight validation in composition step
```

---

#### `protocolShareBps()`

Returns the protocol's share of the management fee.

```typescript
const protocolShare = await readContract({
  address: REGISTRY_ADDRESS,
  abi: WeaveRegistryABI,
  functionName: 'protocolShareBps',
})
// protocolShare: bigint — e.g. 2000n = 20%
// creatorShare = 10000n - protocolShare = 8000n = 80%
// Use for: fee breakdown display on create basket page
```

---

### BasketImplementation (one per basket proxy)

**Contract address:** the specific basket proxy address from the API or registry.
Uses `BasketImplementation` ABI — the proxy delegates to the implementation.

---

#### `balanceOf(address account)`

Returns a wallet's basket token balance.

```typescript
const balance = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'balanceOf',
  args: [walletAddress],
})
// balance: bigint — 18-decimal basket tokens
// Use for: redeem form max amount, portfolio display
// This is the source of truth — backend position data provides cost basis
```

---

#### `navPerToken()`

Returns the current net asset value per basket token in USDG.

```typescript
const nav = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'navPerToken',
})
// nav: bigint — 18-decimal USDG per basket token
// Use for: live price display, estimated redemption value
// Cross-check against backend /baskets/:address response
```

---

#### `totalSupply()`

Returns the total basket token supply outstanding.

```typescript
const supply = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'totalSupply',
})
// supply: bigint — 18-decimal
```

---

#### `totalValueUsdg()`

Returns the total AUM of the basket in USDG.

```typescript
const aum = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'totalValueUsdg',
})
// aum: bigint — 6-decimal USDG
```

---

#### `needsRebalancing()`

Returns whether any constituent currently exceeds the drift threshold.

```typescript
const needs = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'needsRebalancing',
})
// needs: boolean
// Use for: showing the Rebalance Now button on basket detail page
// Always read live — do not cache this value
```

---

#### `maxDrift()`

Returns the current maximum drift in basis points across all constituents.

```typescript
const drift = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'maxDrift',
})
// drift: bigint — basis points
// Use for: drift indicator progress bar on basket detail page
```

---

#### `rebalancingEnabled()`

Returns whether this basket has automatic rebalancing enabled.

```typescript
const enabled = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'rebalancingEnabled',
})
// enabled: boolean
```

---

#### `suspended()`

Returns whether this basket is suspended (a constituent was deactivated).

```typescript
const isSuspended = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'suspended',
})
// isSuspended: boolean
// Use for: showing suspended warning banner, disabling deposit button
```

---

#### `basketState()`

Returns the full basket state in one call. Use this instead of multiple
individual calls when you need everything at once.

```typescript
const [
  constituents,
  targetWeights,
  currentWeights,
  balances,
  totalValue,
  nav,
  rebalancingEnabled,
  driftThresholdBps,
  maxDrift,
] = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'basketState',
})

// constituents:       address[]  — constituent token addresses
// targetWeights:      bigint[]   — target weights in bps
// currentWeights:     bigint[]   — live weights in bps from oracle prices
// balances:           bigint[]   — constituent token balances (18-decimal each)
// totalValue:         bigint     — total AUM in 6-decimal USDG
// nav:                bigint     — NAV per token in 18-decimal USDG
// rebalancingEnabled: boolean
// driftThresholdBps:  bigint     — drift trigger threshold in bps
// maxDrift:           bigint     — current maximum drift in bps

// Use for: basket detail page initial load
```

---

#### `constituents()`

Returns the array of constituent token addresses.

```typescript
const addrs = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'constituents',
})
// addrs: address[]
// Use for: building minAmountsOut array for rebalance call
```

---

### CreatorToken (one per basket)

**Contract address:** `creatorToken` field from basket API response or registry.

---

#### `balanceOf(address account)`

Returns a wallet's creator token balance.

```typescript
const balance = await readContract({
  address: creatorTokenAddress,
  abi: CreatorTokenABI,
  functionName: 'balanceOf',
  args: [walletAddress],
})
// balance: bigint — 18-decimal creator tokens
// TOTAL_SUPPLY = 1_000_000 * 1e18
// ownershipPct = balance * 100n / (1_000_000n * 10n**18n)
```

---

#### `claimableRevenue(address account, uint256 snapshotId)`

Returns the USDG claimable by an account for a specific snapshot.

```typescript
const claimable = await readContract({
  address: creatorTokenAddress,
  abi: CreatorTokenABI,
  functionName: 'claimableRevenue',
  args: [walletAddress, snapshotId],
})
// claimable: bigint — 6-decimal USDG
// Use for: displaying claimable amount per snapshot on creator dashboard
// Call for each unclaimed snapshot ID from the backend /creator-tokens/:address response
```

---

#### `snapshotCount()`

Returns the total number of revenue snapshots created so far.

```typescript
const count = await readContract({
  address: creatorTokenAddress,
  abi: CreatorTokenABI,
  functionName: 'snapshotCount',
})
// count: bigint
// Use for: knowing how many snapshots to query claimableRevenue for
```

---

### ERC-20 (USDG and basket tokens)

---

#### `allowance(address owner, address spender)`

Returns the current USDG allowance granted to a spender.

```typescript
const allowance = await readContract({
  address: USDG_ADDRESS,
  abi: ERC20ABI,
  functionName: 'allowance',
  args: [walletAddress, spenderAddress],  // spender = basket or factory address
})
// allowance: bigint — 6-decimal USDG
// Use for: checking if approval step is needed before deposit or basket creation
```

---

## Write Calls (Transactions)

These require a connected wallet and gas. Always show a transaction status
toast with pending → confirmed → error states.

---

### USDG Approval

Must be called before any deposit or basket creation if the current allowance
is insufficient. Check `allowance()` first — do not always re-approve.

```typescript
// Step 1: check allowance
const current = await readContract({
  address: USDG_ADDRESS,
  abi: ERC20ABI,
  functionName: 'allowance',
  args: [walletAddress, spenderAddress],
})

// Step 2: approve only if needed
if (current < requiredAmount) {
  await writeContract({
    address: USDG_ADDRESS,
    abi: ERC20ABI,
    functionName: 'approve',
    args: [
      spenderAddress,   // basket address for deposits, factory address for creation
      requiredAmount,   // exact amount or MaxUint256 for unlimited approval
    ],
  })
  // Wait for confirmation before proceeding to the actual transaction
}
```

---

### BasketFactory

---

#### `createBasket(...)`

Deploys a new basket proxy and creator token atomically.
This is the most complex write call — validate all inputs client-side before submitting.

```typescript
await writeContract({
  address: BASKET_FACTORY_ADDRESS,
  abi: BasketFactoryABI,
  functionName: 'createBasket',
  args: [
    name,               // string   — basket display name, max 50 chars
    symbol,             // string   — basket token symbol, max 8 chars, uppercase
    thesis,             // string   — investment thesis text
    constituents,       // address[] — constituent token addresses from catalogue
    targetWeightsBps,   // uint256[] — weights in bps, must sum to exactly 10000
    rebalancingEnabled, // bool     — whether to enable auto-rebalancing
    driftThresholdBps,  // uint256  — drift trigger in bps (0 if rebalancing disabled)
    initialDepositUsdg, // uint256  — 6-decimal USDG seed amount
  ],
})

// Prerequisite: USDG approved to BASKET_FACTORY_ADDRESS for at least initialDepositUsdg
//
// Client-side validation before submitting:
//   constituents.length >= 3
//   constituents.length <= maxConstituents (read from registry)
//   constituents.length === targetWeightsBps.length
//   targetWeightsBps.reduce((a,b) => a+b, 0n) === 10_000n
//   every weight >= minWeightBps (read from registry)
//   every weight <= 5_000n
//   no duplicate addresses in constituents
//   every address exists in the active catalogue (check via backend /catalogue)
//   initialDepositUsdg >= minFirstDepositUsdg (read from registry)
//   if rebalancingEnabled: driftThresholdBps > 0n && driftThresholdBps <= 5_000n
//
// After confirmation: listen for BasketCreated event to get the deployed basket address
// Do NOT navigate to the basket page until you have confirmed the event
```

**Getting the deployed basket address after creation:**

```typescript
// Option 1: read from transaction receipt logs
const receipt = await waitForTransactionReceipt({ hash: txHash })
const basketCreatedLog = receipt.logs.find(log =>
  log.topics[0] === keccak256('BasketCreated(address,address,address,string,bool)')
)
const basketAddress = '0x' + basketCreatedLog.topics[1].slice(26)

// Option 2: poll the backend after a few seconds
// GET /baskets will include the new basket once the indexer picks up the event
```

---

### BasketImplementation

---

#### `deposit(uint256 usdgAmount, uint256 minBasketTokensOut, address receiver)`

Deposit USDG into a basket and receive basket tokens.

```typescript
await writeContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'deposit',
  args: [
    usdgAmount,          // uint256 — 6-decimal USDG to deposit
    minBasketTokensOut,  // uint256 — minimum basket tokens to accept (slippage protection)
    receiverAddress,     // address — who receives the basket tokens (usually walletAddress)
  ],
})

// Prerequisite: USDG approved to basketAddress for at least usdgAmount
//
// Computing minBasketTokensOut (5% slippage):
//   const nav = await readContract({ functionName: 'navPerToken' })
//   const supply = await readContract({ functionName: 'totalSupply' })
//   const totalValue = await readContract({ functionName: 'totalValueUsdg' })
//   const feeBps = await readContract({ address: REGISTRY, functionName: 'managementFeeBps' })
//   const netUsdg = usdgAmount - (usdgAmount * feeBps / 10_000n)
//   const estimatedTokens = supply === 0n
//     ? netUsdg * 1_000_000_000_000n   // first depositor: 1 USDG = 1 token (scaled)
//     : netUsdg * supply / totalValue
//   const minBasketTokensOut = estimatedTokens * 95n / 100n  // 5% slippage
```

---

#### `redeem(uint256 basketTokenAmount, uint256 minUsdgOut, address receiver)`

Burn basket tokens and receive USDG.

```typescript
await writeContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'redeem',
  args: [
    basketTokenAmount,  // uint256 — 18-decimal basket tokens to burn
    minUsdgOut,         // uint256 — minimum USDG to accept (slippage protection)
    receiverAddress,    // address — who receives the USDG (usually walletAddress)
  ],
})

// No approval needed — the basket burns the caller's own tokens
//
// Computing minUsdgOut (5% slippage):
//   const nav = await readContract({ functionName: 'navPerToken' })
//   const feeBps = await readContract({ address: REGISTRY, functionName: 'managementFeeBps' })
//   const grossUsdg = basketTokenAmount * nav / 10n**18n
//   const feeUsdg = grossUsdg * feeBps / 10_000n
//   const netUsdg = grossUsdg - feeUsdg
//   const minUsdgOut = netUsdg * 95n / 100n  // 5% slippage
```

---

#### `rebalance(uint256[] minAmountsOut)`

Permissionless rebalancing call. Anyone can call this — it is a public service action.
Only works on baskets with `rebalancingEnabled === true` and where `needsRebalancing()` returns true.

```typescript
const constituentsArr = await readContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'constituents',
})

// Pass zero minimums — the MockSwapRouter prices at oracle rates
// On mainnet, the real DEX handles slippage internally
const minAmountsOut = new Array(constituentsArr.length).fill(0n)

await writeContract({
  address: basketAddress,
  abi: BasketImplementationABI,
  functionName: 'rebalance',
  args: [minAmountsOut],
})

// Always check needsRebalancing() immediately before showing the Rebalance Now button
// The button should only be visible when needsRebalancing() === true
```

---

### CreatorToken

---

#### `claim(uint256 snapshotId)`

Claim USDG revenue for a single snapshot. Reverts if already claimed.

```typescript
await writeContract({
  address: creatorTokenAddress,
  abi: CreatorTokenABI,
  functionName: 'claim',
  args: [snapshotId],  // uint256 — snapshot ID from backend /creator-tokens/:address
})

// Use for: claiming a specific snapshot from the revenue history list
// Check claimableRevenue() before showing the claim button for a snapshot
```

---

#### `claimAll()`

Claim all unclaimed snapshots in a single transaction.
Silently skips snapshots where the caller has nothing to claim.

```typescript
await writeContract({
  address: creatorTokenAddress,
  abi: CreatorTokenABI,
  functionName: 'claimAll',
})

// Use for: the primary "Claim All Revenue" button on the creator dashboard
// If the creator has multiple baskets, call claimAll() on each creator token contract
// Show a progress indicator: "Claiming from basket 1 of 3..."
```

---

#### `burn(uint256 amount)`

Burn creator tokens and redeem a proportional share of the accumulated revenue pool.
This is the ERC-7641 deflationary mechanism — reduces total supply permanently.

```typescript
await writeContract({
  address: creatorTokenAddress,
  abi: CreatorTokenABI,
  functionName: 'burn',
  args: [amount],  // uint256 — 18-decimal creator tokens to burn
})

// Check redeemableOnBurn(amount) first to show the user what they'll receive
const redeemable = await readContract({
  address: creatorTokenAddress,
  abi: CreatorTokenABI,
  functionName: 'redeemableOnBurn',
  args: [amount],
})
// redeemable: bigint — 6-decimal USDG they will receive
```

---

## Event Listening

Listen for these events to update the UI after transactions confirm
without requiring a full page refresh.

---

### BasketFactory — `BasketCreated`

```typescript
// Signature: BasketCreated(address indexed basket, address indexed creatorToken, address indexed creator, string name, bool rebalancingEnabled)

watchContractEvent({
  address: BASKET_FACTORY_ADDRESS,
  abi: BasketFactoryABI,
  eventName: 'BasketCreated',
  onLogs: (logs) => {
    for (const log of logs) {
      const { basket, creatorToken, creator, name, rebalancingEnabled } = log.args
      // Navigate to /baskets/:basket after creation confirms
    }
  },
})
```

---

### BasketImplementation — `Deposited`

```typescript
// Signature: Deposited(address indexed investor, uint256 usdgAmount, uint256 basketTokensMinted, uint256 feeUsdg)

watchContractEvent({
  address: basketAddress,
  abi: BasketImplementationABI,
  eventName: 'Deposited',
  args: { investor: walletAddress },  // filter to current wallet
  onLogs: (logs) => {
    // Refresh basket token balance and portfolio
  },
})
```

---

### BasketImplementation — `Redeemed`

```typescript
// Signature: Redeemed(address indexed investor, uint256 basketTokensBurned, uint256 usdgReturned, uint256 feeUsdg)

watchContractEvent({
  address: basketAddress,
  abi: BasketImplementationABI,
  eventName: 'Redeemed',
  args: { investor: walletAddress },
  onLogs: (logs) => {
    // Refresh basket token balance and USDG balance
  },
})
```

---

### BasketImplementation — `Rebalanced`

```typescript
// Signature: Rebalanced(address indexed triggeredBy)

watchContractEvent({
  address: basketAddress,
  abi: BasketImplementationABI,
  eventName: 'Rebalanced',
  onLogs: (logs) => {
    // Refresh current weights and drift indicator
    // needsRebalancing() will return false after this fires
  },
})
```

---

### CreatorToken — `RevenueClaimed`

```typescript
// Signature: RevenueClaimed(address indexed account, uint256 indexed snapshotId, uint256 usdgAmount)

watchContractEvent({
  address: creatorTokenAddress,
  abi: CreatorTokenABI,
  eventName: 'RevenueClaimed',
  args: { account: walletAddress },
  onLogs: (logs) => {
    // Refresh claimable amounts on creator dashboard
  },
})
```

---

## Error Handling

The contracts revert with custom errors. Decode these from transaction receipts
and show human-readable messages in the UI.

| Custom Error | Selector | User-Facing Message |
|---|---|---|
| `BasketSuspended()` | `0x...` | "This basket is suspended and cannot accept deposits." |
| `InsufficientSlippage()` | `0x...` | "Price moved too much during execution. Please try again." |
| `StalePrice(address)` | `0x...` | "Price data is temporarily unavailable. Please try again." |
| `WeightSumInvalid(uint256)` | `0x...` | "Constituent weights must sum to exactly 100%." |
| `MinDepositNotMet()` | `0x...` | "Deposit amount is below the minimum required." |
| `RebalancingNotEnabled()` | `0x...` | "This basket does not have rebalancing enabled." |
| `DriftThresholdNotMet()` | `0x...` | "This basket does not currently need rebalancing." |
| `TooFewConstituents(uint256)` | `0x...` | "A basket needs at least 3 constituents." |
| `TooManyConstituents(uint256,uint256)` | `0x...` | "Maximum constituents exceeded." |
| `WeightTooLow(address,uint256)` | `0x...` | "Each constituent must have at least 1% weight." |
| `WeightTooHigh(address,uint256)` | `0x...` | "No single constituent can exceed 50% weight." |
| `DuplicateConstituent(address)` | `0x...` | "Duplicate constituent in basket composition." |
| `InvalidDriftThreshold()` | `0x...` | "Drift threshold must be between 1% and 50%." |
| `AlreadyClaimed(address,uint256)` | `0x...` | "You have already claimed this snapshot." |
| `NothingToClaim()` | `0x...` | "No claimable revenue for your current balance." |
| `ZeroAmount()` | `0x...` | "Amount cannot be zero." |

```typescript
// Decode custom errors from a failed transaction using viem
import { decodeErrorResult } from 'viem'

try {
  await writeContract({ ... })
} catch (error) {
  if (error.cause?.data) {
    const decoded = decodeErrorResult({
      abi: BasketImplementationABI,
      data: error.cause.data,
    })
    // decoded.errorName gives you the error name to map to a user message
    showToast(ERROR_MESSAGES[decoded.errorName] ?? 'Transaction failed.')
  }
}
```

---

## Call Patterns by Page

Quick reference for which calls each page needs.

---

### Marketplace (`/`)

No wallet required. No contract calls — all data from `GET /baskets`.

---

### Basket Detail (`/baskets/:address`)

**On load (no wallet):**
```
GET /baskets/:address           → basket metadata and performance
basketState()                   → live weights, NAV, drift
needsRebalancing()              → show/hide Rebalance Now button
```

**On load (wallet connected):**
```
balanceOf(walletAddress)        → investor's basket token balance
GET /baskets/:address/positions/:wallet  → cost basis and PnL
allowance(wallet, basketAddress) → pre-check for deposit form
```

**Deposit flow:**
```
1. allowance() → if insufficient:
2. approve(basketAddress, amount)  → wait for confirmation
3. deposit(amount, minOut, wallet) → wait for confirmation
4. refresh balanceOf() and totalValueUsdg()
```

**Redeem flow:**
```
1. balanceOf() → populate max amount
2. redeem(tokenAmount, minOut, wallet) → wait for confirmation
3. refresh balanceOf()
```

**Rebalance:**
```
1. needsRebalancing() → show button if true
2. constituents() → build minAmountsOut array
3. rebalance(minAmountsOut) → wait for confirmation
4. refresh basketState()
```

---

### Create Basket (`/create`)

**Step 1 — Thesis input:**
```
POST /ai/compose  → AI proposal (via backend proxy)
```

**Step 2 — Review composition:**
```
GET /catalogue    → validate all addresses exist and are active
minWeightBps()    → validate no weight below minimum
maxConstituents() → validate constituent count
```

**Step 3 — Configure:**
```
managementFeeBps()      → fee preview
protocolShareBps()      → creator share = 10000 - protocolShareBps
minFirstDepositUsdg()   → minimum initial deposit
```

**Step 4 — Deploy:**
```
1. allowance(wallet, FACTORY_ADDRESS) → if insufficient:
2. approve(FACTORY_ADDRESS, initialDeposit)
3. createBasket(...all params...)
4. listen for BasketCreated event → navigate to /baskets/:newAddress
```

---

### Portfolio (`/portfolio`)

**On load:**
```
GET /positions/:wallet    → all positions
balanceOf(wallet)         → live balance per basket (cross-check)
navPerToken()             → current value per basket (cross-check)
```

---

### Creator Dashboard (`/creator`)

**On load:**
```
GET /creator/:wallet                    → baskets created
GET /creator-tokens/:creatorTokenAddr   → snapshot history per basket
balanceOf(wallet) on each creatorToken  → ownership percentage
snapshotCount() on each creatorToken    → total snapshot count
claimableRevenue(wallet, snapshotId)    → claimable per snapshot
```

**Claim flow:**
```
1. claimAll() on each creator token contract
2. Show progress: "Claiming from basket N of M"
3. Refresh claimableRevenue() after each confirmation
```

---

## Notes for Implementation

**Multicall.** Where possible batch multiple read calls into a single `multicall` to reduce RPC round trips. viem's `useContractReads` hook handles this automatically in React.

**Polling interval.** For live data on the basket detail page, poll `basketState()` and `needsRebalancing()` every 30 seconds. Do not poll more frequently — oracle prices update on a 60-second interval on the backend.

**BigInt everywhere.** All `uint256` values from contracts come back as `bigint` in viem. Never cast to `number` until the final display step — `Number(bigint)` loses precision for large values.

**First depositor edge case.** When `totalSupply() === 0n`, the basket has no NAV yet. The estimated tokens for a deposit is `netUsdg * 1_000_000_000_000n` (multiply by 1e12 to convert 6-decimal USDG to 18-decimal tokens). Show "Initial price: 1 USDG = 1 basket token" in the UI for this case.

**Transaction confirmation.** Always wait for `waitForTransactionReceipt` before updating UI state or navigating. A transaction hash being available does not mean the transaction succeeded — it must be mined and confirmed.

**Revert reason display.** On Robinhood Chain testnet, revert reasons are available in the transaction receipt. Always decode them — a raw hex error is not acceptable UX.

## Contract Addresses

### Robinhood Chain Testnet (Chain ID: 46630)

| Contract | Address |
|---|---|
| WeaveRegistry | `0xE46331c15A61c8F99114c970f607E9b199603bb9` |
| BasketFactory | `0x436d3EB9f79416b9d9d33CEe60423dc1Dc2d3d43` |
| BasketImplementation | `0x977134d124C90F43F26E43Ad973214dd95b2a320` |
| WeaveAutomation | `0xEc97FF9Ba6FDD4e04a3C23DE4a9D3364928039D4` |
| MockSwapRouter | `0xdA13d829D4114Cf00DaE90f5c93314120242e482` |
| MockOracle (TSLA) | `0x26DAf42381CeD15760C5F47a5072a228370B100B` |
| MockOracle (AMZN) | `0x9f8c9D395997472DEC7FA0D0E9DD450EE7263BC9` |
| MockOracle (PLTR) | `0xc9fcB8B0B37fe77EAe1C73E36BFAb3230AA37D4b` |
| MockOracle (NFLX) | `0xdb35a2a98366e7d3F381682AC836fF101cC0A8e0` |
| MockOracle (AMD) | `0xbB4fB68f13425155D72813F91e703EFc811edf77` |

### Robinhood Chain Testnet Stock Tokens

| Token | Address |
|---|---|
| USDG | `0x7E955252E15c84f5768B83c41a71F9eba181802F` |
| TSLA | `0xC9f9c86933092BbbfFF3CCb4b105A4A94bf3Bd4E` |
| AMZN | `0x5884aD2f920c162CFBbACc88C9C51AA75eC09E02` |
| PLTR | `0x1FBE1a0e43594b3455993B5dE5Fd0A7A266298d0` |
| NFLX | `0x3b8262A63d25f0477c4DDE23F83cfe22Cb768C93` |
| AMD | `0x71178BAc73cBeb415514eB542a8995b82669778d` |