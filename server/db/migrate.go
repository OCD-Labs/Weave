package db

import (
	"fmt"
	"strings"
)

// Migrate applies schema migrations that cannot be expressed as CREATE TABLE IF NOT EXISTS
// in schema.sql because they modify existing tables on a live Railway volume.
//
// Each migration is idempotent:
//   - ALTER TABLE statements are skipped if the column already exists.
//   - CREATE UNIQUE INDEX IF NOT EXISTS is safe to re-run.
//   - CREATE TABLE IF NOT EXISTS is safe to re-run.
//   - UPDATE/DELETE statements whose effects are already applied are no-ops.
//
// Call Migrate() in main.go immediately after db.Open() and before starting
// the indexer. A migration failure is fatal.
func (d *DB) Migrate() error {
	migrations := []struct {
		name string
		sql  string
	}{
		// Phase 1 — add log_index to event tables for idempotent re-scan inserts.
		{
			name: "deposits.log_index",
			sql:  `ALTER TABLE deposits ADD COLUMN log_index INTEGER NOT NULL DEFAULT 0`,
		},
		{
			name: "redemptions.log_index",
			sql:  `ALTER TABLE redemptions ADD COLUMN log_index INTEGER NOT NULL DEFAULT 0`,
		},
		{
			name: "rebalances.log_index",
			sql:  `ALTER TABLE rebalances ADD COLUMN log_index INTEGER NOT NULL DEFAULT 0`,
		},
		{
			name: "fee_snapshots.log_index",
			sql:  `ALTER TABLE fee_snapshots ADD COLUMN log_index INTEGER NOT NULL DEFAULT 0`,
		},
		// Unique deduplication indexes.
		{
			name: "idx_deposits_dedup",
			sql:  `CREATE UNIQUE INDEX IF NOT EXISTS idx_deposits_dedup ON deposits(tx_hash, log_index)`,
		},
		{
			name: "idx_redemptions_dedup",
			sql:  `CREATE UNIQUE INDEX IF NOT EXISTS idx_redemptions_dedup ON redemptions(tx_hash, log_index)`,
		},
		{
			name: "idx_rebalances_dedup",
			sql:  `CREATE UNIQUE INDEX IF NOT EXISTS idx_rebalances_dedup ON rebalances(tx_hash, log_index)`,
		},
		{
			name: "idx_fee_snapshots_dedup",
			sql:  `CREATE UNIQUE INDEX IF NOT EXISTS idx_fee_snapshots_dedup ON fee_snapshots(tx_hash, log_index)`,
		},
		// Seed failure tracker.
		{
			name: "basket_seed_failures",
			sql: `CREATE TABLE IF NOT EXISTS basket_seed_failures (
				basket_address TEXT PRIMARY KEY,
				attempts       INTEGER NOT NULL DEFAULT 0,
				last_attempt   INTEGER NOT NULL
			)`,
		},

		// Phase 2 — fix RevenueSnapshoted indexing.
		//
		// Root cause: the indexer never included CreatorToken contract addresses in
		// its FilterLogs/WebSocket subscription filter, so every RevenueSnapshoted
		// event was silently dropped. Additionally, even if the event had been
		// received, handleFeeSnapshot stored vLog.Address (the CreatorToken address)
		// as basket_address — the wrong value.
		//
		// Fix applied in indexer.go:
		//   1. creatorTokenToBasket map tracks CreatorToken → basket address.
		//   2. filterAddresses() now includes all CreatorToken addresses.
		//   3. writeFeeSnapshot resolves the basket from creatorTokenToBasket (DB fallback).
		//
		// To make the already-emitted RevenueSnapshoted events visible we must:
		//   a. Delete any fee_snapshot rows written with the wrong basket_address
		//      (i.e. rows whose basket_address is a creator token address, not a basket).
		//      These were stored under the creator token address due to the old bug.
		//   b. Reset the event cursor to the deploy block so the historical scan
		//      replays from the beginning and re-indexes all events including the
		//      missed RevenueSnapshoted events with the correct basket_address.
		//   c. Clear the creator_claimable_cache so stale zeros are not returned
		//      while the rescan is in progress.
		//
		// All three steps are idempotent: deleting already-absent rows is a no-op,
		// setting the cursor to a value it already has is a no-op, and deleting
		// cache rows that don't exist is a no-op.
		{
			name: "fix_fee_snapshots_wrong_basket_address",
			sql: `DELETE FROM fee_snapshots
				WHERE basket_address NOT IN (SELECT address FROM baskets)`,
		},
		{
			name: "reset_event_cursor_for_creator_token_rescan",
			sql:  `UPDATE sync_cursors SET block_num = 68391146 WHERE key = 'events'`,
		},
		{
			name: "clear_creator_claimable_cache",
			sql:  `DELETE FROM creator_claimable_cache`,
		},
		// Index creator_token_address so the writeFeeSnapshot DB fallback
		// (SELECT address FROM baskets WHERE creator_token_address = ?) is
		// an index seek rather than a full table scan during historical rescan.
		{
			name: "idx_baskets_creator_token",
			sql:  `CREATE INDEX IF NOT EXISTS idx_baskets_creator_token ON baskets(creator_token_address)`,
		},
	}

	for _, m := range migrations {
		if _, err := d.Exec(m.sql); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("migration %q failed: %w", m.name, err)
		}
	}

	return nil
}