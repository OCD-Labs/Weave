package api

// openAPISpec is the complete OpenAPI 3.0 specification for the Weave API.
// Served at GET /openapi.json and consumed by Swagger UI at GET /docs.
const openAPISpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Weave API",
    "description": "Onchain index protocol for tokenized equities on Robinhood Chain. Create thematic baskets of tokenized stocks, earn revenue as a basket creator, and get AI-assisted composition from natural language investment theses.\n\n**Decimal conventions:**\n- USDG amounts: 6-decimal integer strings (e.g. \"10000000\" = $10.00)\n- Oracle prices: 8-decimal integer strings (e.g. \"41555000000\" = $415.55)\n- Basket token amounts: 18-decimal integer strings\n- All monetary values are returned as raw uint256 strings — never as floats",
    "version": "1.0.0",
    "contact": {
      "name": "OCD Labs",
      "url": "https://github.com/OCD-Labs/Weave"
    }
  },
  "servers": [
    {
      "url": "https://weave.up.railway.app",
      "description": "Production (Robinhood Chain Testnet)"
    },
    {
      "url": "http://localhost:8080",
      "description": "Local development"
    }
  ],
  "tags": [
    { "name": "Catalogue", "description": "Tokenized stock assets available for basket construction" },
    { "name": "Baskets",   "description": "Published investment baskets" },
    { "name": "Portfolio", "description": "Investor positions across baskets" },
    { "name": "Creator",   "description": "Basket creator revenue and dashboards" },
    { "name": "AI",        "description": "AI-assisted basket composition" }
  ],
  "paths": {
    "/catalogue": {
      "get": {
        "tags": ["Catalogue"],
        "summary": "List all supported assets",
        "description": "Returns all tokenized stock assets available for basket construction, with their latest oracle prices and 24h price change. Sorted alphabetically by symbol.",
        "operationId": "getCatalogue",
        "responses": {
          "200": {
            "description": "Array of active assets",
            "content": {
              "application/json": {
                "schema": { "type": "array", "items": { "$ref": "#/components/schemas/CatalogueAsset" } },
                "example": [
                  {
                    "address": "0x71178bac73cbeb415514eb542a8995b82669778d",
                    "symbol": "AMD",
                    "name": "Advanced Micro Devices Inc",
                    "sector": "Technology",
                    "oracle": "0xDaf7e6168A748A0348e8392d31377B486D9278Ab",
                    "isActive": true,
                    "currentPriceUsdg": "49701000000",
                    "priceChange24hPct": "2.45"
                  }
                ]
              }
            }
          }
        }
      }
    },
    "/catalogue/{address}": {
      "get": {
        "tags": ["Catalogue"],
        "summary": "Get a single asset",
        "description": "Returns asset metadata only. currentPriceUsdg and priceChange24hPct are NOT returned — use GET /catalogue for price data.",
        "operationId": "getCatalogueAsset",
        "parameters": [
          {
            "name": "address",
            "in": "path",
            "required": true,
            "description": "ERC-20 token address (case-insensitive)",
            "schema": { "type": "string", "example": "0x71178bac73cbeb415514eb542a8995b82669778d" }
          }
        ],
        "responses": {
          "200": {
            "description": "Asset metadata",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/CatalogueAssetDetail" } } }
          },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      }
    },
    "/prices": {
      "get": {
        "tags": ["Catalogue"],
        "summary": "Latest oracle prices",
        "description": "Returns the latest oracle price for assets that are constituents of at least one active basket. Assets not held by any basket are not returned. Returns an empty array before the first price poll cycle (60 seconds after startup).",
        "operationId": "getPrices",
        "responses": {
          "200": {
            "description": "Latest prices",
            "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/PriceEntry" } } } }
          }
        }
      }
    },
    "/baskets": {
      "get": {
        "tags": ["Baskets"],
        "summary": "List all baskets",
        "description": "Returns all published baskets with current NAV, AUM, and constituent summary.",
        "operationId": "listBaskets",
        "responses": {
          "200": {
            "description": "Array of baskets",
            "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/BasketSummary" } } } }
          }
        }
      }
    },
    "/baskets/{address}": {
      "get": {
        "tags": ["Baskets"],
        "summary": "Get basket detail",
        "description": "Returns full basket detail. Constituent weights and balances come from a live contract basketState() call cached for 30 seconds. Note: constituents do not include per-constituent price data — cross-reference with GET /catalogue by address.",
        "operationId": "getBasket",
        "parameters": [
          {
            "name": "address",
            "in": "path",
            "required": true,
            "description": "Basket proxy contract address",
            "schema": { "type": "string", "example": "0x4783ef175d5f9f0082a2ab61352c4db6f149a7d2" }
          }
        ],
        "responses": {
          "200": {
            "description": "Basket detail",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/BasketDetail" } } }
          },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      }
    },
    "/baskets/{address}/performance": {
      "get": {
        "tags": ["Baskets"],
        "summary": "NAV history time series",
        "description": "Returns the full NAV per token history. Updated every 5 minutes by the backend NAV poller. Returns an empty array if the poller has not run yet.",
        "operationId": "getBasketPerformance",
        "parameters": [
          { "name": "address", "in": "path", "required": true, "schema": { "type": "string" } }
        ],
        "responses": {
          "200": {
            "description": "NAV history ordered by timestamp ascending",
            "content": { "application/json": { "schema": { "type": "array", "items": { "$ref": "#/components/schemas/NavPoint" } } } }
          }
        }
      }
    },
    "/baskets/{address}/positions/{wallet}": {
      "get": {
        "tags": ["Baskets"],
        "summary": "Investor position in a basket",
        "description": "Returns a wallet's position in a specific basket. Cost basis and PnL are computed from on-chain deposit and redemption event history. Cross-check basketTokenBalance with a live contract balanceOf call for the redemption form.",
        "operationId": "getPosition",
        "parameters": [
          { "name": "address", "in": "path", "required": true, "description": "Basket proxy contract address", "schema": { "type": "string" } },
          { "name": "wallet",  "in": "path", "required": true, "description": "Investor wallet address", "schema": { "type": "string" } }
        ],
        "responses": {
          "200": {
            "description": "Investor position",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/InvestorPosition" } } }
          }
        }
      }
    },
    "/positions/{wallet}": {
      "get": {
        "tags": ["Portfolio"],
        "summary": "All positions for a wallet",
        "description": "Returns a portfolio summary across all baskets for a wallet. Returns a valid response with empty positions array and zero totals when the wallet has no deposit history — never 404.",
        "operationId": "getPortfolio",
        "parameters": [
          { "name": "wallet", "in": "path", "required": true, "description": "Investor wallet address", "schema": { "type": "string" } }
        ],
        "responses": {
          "200": {
            "description": "Portfolio summary",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/PortfolioSummary" } } }
          }
        }
      }
    },
    "/creator/{wallet}": {
      "get": {
        "tags": ["Creator"],
        "summary": "Creator dashboard",
        "description": "Returns all baskets created by a wallet with revenue snapshot data and claimable amounts. creatorTokenBalance, totalCreatorTokenSupply, and ownershipPct are NOT returned — read these directly from the creator token contract via balanceOf().",
        "operationId": "getCreatorDashboard",
        "parameters": [
          { "name": "wallet", "in": "path", "required": true, "schema": { "type": "string" } }
        ],
        "responses": {
          "200": {
            "description": "Creator dashboard. Returns empty baskets array when the wallet has created no baskets.",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/CreatorDashboard" } } }
          }
        }
      }
    },
    "/creator-tokens/{address}": {
      "get": {
        "tags": ["Creator"],
        "summary": "Creator token revenue history",
        "description": "Returns all ERC-7641 revenue snapshots for a creator token contract address.",
        "operationId": "getCreatorToken",
        "parameters": [
          {
            "name": "address",
            "in": "path",
            "required": true,
            "description": "Creator token contract address",
            "schema": { "type": "string", "example": "0x4fE76585301dC9ae7f4FaB6Eca1DD9e8caD3Bbb7" }
          }
        ],
        "responses": {
          "200": {
            "description": "Revenue snapshot history",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/CreatorTokenHistory" } } }
          }
        }
      }
    },
    "/ai/compose": {
      "post": {
        "tags": ["AI"],
        "summary": "AI basket composition",
        "description": "Submits a natural language investment thesis and returns an AI-generated basket composition proposal using OpenAI gpt-4.1-mini. No on-chain action is taken. All constituent addresses are from the active catalogue. Weights sum to exactly 10000 bps.",
        "operationId": "aiCompose",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/ComposeRequest" },
              "example": { "thesis": "companies building the physical infrastructure for AI including data centres, power, and semiconductor manufacturing" }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Basket composition proposal",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/ComposeResponse" } } }
          },
          "400": {
            "description": "Thesis too short (minimum 20 characters) or invalid JSON body",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Error" }, "example": { "error": "thesis must be at least 20 characters" } } }
          },
          "502": {
            "description": "OpenAI API call failed or returned invalid JSON after retry",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Error" } } }
          },
          "503": {
            "description": "Fewer than 3 active assets in catalogue — cannot compose a valid basket",
            "content": { "application/json": { "schema": { "$ref": "#/components/schemas/Error" }, "example": { "error": "not enough active assets in catalogue" } } }
          }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "CatalogueAsset": {
        "type": "object",
        "properties": {
          "address":           { "type": "string", "description": "ERC-20 token address on Robinhood Chain, lowercase" },
          "symbol":            { "type": "string", "example": "AMD" },
          "name":              { "type": "string", "example": "Advanced Micro Devices Inc" },
          "sector":            { "type": "string", "example": "Technology" },
          "oracle":            { "type": "string", "description": "OracleAdapter contract address, lowercase" },
          "isActive":          { "type": "boolean" },
          "currentPriceUsdg":  { "type": "string", "description": "8-decimal oracle price as raw uint256 string. Divide by 1e8 for USD display. e.g. '49701000000' = $497.01", "example": "49701000000" },
          "priceChange24hPct": { "type": "string", "description": "24h price change as formatted percentage string. '0.00' when insufficient history.", "example": "2.45" }
        }
      },
      "CatalogueAssetDetail": {
        "type": "object",
        "description": "Single asset metadata from GET /catalogue/:address. Does not include price data.",
        "properties": {
          "address":  { "type": "string" },
          "symbol":   { "type": "string" },
          "name":     { "type": "string" },
          "sector":   { "type": "string" },
          "oracle":   { "type": "string" },
          "isActive": { "type": "boolean" }
        }
      },
      "PriceEntry": {
        "type": "object",
        "properties": {
          "address":           { "type": "string", "description": "Token contract address, lowercase" },
          "symbol":            { "type": "string" },
          "priceUsdg":         { "type": "string", "description": "8-decimal oracle price as raw uint256 string" },
          "priceChange24hPct": { "type": "string" },
          "timestamp":         { "type": "integer", "description": "Unix timestamp of this price reading" }
        }
      },
      "BasketConstituentSummary": {
        "type": "object",
        "description": "Constituent as returned in GET /baskets list",
        "properties": {
          "symbol":          { "type": "string" },
          "targetWeightBps": { "type": "integer", "description": "Target weight in basis points, e.g. 5000 = 50%" },
          "sector":          { "type": "string" }
        }
      },
      "BasketConstituentDetail": {
        "type": "object",
        "description": "Constituent as returned in GET /baskets/:address. Weight fields are strings not integers.",
        "properties": {
          "address":          { "type": "string" },
          "symbol":           { "type": "string" },
          "sector":           { "type": "string" },
          "targetWeightBps":  { "type": "string", "description": "Target weight in bps as string, e.g. '5000'" },
          "currentWeightBps": { "type": "string", "description": "Live weight in bps as string computed from oracle prices, e.g. '4998'" },
          "balanceRaw":       { "type": "string", "description": "18-decimal constituent token balance as uint256 string" }
        }
      },
      "BasketSummary": {
        "type": "object",
        "properties": {
          "address":            { "type": "string", "description": "Basket proxy contract address, lowercase" },
          "creatorToken":       { "type": "string", "description": "Creator token contract address, lowercase" },
          "creator":            { "type": "string", "description": "Creator wallet address, lowercase" },
          "name":               { "type": "string", "example": "AI Infrastructure" },
          "symbol":             { "type": "string", "example": "AIIB" },
          "thesis":             { "type": "string" },
          "rebalancingEnabled": { "type": "boolean" },
          "driftThresholdBps":  { "type": "integer", "description": "Rebalance trigger in bps. 0 when rebalancingEnabled is false." },
          "createdAt":          { "type": "integer", "description": "Unix timestamp of basket creation" },
          "suspended":          { "type": "boolean", "description": "True if a constituent was deactivated by governance. Basket cannot accept deposits." },
          "navPerToken":        { "type": "string", "description": "Current NAV per basket token, 18-decimal USDG uint256 string" },
          "totalValueUsdg":     { "type": "string", "description": "Total AUM, 6-decimal USDG uint256 string" },
          "navChange24hPct":    { "type": "string", "description": "24h NAV change as percentage string. '0.00' when insufficient history." },
          "constituentCount":   { "type": "integer" },
          "constituents":       { "type": "array", "items": { "$ref": "#/components/schemas/BasketConstituentSummary" } }
        }
      },
      "BasketDetail": {
        "type": "object",
        "properties": {
          "address":            { "type": "string" },
          "creatorToken":       { "type": "string" },
          "creator":            { "type": "string" },
          "name":               { "type": "string" },
          "symbol":             { "type": "string" },
          "thesis":             { "type": "string" },
          "rebalancingEnabled": { "type": "boolean" },
          "driftThresholdBps":  { "type": "integer" },
          "createdAt":          { "type": "integer" },
          "suspended":          { "type": "boolean" },
          "navPerToken":        { "type": "string" },
          "totalValueUsdg":     { "type": "string" },
          "navChange24hPct":    { "type": "string" },
          "navChange7dPct":     { "type": "string" },
          "navChange30dPct":    { "type": "string" },
          "maxDriftBps":        { "type": "integer", "description": "Current maximum drift across all constituents in bps" },
          "needsRebalancing":   { "type": "boolean", "description": "True when maxDriftBps >= driftThresholdBps. Always false for static or suspended baskets." },
          "constituents":       { "type": "array", "items": { "$ref": "#/components/schemas/BasketConstituentDetail" } },
          "performanceHistory": { "type": "array", "description": "NAV history ordered by timestamp ascending. Empty array before first NAV poll (5 minutes after basket creation).", "items": { "$ref": "#/components/schemas/NavPoint" } },
          "rebalanceHistory":   { "type": "array", "items": { "$ref": "#/components/schemas/RebalanceEvent" } },
          "depositHistory":     { "type": "array", "items": { "$ref": "#/components/schemas/DepositEvent" } }
        }
      },
      "NavPoint": {
        "type": "object",
        "properties": {
          "navPerToken":    { "type": "string", "description": "18-decimal NAV per basket token" },
          "totalValueUsdg": { "type": "string", "description": "6-decimal total AUM" },
          "timestamp":      { "type": "integer", "description": "Unix timestamp" }
        }
      },
      "RebalanceEvent": {
        "type": "object",
        "properties": {
          "timestamp":   { "type": "integer" },
          "txHash":      { "type": "string" },
          "triggeredBy": { "type": "string", "description": "Wallet address that called rebalance()" }
        }
      },
      "DepositEvent": {
        "type": "object",
        "properties": {
          "investor":           { "type": "string", "description": "Investor wallet address" },
          "usdgAmount":         { "type": "string", "description": "6-decimal USDG deposited (before fee)" },
          "basketTokensMinted": { "type": "string", "description": "18-decimal basket tokens minted" },
          "timestamp":          { "type": "integer" },
          "txHash":             { "type": "string" }
        }
      },
      "InvestorPosition": {
        "type": "object",
        "properties": {
          "basketAddress":      { "type": "string" },
          "walletAddress":      { "type": "string" },
          "basketTokenBalance": { "type": "string", "description": "18-decimal basket token balance computed from deposit/redemption event history" },
          "currentValueUsdg":   { "type": "string", "description": "6-decimal current value = basketTokenBalance * navPerToken / 1e18" },
          "totalDepositedUsdg": { "type": "string", "description": "6-decimal sum of all USDG deposited by this wallet (before fees)" },
          "unrealisedPnlUsdg":  { "type": "string", "description": "6-decimal unrealised PnL = currentValueUsdg - totalDepositedUsdg. May be negative." },
          "unrealisedPnlPct":   { "type": "string", "description": "Formatted percentage string e.g. '-0.80'" }
        }
      },
      "PortfolioPosition": {
        "type": "object",
        "properties": {
          "basketAddress":      { "type": "string" },
          "basketName":         { "type": "string" },
          "basketSymbol":       { "type": "string" },
          "basketNavPerToken":  { "type": "string" },
          "rebalancingEnabled": { "type": "boolean" },
          "suspended":          { "type": "boolean" },
          "basketTokenBalance": { "type": "string" },
          "currentValueUsdg":   { "type": "string" },
          "totalDepositedUsdg": { "type": "string" },
          "unrealisedPnlUsdg":  { "type": "string" },
          "unrealisedPnlPct":   { "type": "string" }
        }
      },
      "PortfolioSummary": {
        "type": "object",
        "properties": {
          "walletAddress":          { "type": "string" },
          "totalValueUsdg":         { "type": "string", "description": "Sum of all position current values" },
          "totalDepositedUsdg":     { "type": "string", "description": "Sum of all position cost bases" },
          "totalUnrealisedPnlUsdg": { "type": "string" },
          "totalUnrealisedPnlPct":  { "type": "string" },
          "positions":              { "type": "array", "items": { "$ref": "#/components/schemas/PortfolioPosition" } }
        }
      },
      "RevenueSnapshot": {
        "type": "object",
        "properties": {
          "snapshotId": { "type": "integer", "description": "Monotonically increasing ID starting at 1" },
          "usdgAmount": { "type": "string", "description": "6-decimal USDG added to revenue pool at this snapshot" },
          "timestamp":  { "type": "integer" },
          "txHash":     { "type": "string" }
        }
      },
      "UnclaimedSnapshot": {
        "type": "object",
        "properties": {
          "snapshotId":        { "type": "integer" },
          "usdgAmount":        { "type": "string", "description": "Total USDG in this snapshot" },
          "timestamp":         { "type": "integer" },
          "claimableByWallet": { "type": "string", "description": "6-decimal USDG claimable by the queried wallet, proportional to their creator token balance at the snapshot block" }
        }
      },
      "CreatorBasket": {
        "type": "object",
        "properties": {
          "basketAddress":       { "type": "string" },
          "basketName":          { "type": "string" },
          "basketSymbol":        { "type": "string" },
          "creatorTokenAddress": { "type": "string" },
          "totalValueUsdg":      { "type": "string" },
          "totalClaimableUsdg":  { "type": "string", "description": "Sum of claimableByWallet across all unclaimedSnapshots" },
          "unclaimedSnapshots":  { "type": "array", "items": { "$ref": "#/components/schemas/UnclaimedSnapshot" } },
          "revenueHistory":      { "type": "array", "items": { "$ref": "#/components/schemas/RevenueSnapshot" } }
        }
      },
      "CreatorDashboard": {
        "type": "object",
        "properties": {
          "walletAddress":      { "type": "string" },
          "totalClaimableUsdg": { "type": "string", "description": "Sum of totalClaimableUsdg across all creator baskets" },
          "baskets":            { "type": "array", "items": { "$ref": "#/components/schemas/CreatorBasket" } }
        }
      },
      "CreatorTokenHistory": {
        "type": "object",
        "properties": {
          "creatorTokenAddress": { "type": "string" },
          "totalRevenueUsdg":    { "type": "string", "description": "Cumulative 6-decimal USDG distributed across all snapshots" },
          "snapshots":           { "type": "array", "items": { "$ref": "#/components/schemas/RevenueSnapshot" } }
        }
      },
      "ComposeRequest": {
        "type": "object",
        "required": ["thesis"],
        "properties": {
          "thesis": {
            "type": "string",
            "minLength": 20,
            "description": "Natural language investment thesis",
            "example": "companies building the physical infrastructure for AI including data centres, power, and semiconductor manufacturing"
          }
        }
      },
      "ComposeConstituent": {
        "type": "object",
        "properties": {
          "address":          { "type": "string", "description": "Token contract address from the active catalogue" },
          "symbol":           { "type": "string" },
          "name":             { "type": "string" },
          "sector":           { "type": "string" },
          "weightBps":        { "type": "integer", "minimum": 100, "maximum": 5000, "description": "Target weight in bps. All constituents sum to exactly 10000." },
          "rationale":        { "type": "string", "description": "One sentence explaining why this stock fits the thesis" },
          "currentPriceUsdg": { "type": "string", "description": "8-decimal oracle price at time of composition" }
        }
      },
      "ComposeResponse": {
        "type": "object",
        "properties": {
          "constituents":     { "type": "array", "minItems": 3, "maxItems": 12, "items": { "$ref": "#/components/schemas/ComposeConstituent" } },
          "overallRationale": { "type": "string", "description": "2-3 sentences explaining the basket construction logic" },
          "riskNotes":        { "type": "string", "description": "1-2 sentences noting key risks or concentration exposures" },
          "provider":         { "type": "string", "description": "Model identifier", "example": "openai/gpt-4.1-mini" }
        }
      },
      "Error": {
        "type": "object",
        "properties": {
          "error": { "type": "string" }
        }
      }
    },
    "responses": {
      "NotFound": {
        "description": "Resource not found",
        "content": {
          "application/json": {
            "schema": { "$ref": "#/components/schemas/Error" },
            "example": { "error": "basket not found" }
          }
        }
      }
    }
  }
}`

// swaggerHTML is a self-contained Swagger UI page served at GET /docs.
const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Weave API Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui.css">
  <style>
    * { box-sizing: border-box; }
    body { margin: 0; background: #0f0f0f; }
    .topbar { display: none !important; }
    #swagger-ui .swagger-ui { font-family: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif; }
    .swagger-ui .info .title { color: #ffffff; font-size: 2rem; font-weight: 700; }
    .swagger-ui .info .description p { color: #aaaaaa; }
    .swagger-ui .scheme-container { background: #1a1a1a; box-shadow: none; border-bottom: 1px solid #2a2a2a; padding: 16px 0; }
    .swagger-ui .opblock-tag { color: #ffffff; border-bottom: 1px solid #2a2a2a; }
    .swagger-ui .opblock-tag:hover { background: #1a1a1a; }
    .swagger-ui .opblock.opblock-get .opblock-summary-method { background: #1d4e89; }
    .swagger-ui .opblock.opblock-post .opblock-summary-method { background: #1e6b3e; }
    .swagger-ui .opblock { border: 1px solid #2a2a2a; border-radius: 6px; margin: 6px 0; background: #1a1a1a; }
    .swagger-ui .opblock .opblock-summary { border-bottom: none; }
    .swagger-ui .opblock.opblock-get { border-color: #1d4e89; background: rgba(29, 78, 137, 0.08); }
    .swagger-ui .opblock.opblock-post { border-color: #1e6b3e; background: rgba(30, 107, 62, 0.08); }
    .swagger-ui .opblock .opblock-summary-description { color: #cccccc; }
    .swagger-ui .opblock-body pre.microlight { background: #0f0f0f; color: #e0e0e0; }
    .swagger-ui section.models { border: 1px solid #2a2a2a; border-radius: 6px; }
    .swagger-ui section.models h4 { color: #ffffff; }
    .swagger-ui .model-title { color: #ffffff; }
    .swagger-ui .btn.execute { background: #6c47ff; border-color: #6c47ff; color: #ffffff; }
    .swagger-ui .btn.execute:hover { background: #5835e0; }
    .swagger-ui .btn.btn-clear { color: #aaaaaa; border-color: #444444; }
    #weave-header { display: flex; align-items: center; gap: 12px; padding: 20px 32px; background: #0f0f0f; border-bottom: 1px solid #2a2a2a; }
    #weave-header .logo { font-size: 1.4rem; font-weight: 800; color: #ffffff; letter-spacing: -0.5px; }
    #weave-header .logo span { color: #6c47ff; }
    #weave-header .badge { font-size: 0.7rem; padding: 2px 8px; background: #1a1a1a; border: 1px solid #2a2a2a; border-radius: 999px; color: #888888; }
    #weave-header .chain { margin-left: auto; font-size: 0.75rem; color: #666666; }
  </style>
</head>
<body>
  <div id="weave-header">
    <div class="logo">W<span>eave</span></div>
    <div class="badge">v1.0.0</div>
    <div class="badge">Robinhood Chain Testnet · 46630</div>
    <div class="chain">Onchain Index Protocol for Tokenized Equities</div>
  </div>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-bundle.js" crossorigin></script>
  <script src="https://unpkg.com/swagger-ui-dist@5.11.0/swagger-ui-standalone-preset.js" crossorigin></script>
  <script>
    window.onload = function() {
      SwaggerUIBundle({
        url: '/openapi.json',
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
        plugins: [SwaggerUIBundle.plugins.DownloadUrl],
        layout: 'BaseLayout',
        defaultModelsExpandDepth: 1,
        defaultModelExpandDepth: 2,
        displayRequestDuration: true,
        tryItOutEnabled: true,
        filter: true
      });
    };
  </script>
</body>
</html>`