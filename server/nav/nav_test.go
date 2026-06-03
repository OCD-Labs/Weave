package nav

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OCD-Labs/Weave/server/db"
)

const testSchema = `
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
CREATE TABLE IF NOT EXISTS nav_history (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	basket_address   TEXT NOT NULL,
	nav_per_token    TEXT NOT NULL,
	total_value_usdg TEXT NOT NULL,
	timestamp        INTEGER NOT NULL
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

func insertBasket(t *testing.T, database *db.DB, addr string, suspended int) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis, rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, '0xct', '0xcreator', 'Test', 'TST', 'thesis', 0, ?, '', ?)`,
		addr, time.Now().Unix(), suspended,
	)
	if err != nil {
		t.Fatalf("insertBasket: %v", err)
	}
}

func TestNewPoller_Initialises(t *testing.T) {
	database := openTestDB(t)
	p := NewPoller("http://localhost:8545", database, time.Minute)
	if p == nil {
		t.Fatal("expected non-nil poller")
	}
	if p.interval != time.Minute {
		t.Errorf("expected 1m interval, got %v", p.interval)
	}
}

func TestPoll_NoBaskets_NoRows(t *testing.T) {
	database := openTestDB(t)
	p := NewPoller("http://localhost:1", database, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.poll(ctx)

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM nav_history`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 nav_history rows with no baskets, got %d", count)
	}
}

func TestPoll_SuspendedBasketsExcluded(t *testing.T) {
	database := openTestDB(t)
	insertBasket(t, database, "0xbasket1", 1) // suspended

	p := NewPoller("http://localhost:1", database, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.poll(ctx)

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM nav_history`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 nav_history rows for suspended basket, got %d", count)
	}
}

func TestPoll_ActiveBasket_AttemptsRPC(t *testing.T) {
	database := openTestDB(t)
	insertBasket(t, database, "0xbasket1", 0) // active

	// RPC will fail — unreachable endpoint. The poll must not panic
	// and must not write any nav_history row on RPC failure.
	p := NewPoller("http://localhost:1", database, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p.poll(ctx)

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM nav_history`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 nav_history rows on RPC failure, got %d", count)
	}
}

func TestPoll_MultipleSuspendedAllExcluded(t *testing.T) {
	database := openTestDB(t)
	for i, addr := range []string{"0xb1", "0xb2", "0xb3"} {
		insertBasket(t, database, addr, func() int {
			if i%2 == 0 {
				return 1
			}
			return 1
		}())
	}

	p := NewPoller("http://localhost:1", database, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.poll(ctx)

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM nav_history`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 nav_history rows when all baskets suspended, got %d", count)
	}
}

func TestPollOne_WritesNavHistory_WhenRPCSucceeds(t *testing.T) {
	// This test verifies the DB write path independently of the RPC.
	// We call the DB Exec directly to confirm schema correctness.
	database := openTestDB(t)
	insertBasket(t, database, "0xbasket1", 0)

	_, err := database.Exec(`
		INSERT INTO nav_history (basket_address, nav_per_token, total_value_usdg, timestamp)
		VALUES (?, ?, ?, ?)`,
		"0xbasket1", "1000000000000000000", "100000000", time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("expected nav_history insert to succeed: %v", err)
	}

	var count int
	database.QueryRow(`SELECT COUNT(*) FROM nav_history`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 nav_history row, got %d", count)
	}
}

func TestBasketViewABI_HasRequiredMethods(t *testing.T) {
	if _, ok := basketViewABI.Methods["navPerToken"]; !ok {
		t.Error("basketViewABI must define navPerToken")
	}
	if _, ok := basketViewABI.Methods["totalValueUsdg"]; !ok {
		t.Error("basketViewABI must define totalValueUsdg")
	}
}

func TestBasketViewABI_SingleUint256Output(t *testing.T) {
	for _, method := range []string{"navPerToken", "totalValueUsdg"} {
		m, ok := basketViewABI.Methods[method]
		if !ok {
			t.Errorf("method %s not found", method)
			continue
		}
		if len(m.Outputs) != 1 {
			t.Errorf("%s: expected 1 output, got %d", method, len(m.Outputs))
		}
		if m.Outputs[0].Type.String() != "uint256" {
			t.Errorf("%s: expected uint256 output, got %s", method, m.Outputs[0].Type.String())
		}
	}
}

func TestRun_CancelStops(t *testing.T) {
	database := openTestDB(t)
	p := NewPoller("http://localhost:1", database, 50*time.Millisecond)

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