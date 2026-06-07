package db

import (
	"fmt"
	"strings"
)

// Migrate applies schema migrations that cannot be expressed as CREATE TABLE IF NOT EXISTS
// in schema.sql because they modify existing tables on a live Railway volume.
//
// Each migration is idempotent:
//   - ALTER TABLE statements are skipped if the column already exists
//     (SQLite returns "duplicate column name" in that case).
//   - CREATE UNIQUE INDEX IF NOT EXISTS is safe to re-run.
//   - CREATE TABLE IF NOT EXISTS is safe to re-run.
//
// Call Migrate() in main.go immediately after db.Open() and before starting
// the indexer. A migration failure is fatal — the process must not start with
// an incomplete schema because the indexer will write log_index columns that
// do not exist and every event insert will fail.
func (d *DB) Migrate() error {
	migrations := []struct {
		name string
		sql  string
	}{
		// Add log_index to event tables for idempotent re-scan inserts.
		// The DEFAULT 0 means existing rows (which have no log_index) get 0,
		// which is safe — existing rows pre-date the unique constraint and will
		// not conflict with new inserts that carry real log_index values.
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
		// Unique indexes that enforce idempotency on re-scans after restarts.
		// These are the primary defence against duplicate financial records.
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
		// Seed failure tracker so the indexer does not retry permanently
		// broken baskets on every startup.
		{
			name: "basket_seed_failures",
			sql: `CREATE TABLE IF NOT EXISTS basket_seed_failures (
				basket_address TEXT PRIMARY KEY,
				attempts       INTEGER NOT NULL DEFAULT 0,
				last_attempt   INTEGER NOT NULL
			)`,
		},
	}

	for _, m := range migrations {
		if _, err := d.Exec(m.sql); err != nil {
			// SQLite returns "duplicate column name: <col>" when ALTER TABLE
			// tries to add a column that already exists. That is not an error —
			// it means this migration already ran on a previous startup.
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			// Every other error is fatal. An incomplete migration leaves the
			// schema in a state where event inserts will fail.
			return fmt.Errorf("migration %q failed: %w", m.name, err)
		}
	}

	return nil
}