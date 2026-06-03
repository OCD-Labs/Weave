-- SQLite schema for the Weave backend indexer.
-- Applied once on first startup via the Go migration runner.

CREATE TABLE IF NOT EXISTS baskets (
    address               TEXT PRIMARY KEY,
    creator_token_address TEXT NOT NULL,
    creator_address       TEXT NOT NULL,
    name                  TEXT NOT NULL,
    symbol                TEXT NOT NULL,
    thesis                TEXT NOT NULL,
    rebalancing_enabled   INTEGER NOT NULL,
    drift_threshold_bps   INTEGER,
    created_at            INTEGER NOT NULL,
    created_tx            TEXT NOT NULL,
    suspended             INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS basket_constituents (
    basket_address    TEXT NOT NULL,
    stock_address     TEXT NOT NULL,
    symbol            TEXT NOT NULL,
    target_weight_bps INTEGER NOT NULL,
    display_order     INTEGER NOT NULL,
    PRIMARY KEY (basket_address, stock_address)
);

CREATE TABLE IF NOT EXISTS deposits (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address       TEXT NOT NULL,
    investor_address     TEXT NOT NULL,
    usdg_amount          TEXT NOT NULL,
    basket_tokens_minted TEXT NOT NULL,
    fee_usdg             TEXT NOT NULL,
    timestamp            INTEGER NOT NULL,
    tx_hash              TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS redemptions (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address       TEXT NOT NULL,
    investor_address     TEXT NOT NULL,
    basket_tokens_burned TEXT NOT NULL,
    usdg_returned        TEXT NOT NULL,
    fee_usdg             TEXT NOT NULL,
    timestamp            INTEGER NOT NULL,
    tx_hash              TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rebalances (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address TEXT NOT NULL,
    triggered_by   TEXT NOT NULL,
    timestamp      INTEGER NOT NULL,
    tx_hash        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS fee_snapshots (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address TEXT NOT NULL,
    snapshot_id    INTEGER NOT NULL,
    usdg_amount    TEXT NOT NULL,
    timestamp      INTEGER NOT NULL,
    tx_hash        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS supported_assets (
    address        TEXT PRIMARY KEY,
    symbol         TEXT NOT NULL,
    name           TEXT NOT NULL,
    sector         TEXT NOT NULL,
    oracle_address TEXT NOT NULL,
    is_active      INTEGER NOT NULL DEFAULT 1,
    added_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS price_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    stock_address TEXT NOT NULL,
    price_usdg    TEXT NOT NULL,
    timestamp     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS nav_history (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address   TEXT NOT NULL,
    nav_per_token    TEXT NOT NULL,
    total_value_usdg TEXT NOT NULL,
    timestamp        INTEGER NOT NULL
);

-- Cursor table for event block scanning. Tracks the last processed block
-- per named stream so restarts resume from where they left off rather than
-- re-scanning the entire chain history.
CREATE TABLE IF NOT EXISTS sync_cursors (
    key       TEXT PRIMARY KEY,
    block_num INTEGER NOT NULL DEFAULT 0
);

-- Database-backed cache for live basket state fetched via basketState() RPC.
-- Shared across all backend instances — any instance can serve a cache hit.
-- cached_at is a Unix timestamp; entries older than 30 seconds are stale.
CREATE TABLE IF NOT EXISTS basket_state_cache (
    basket_address       TEXT PRIMARY KEY,
    constituents_json    TEXT NOT NULL,
    current_weights_json TEXT NOT NULL,
    balances_json        TEXT NOT NULL,
    total_value_usdg     TEXT NOT NULL,
    nav_per_token        TEXT NOT NULL,
    max_drift_bps        INTEGER NOT NULL,
    needs_rebalancing    INTEGER NOT NULL,
    cached_at            INTEGER NOT NULL
);

-- Database-backed cache for claimable revenue amounts per (wallet, snapshot).
-- Populated on first creator dashboard load; refreshed after 60 seconds.
-- Eliminates repeated on-chain calls for unchanged snapshot data.
CREATE TABLE IF NOT EXISTS creator_claimable_cache (
    wallet_address TEXT NOT NULL,
    snapshot_id    INTEGER NOT NULL,
    basket_address TEXT NOT NULL,
    claimable_usdg TEXT NOT NULL,
    cached_at      INTEGER NOT NULL,
    PRIMARY KEY (wallet_address, snapshot_id, basket_address)
);

-- Indexes for the query patterns the API uses most.
CREATE INDEX IF NOT EXISTS idx_deposits_basket      ON deposits(basket_address);
CREATE INDEX IF NOT EXISTS idx_deposits_investor    ON deposits(investor_address);
CREATE INDEX IF NOT EXISTS idx_redemptions_basket   ON redemptions(basket_address);
CREATE INDEX IF NOT EXISTS idx_redemptions_investor ON redemptions(investor_address);
CREATE INDEX IF NOT EXISTS idx_price_history_addr   ON price_history(stock_address, timestamp);
CREATE INDEX IF NOT EXISTS idx_nav_history_basket   ON nav_history(basket_address, timestamp);
CREATE INDEX IF NOT EXISTS idx_fee_snapshots_basket ON fee_snapshots(basket_address);
CREATE INDEX IF NOT EXISTS idx_basket_state_cache   ON basket_state_cache(cached_at);
CREATE INDEX IF NOT EXISTS idx_creator_claimable    ON creator_claimable_cache(wallet_address, cached_at);