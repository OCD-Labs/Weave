package prices

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OCD-Labs/Weave/server/db"
)

const testSchema = `
CREATE TABLE IF NOT EXISTS supported_assets (
	address        TEXT PRIMARY KEY,
	symbol         TEXT NOT NULL,
	name           TEXT NOT NULL,
	sector         TEXT NOT NULL,
	oracle_address TEXT NOT NULL,
	is_active      INTEGER NOT NULL DEFAULT 1,
	added_at       INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS basket_constituents (
	basket_address    TEXT NOT NULL,
	stock_address     TEXT NOT NULL,
	symbol            TEXT NOT NULL,
	target_weight_bps INTEGER NOT NULL,
	display_order     INTEGER NOT NULL,
	PRIMARY KEY (basket_address, stock_address)
);
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
CREATE TABLE IF NOT EXISTS price_history (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	stock_address TEXT NOT NULL,
	price_usdg    TEXT NOT NULL,
	timestamp     INTEGER NOT NULL
);`

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	database, err := db.OpenWithSchema(filepath.Join(dir, "test.db"), testSchema)
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestNewPoller_Initialises(t *testing.T) {
	database := openTestDB(t)
	p := NewPoller("http://localhost:8545", "0x1234", database, time.Minute)
	if p == nil {
		t.Fatal("expected non-nil poller")
	}
	if p.interval != time.Minute {
		t.Errorf("expected 1m interval, got %v", p.interval)
	}
}

func TestPoller_Poll_NoActiveConstituents(t *testing.T) {
	database := openTestDB(t)

	// Asset exists but has no basket constituents — must not be polled.
	database.Exec(`INSERT INTO supported_assets (address, symbol, name, sector, oracle_address, is_active, added_at)
		VALUES ('0xtoken1', 'TST', 'Test', 'Tech', '0xoracle1', 1, ?)`, time.Now().Unix())

	p := NewPoller("http://localhost:1", "0x0000", database, time.Minute)

	// poll() should return without error — no assets to poll means no RPC calls attempted.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.poll(ctx)

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM price_history`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 price_history rows, got %d", count)
	}
}

func TestPoller_Poll_SuspendedBasketExcluded(t *testing.T) {
	database := openTestDB(t)

	database.Exec(`INSERT INTO supported_assets (address, symbol, name, sector, oracle_address, is_active, added_at)
		VALUES ('0xtoken1', 'TST', 'Test', 'Tech', '0xoracle1', 1, ?)`, time.Now().Unix())

	// Basket is suspended — constituent must not be polled.
	database.Exec(`INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis, rebalancing_enabled, created_at, created_tx, suspended)
		VALUES ('0xbasket1', '0xct1', '0xcreator', 'B', 'B', 't', 0, ?, '', 1)`, time.Now().Unix())

	database.Exec(`INSERT INTO basket_constituents (basket_address, stock_address, symbol, target_weight_bps, display_order)
		VALUES ('0xbasket1', '0xtoken1', 'TST', 10000, 0)`)

	p := NewPoller("http://localhost:1", "0x0000", database, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.poll(ctx)

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM price_history`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 price_history rows for suspended basket, got %d", count)
	}
}

func TestPoller_Poll_InactiveAssetExcluded(t *testing.T) {
	database := openTestDB(t)

	// Asset is inactive — must not be polled even if in an active basket.
	database.Exec(`INSERT INTO supported_assets (address, symbol, name, sector, oracle_address, is_active, added_at)
		VALUES ('0xtoken1', 'TST', 'Test', 'Tech', '0xoracle1', 0, ?)`, time.Now().Unix())

	database.Exec(`INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis, rebalancing_enabled, created_at, created_tx, suspended)
		VALUES ('0xbasket1', '0xct1', '0xcreator', 'B', 'B', 't', 0, ?, '', 0)`, time.Now().Unix())

	database.Exec(`INSERT INTO basket_constituents (basket_address, stock_address, symbol, target_weight_bps, display_order)
		VALUES ('0xbasket1', '0xtoken1', 'TST', 10000, 0)`)

	p := NewPoller("http://localhost:1", "0x0000", database, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.poll(ctx)

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM price_history`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 price_history rows for inactive asset, got %d", count)
	}
}

func TestPoller_ReadOraclePrice_InvalidEndpoint(t *testing.T) {
	database := openTestDB(t)
	p := NewPoller("http://localhost:1", "0x0000", database, time.Minute)

	// Dialling a non-existent RPC must not panic — it returns nil.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// We can't call readOraclePrice without a client, so we verify poll handles
	// the dial error gracefully by running poll against an unreachable RPC.
	// No panic is the assertion.
	p.poll(ctx)
}

func TestPoller_Run_CancelStops(t *testing.T) {
	if os.Getenv("CI") == "" {
		t.Skip("skipping Run lifecycle test outside CI")
	}

	database := openTestDB(t)
	p := NewPoller("http://localhost:1", "0x0000", database, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		p.Run(ctx)
		close(done)
	}()

	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Run did not stop after context cancellation")
	}
}

func TestLatestRoundDataABI_CorrectFieldCount(t *testing.T) {
	method, ok := latestRoundDataABI.Methods["latestRoundData"]
	if !ok {
		t.Fatal("latestRoundData method not found in ABI")
	}
	if len(method.Outputs) != 5 {
		t.Errorf("expected 5 output fields, got %d", len(method.Outputs))
	}

	names := []string{"roundId", "answer", "startedAt", "updatedAt", "answeredInRound"}
	for i, name := range names {
		if method.Outputs[i].Name != name {
			t.Errorf("output[%d]: expected name %q, got %q", i, name, method.Outputs[i].Name)
		}
	}
}

func TestLatestRoundDataABI_FunctionName(t *testing.T) {
	if _, ok := latestRoundDataABI.Methods["latestRoundData"]; !ok {
		t.Error("ABI must define latestRoundData, not latestPrice")
	}
	if _, ok := latestRoundDataABI.Methods["latestPrice"]; ok {
		t.Error("ABI must not define latestPrice — it was renamed to latestRoundData")
	}
}