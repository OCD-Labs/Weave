package db

import (
	"fmt"
	"strings"
)

// Migrate applies schema migrations that cannot be expressed as CREATE TABLE IF NOT EXISTS
// in schema.sql because they modify existing tables on a live volume.
func (d *DB) Migrate() error {
	migrations := []struct {
		name string
		sql  string
	}{
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
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("migration %q failed: %w", m.name, err)
		}
	}

	return nil
}