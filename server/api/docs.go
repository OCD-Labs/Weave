package api

// openAPISpec is the complete OpenAPI 3.0 specification for the Weave API.
// Served at GET /openapi.json and consumed by Swagger UI at GET /docs.
const openAPISpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Weave API",
    "description": "Onchain index protocol for tokenized equities on Robinhood Chain. Create thematic baskets of tokenized stocks, earn revenue as a basket creator, and get AI-assisted composition from natural language investment theses.",
    "version": "1.0.0",
    "contact": {
      "name": "OCD Labs",
      "url": "https://github.com/OCD-Labs/Weave"
    }
  },
  "servers": [
    {
      "url": "https://weave.up.railway.app",
      "description": "Production"
    },
    {
      "url": "http://localhost:8080",
      "description": "Local development"
    }
  ],
  "tags": [
    { "name": "Catalogue", "description": "Tokenized stock assets available for basket construction" },
    { "name": "Baskets", "description": "Published investment baskets" },
    { "name": "Portfolio", "description": "Investor positions across baskets" },
    { "name": "Creator", "description": "Basket creator revenue and dashboards" },
    { "name": "AI", "description": "AI-assisted basket composition" }
  ],
  "paths": {
    "/catalogue": {
      "get": {
        "tags": ["Catalogue"],
        "summary": "List all active assets",
        "description": "Returns all tokenized stock assets available for basket construction, with their latest oracle prices.",
        "operationId": "getCatalogue",
        "responses": {
          "200": {
            "description": "Array of active assets",
            "content": {
              "application/json": {
                "schema": {
                  "type": "array",
                  "items": { "$ref": "#/components/schemas/CatalogueAsset" }
                },
                "example": [
                  {
                    "address": "0x71178bac73cbeb415514eb542a8995b82669778d",
                    "symbol": "AMD",
                    "name": "Advanced Micro Devices Inc",
                    "sector": "Technology",
                    "oracle": "0xbb4fb68f13425155d72813f91e703efc811edf77",
                    "isActive": true,
                    "currentPriceUsdg": "11000000000",
                    "priceUpdatedAt": 1748720000
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
            "description": "Asset details",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/CatalogueAsset" }
              }
            }
          },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      }
    },
    "/prices": {
      "get": {
        "tags": ["Catalogue"],
        "summary": "Latest oracle prices",
        "description": "Returns the latest oracle price for every active asset. Lighter than /catalogue when only prices are needed.",
        "operationId": "getPrices",
        "responses": {
          "200": {
            "description": "Latest prices",
            "content": {
              "application/json": {
                "schema": {
                  "type": "array",
                  "items": { "$ref": "#/components/schemas/Price" }
                }
              }
            }
          }
        }
      }
    },
    "/baskets": {
      "get": {
        "tags": ["Baskets"],
        "summary": "List all baskets",
        "description": "Returns all published baskets with current NAV and AUM metrics.",
        "operationId": "listBaskets",
        "responses": {
          "200": {
            "description": "Array of baskets",
            "content": {
              "application/json": {
                "schema": {
                  "type": "array",
                  "items": { "$ref": "#/components/schemas/BasketSummary" }
                }
              }
            }
          }
        }
      }
    },
    "/baskets/{address}": {
      "get": {
        "tags": ["Baskets"],
        "summary": "Get a single basket",
        "operationId": "getBasket",
        "parameters": [
          {
            "name": "address",
            "in": "path",
            "required": true,
            "description": "Basket contract address",
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Basket details",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/BasketSummary" }
              }
            }
          },
          "404": { "$ref": "#/components/responses/NotFound" }
        }
      }
    },
    "/baskets/{address}/performance": {
      "get": {
        "tags": ["Baskets"],
        "summary": "NAV history",
        "description": "Returns the NAV per token time series for a basket.",
        "operationId": "getBasketPerformance",
        "parameters": [
          {
            "name": "address",
            "in": "path",
            "required": true,
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "NAV history points",
            "content": {
              "application/json": {
                "schema": {
                  "type": "array",
                  "items": { "$ref": "#/components/schemas/NavPoint" }
                }
              }
            }
          }
        }
      }
    },
    "/baskets/{address}/positions/{wallet}": {
      "get": {
        "tags": ["Baskets"],
        "summary": "Investor position in a basket",
        "description": "Returns a wallet's position in a specific basket computed from on-chain deposit and redemption history.",
        "operationId": "getPosition",
        "parameters": [
          {
            "name": "address",
            "in": "path",
            "required": true,
            "description": "Basket contract address",
            "schema": { "type": "string" }
          },
          {
            "name": "wallet",
            "in": "path",
            "required": true,
            "description": "Investor wallet address",
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Position summary",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/Position" }
              }
            }
          }
        }
      }
    },
    "/positions/{wallet}": {
      "get": {
        "tags": ["Portfolio"],
        "summary": "All positions for a wallet",
        "description": "Returns a summary of all basket positions held by a wallet across the entire protocol.",
        "operationId": "getPortfolio",
        "parameters": [
          {
            "name": "wallet",
            "in": "path",
            "required": true,
            "description": "Investor wallet address",
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Portfolio summary",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/Portfolio" }
              }
            }
          }
        }
      }
    },
    "/creator/{wallet}": {
      "get": {
        "tags": ["Creator"],
        "summary": "Creator dashboard",
        "description": "Returns all baskets created by a wallet and their associated creator token addresses.",
        "operationId": "getCreatorDashboard",
        "parameters": [
          {
            "name": "wallet",
            "in": "path",
            "required": true,
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Creator dashboard",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/CreatorDashboard" }
              }
            }
          }
        }
      }
    },
    "/creator-tokens/{address}": {
      "get": {
        "tags": ["Creator"],
        "summary": "Creator token revenue history",
        "description": "Returns all ERC-7641 revenue snapshots for a creator token contract.",
        "operationId": "getCreatorToken",
        "parameters": [
          {
            "name": "address",
            "in": "path",
            "required": true,
            "description": "Creator token contract address",
            "schema": { "type": "string" }
          }
        ],
        "responses": {
          "200": {
            "description": "Revenue snapshot history",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/CreatorTokenHistory" }
              }
            }
          }
        }
      }
    },
    "/ai/compose": {
      "post": {
        "tags": ["AI"],
        "summary": "AI basket composition",
        "description": "Submits a natural language investment thesis and returns an AI-generated basket composition proposal. No on-chain action is taken — the proposal is for human review before basket deployment.",
        "operationId": "aiCompose",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/ComposeRequest" },
              "example": {
                "thesis": "companies building the physical infrastructure for AI including data centres, power, and semiconductor manufacturing"
              }
            }
          }
        },
        "responses": {
          "200": {
            "description": "Basket composition proposal",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ComposeResponse" }
              }
            }
          },
          "400": { "$ref": "#/components/responses/BadRequest" },
          "502": { "$ref": "#/components/responses/BadGateway" },
          "503": { "$ref": "#/components/responses/ServiceUnavailable" }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "CatalogueAsset": {
        "type": "object",
        "properties": {
          "address":          { "type": "string", "description": "ERC-20 token address on Robinhood Chain" },
          "symbol":           { "type": "string", "description": "Ticker symbol", "example": "AMD" },
          "name":             { "type": "string", "description": "Full company name", "example": "Advanced Micro Devices Inc" },
          "sector":           { "type": "string", "description": "GICS sector classification", "example": "Technology" },
          "oracle":           { "type": "string", "description": "IWeaveOracle contract address" },
          "isActive":         { "type": "boolean" },
          "currentPriceUsdg": { "type": "string", "description": "Latest oracle price as raw uint256 string. 8 decimal places (Chainlink convention). Divide by 1e8 to get USD." },
          "priceUpdatedAt":   { "type": "integer", "description": "Unix timestamp of last price update" }
        }
      },
      "Price": {
        "type": "object",
        "properties": {
          "address":   { "type": "string" },
          "priceUsdg": { "type": "string", "description": "Raw uint256 price string, 8 decimals" },
          "updatedAt": { "type": "integer" }
        }
      },
      "BasketSummary": {
        "type": "object",
        "properties": {
          "address":            { "type": "string" },
          "creatorToken":       { "type": "string" },
          "creator":            { "type": "string" },
          "name":               { "type": "string" },
          "symbol":             { "type": "string" },
          "thesis":             { "type": "string" },
          "rebalancingEnabled": { "type": "boolean" },
          "driftThresholdBps":  { "type": "integer", "nullable": true, "description": "Rebalance trigger in basis points. Null for static baskets." },
          "createdAt":          { "type": "integer" },
          "suspended":          { "type": "boolean", "description": "True if a constituent was deactivated. Basket cannot accept deposits." },
          "navPerToken":        { "type": "string", "description": "Current NAV per basket token, 18-decimal USDG, raw uint256 string" },
          "totalValueUsdg":     { "type": "string", "description": "Total AUM, 6-decimal USDG, raw uint256 string" }
        }
      },
      "NavPoint": {
        "type": "object",
        "properties": {
          "navPerToken":    { "type": "string" },
          "totalValueUsdg": { "type": "string" },
          "timestamp":      { "type": "integer" }
        }
      },
      "Position": {
        "type": "object",
        "properties": {
          "basketAddress":     { "type": "string" },
          "walletAddress":     { "type": "string" },
          "totalDepositedUsdg": { "type": "string" },
          "totalRedeemedUsdg":  { "type": "string" },
          "netCostBasisUsdg":   { "type": "string" }
        }
      },
      "Portfolio": {
        "type": "object",
        "properties": {
          "walletAddress": { "type": "string" },
          "positions": {
            "type": "array",
            "items": { "$ref": "#/components/schemas/Position" }
          }
        }
      },
      "CreatorDashboard": {
        "type": "object",
        "properties": {
          "walletAddress": { "type": "string" },
          "baskets": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "basketAddress":      { "type": "string" },
                "creatorTokenAddress": { "type": "string" },
                "name":               { "type": "string" },
                "symbol":             { "type": "string" }
              }
            }
          }
        }
      },
      "CreatorTokenHistory": {
        "type": "object",
        "properties": {
          "creatorTokenAddress": { "type": "string" },
          "totalRevenueUsdg":    { "type": "string" },
          "snapshots": {
            "type": "array",
            "items": {
              "type": "object",
              "properties": {
                "snapshotId": { "type": "integer" },
                "usdgAmount": { "type": "string" },
                "timestamp":  { "type": "integer" },
                "txHash":     { "type": "string" }
              }
            }
          }
        }
      },
      "ComposeRequest": {
        "type": "object",
        "required": ["thesis"],
        "properties": {
          "thesis": {
            "type": "string",
            "minLength": 20,
            "maxLength": 2000,
            "description": "Natural language investment thesis describing the thematic exposure you want to create"
          }
        }
      },
      "ComposeResponse": {
        "type": "object",
        "properties": {
          "constituents": {
            "type": "array",
            "minItems": 3,
            "maxItems": 12,
            "items": {
              "type": "object",
              "properties": {
                "address":          { "type": "string" },
                "symbol":           { "type": "string" },
                "name":             { "type": "string" },
                "sector":           { "type": "string" },
                "weightBps":        { "type": "integer", "minimum": 100, "maximum": 5000, "description": "Target weight in basis points. All weights sum to exactly 10000." },
                "rationale":        { "type": "string", "description": "One sentence explaining why this stock fits the thesis" },
                "currentPriceUsdg": { "type": "string" }
              }
            }
          },
          "overallRationale": { "type": "string" },
          "riskNotes":        { "type": "string" },
          "provider":         { "type": "string", "enum": ["openai", "anthropic", "ollama"], "description": "Which LLM served this response" }
        }
      },
      "Error": {
        "type": "object",
        "properties": {
          "error": { "type": "string" },
          "code":  { "type": "integer" }
        }
      }
    },
    "responses": {
      "NotFound": {
        "description": "Resource not found",
        "content": {
          "application/json": {
            "schema": { "$ref": "#/components/schemas/Error" },
            "example": { "error": "basket not found", "code": 404 }
          }
        }
      },
      "BadRequest": {
        "description": "Invalid request body",
        "content": {
          "application/json": {
            "schema": { "$ref": "#/components/schemas/Error" },
            "example": { "error": "Invalid request body", "code": 400 }
          }
        }
      },
      "BadGateway": {
        "description": "AI provider error or invalid response after retry",
        "content": {
          "application/json": {
            "schema": { "$ref": "#/components/schemas/Error" }
          }
        }
      },
      "ServiceUnavailable": {
        "description": "Fewer than 3 active assets in catalogue",
        "content": {
          "application/json": {
            "schema": { "$ref": "#/components/schemas/Error" }
          }
        }
      }
    }
  }
}`

// swaggerHTML is a self-contained Swagger UI page served at GET /docs.
// Uses unpkg CDN for swagger-ui-dist@5.11.0 — no build step required.
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

    #swagger-ui .swagger-ui {
      font-family: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif;
    }

    .swagger-ui .info .title {
      color: #ffffff;
      font-size: 2rem;
      font-weight: 700;
    }

    .swagger-ui .info .description p {
      color: #aaaaaa;
    }

    .swagger-ui .scheme-container {
      background: #1a1a1a;
      box-shadow: none;
      border-bottom: 1px solid #2a2a2a;
      padding: 16px 0;
    }

    .swagger-ui .opblock-tag {
      color: #ffffff;
      border-bottom: 1px solid #2a2a2a;
    }

    .swagger-ui .opblock-tag:hover {
      background: #1a1a1a;
    }

    .swagger-ui .opblock.opblock-get .opblock-summary-method {
      background: #1d4e89;
    }

    .swagger-ui .opblock.opblock-post .opblock-summary-method {
      background: #1e6b3e;
    }

    .swagger-ui .opblock {
      border: 1px solid #2a2a2a;
      border-radius: 6px;
      margin: 6px 0;
      background: #1a1a1a;
    }

    .swagger-ui .opblock .opblock-summary {
      border-bottom: none;
    }

    .swagger-ui .opblock.opblock-get {
      border-color: #1d4e89;
      background: rgba(29, 78, 137, 0.08);
    }

    .swagger-ui .opblock.opblock-post {
      border-color: #1e6b3e;
      background: rgba(30, 107, 62, 0.08);
    }

    .swagger-ui .opblock .opblock-summary-description {
      color: #cccccc;
    }

    .swagger-ui .opblock-body pre.microlight {
      background: #0f0f0f;
      color: #e0e0e0;
    }

    .swagger-ui section.models {
      border: 1px solid #2a2a2a;
      border-radius: 6px;
    }

    .swagger-ui section.models h4 {
      color: #ffffff;
    }

    .swagger-ui .model-title {
      color: #ffffff;
    }

    .swagger-ui .btn.execute {
      background: #6c47ff;
      border-color: #6c47ff;
      color: #ffffff;
    }

    .swagger-ui .btn.execute:hover {
      background: #5835e0;
    }

    .swagger-ui .btn.btn-clear {
      color: #aaaaaa;
      border-color: #444444;
    }

    #weave-header {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 20px 32px;
      background: #0f0f0f;
      border-bottom: 1px solid #2a2a2a;
    }

    #weave-header .logo {
      font-size: 1.4rem;
      font-weight: 800;
      color: #ffffff;
      letter-spacing: -0.5px;
    }

    #weave-header .logo span {
      color: #6c47ff;
    }

    #weave-header .badge {
      font-size: 0.7rem;
      padding: 2px 8px;
      background: #1a1a1a;
      border: 1px solid #2a2a2a;
      border-radius: 999px;
      color: #888888;
    }

    #weave-header .chain {
      margin-left: auto;
      font-size: 0.75rem;
      color: #666666;
    }
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
        presets: [
          SwaggerUIBundle.presets.apis,
          SwaggerUIBundle.SwaggerUIStandalonePreset
        ],
        plugins: [
          SwaggerUIBundle.plugins.DownloadUrl
        ],
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