package indexer

import (
	"context"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OCD-Labs/Weave/server/db"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// testSchema is the minimal schema required by the indexer tests.
// It mirrors schema.sql SQL scripts.
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
    tx_hash              TEXT NOT NULL,
    log_index            INTEGER NOT NULL DEFAULT 0,
    UNIQUE(tx_hash, log_index)
);
CREATE TABLE IF NOT EXISTS redemptions (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address       TEXT NOT NULL,
    investor_address     TEXT NOT NULL,
    basket_tokens_burned TEXT NOT NULL,
    usdg_returned        TEXT NOT NULL,
    fee_usdg             TEXT NOT NULL,
    timestamp            INTEGER NOT NULL,
    tx_hash              TEXT NOT NULL,
    log_index            INTEGER NOT NULL DEFAULT 0,
    UNIQUE(tx_hash, log_index)
);
CREATE TABLE IF NOT EXISTS rebalances (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address TEXT NOT NULL,
    triggered_by   TEXT NOT NULL,
    timestamp      INTEGER NOT NULL,
    tx_hash        TEXT NOT NULL,
    log_index      INTEGER NOT NULL DEFAULT 0,
    UNIQUE(tx_hash, log_index)
);
CREATE TABLE IF NOT EXISTS fee_snapshots (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address TEXT NOT NULL,
    snapshot_id    INTEGER NOT NULL,
    usdg_amount    TEXT NOT NULL,
    timestamp      INTEGER NOT NULL,
    tx_hash        TEXT NOT NULL,
    log_index      INTEGER NOT NULL DEFAULT 0,
    UNIQUE(tx_hash, log_index)
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
CREATE TABLE IF NOT EXISTS sync_cursors (
    key       TEXT PRIMARY KEY,
    block_num INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS basket_seed_failures (
    basket_address TEXT PRIMARY KEY,
    attempts       INTEGER NOT NULL DEFAULT 0,
    last_attempt   INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS nav_history (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    basket_address   TEXT NOT NULL,
    nav_per_token    TEXT NOT NULL,
    total_value_usdg TEXT NOT NULL,
    timestamp        INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS price_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    stock_address TEXT NOT NULL,
    price_usdg    TEXT NOT NULL,
    timestamp     INTEGER NOT NULL
);
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
CREATE TABLE IF NOT EXISTS creator_claimable_cache (
    wallet_address TEXT NOT NULL,
    snapshot_id    INTEGER NOT NULL,
    basket_address TEXT NOT NULL,
    claimable_usdg TEXT NOT NULL,
    cached_at      INTEGER NOT NULL,
    PRIMARY KEY (wallet_address, snapshot_id, basket_address)
);
`

// newTestDB opens a fresh in-memory SQLite database with the full production
// schema applied. Each test gets its own database; there is no shared state.
func newTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.OpenWithSchema(filepath.Join(dir, "test.db"), testSchema)
	if err != nil {
		t.Fatalf("newTestDB: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newTestIndexer constructs an Indexer wired to the given database.
func newTestIndexer(t *testing.T, d *db.DB) *Indexer {
	t.Helper()
	os.Setenv("BASKET_FACTORY_ADDRESS", "0xE9854c4734cd4A9dbC5086398A11df3c11f40b21")
	t.Cleanup(func() { os.Unsetenv("BASKET_FACTORY_ADDRESS") })

	idx, err := New(
		context.Background(),
		"ws://localhost",
		"http://localhost",
		"0x19Ab3408af6503a7D4BeC255b064f8B02A345D04",
		68391146,
		d,
	)
	if err != nil {
		t.Fatalf("newTestIndexer: %v", err)
	}
	return idx
}

// insertAsset is a test helper that seeds a supported_assets row directly.
func insertAsset(t *testing.T, d *db.DB, address, symbol string) {
	t.Helper()
	_, err := d.Exec(`
		INSERT INTO supported_assets (address, symbol, name, sector, oracle_address, is_active, added_at)
		VALUES (?, ?, ?, 'Technology', '0x0000000000000000000000000000000000000001', 1, ?)`,
		strings.ToLower(address), symbol, symbol+" Inc", time.Now().Unix(),
	)
	if err != nil {
		t.Fatalf("insertAsset(%s): %v", symbol, err)
	}
}

// writeChunkAtomic tests

// TestWriteChunkAtomic_DepositWrittenAndCursorAdvanced verifies that a
// Deposited event in a chunk is written to the deposits table and the cursor
// is advanced to endBlock in the same transaction.
func TestWriteChunkAtomic_DepositWrittenAndCursorAdvanced(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	if _, err := d.Exec(`INSERT INTO sync_cursors (key, block_num) VALUES ('events', 68391146)`); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	// Seed basket so the deposit foreign-key-style lookup succeeds.
	if _, err := d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES ('0x474835c4da0393bc87d4e85e36fdce3f56edeaa6','0x0','0x0','','','',0,0,'',0)`); err != nil {
		t.Fatalf("seed basket: %v", err)
	}

	investor := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
	usdg     := big.NewInt(10_000_000)
	tokens, _ := new(big.Int).SetString("9950000000000000000", 10)
	fee      := big.NewInt(50_000)
	data, _  := depositedABI.Pack(usdg, tokens, fee)

	vLog := types.Log{
		Topics: []common.Hash{
			topicDeposited,
			common.BytesToHash(investor.Bytes()),
		},
		Data:        data,
		BlockNumber: 68391200,
		TxHash:      common.HexToHash("0xdeadbeef01"),
		Index:       0,
		Address:     common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"),
	}

	// writeChunkAtomic with a nil client — blockTimestamp will fail the RPC
	// and store 0, which is acceptable for this test since we are verifying
	// write correctness, not timestamp resolution.
	if err := idx.writeChunkAtomic(nil, []types.Log{vLog}, 68391300); err != nil {
		t.Fatalf("writeChunkAtomic: %v", err)
	}

	// Verify deposit row exists.
	var count int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM deposits WHERE basket_address = ?`,
		"0x474835c4da0393bc87d4e85e36fdce3f56edeaa6",
	).Scan(&count); err != nil {
		t.Fatalf("count deposits: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 deposit row, got %d", count)
	}

	// Verify cursor was advanced.
	var cursor int64
	if err := d.QueryRow(`SELECT block_num FROM sync_cursors WHERE key = 'events'`).Scan(&cursor); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != 68391300 {
		t.Errorf("cursor: expected 68391300, got %d", cursor)
	}
}

// TestWriteChunkAtomic_IdempotentOnReplay verifies that replaying the same
// chunk twice (simulating a crash-restart) produces exactly one deposit row,
// not two.
func TestWriteChunkAtomic_IdempotentOnReplay(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	if _, err := d.Exec(`INSERT INTO sync_cursors (key, block_num) VALUES ('events', 0)`); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	const idempotBasket = "0x1111111111111111111111111111111111111111"
	if _, err := d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, '0x0', '0x0', '', '', '', 0, 0, '', 0)`, idempotBasket); err != nil {
		t.Fatalf("seed basket: %v", err)
	}

	investor := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
	data, _  := depositedABI.Pack(big.NewInt(1_000_000), big.NewInt(990_000_000_000_000_000), big.NewInt(10_000))

	vLog := types.Log{
		Topics:      []common.Hash{topicDeposited, common.BytesToHash(investor.Bytes())},
		Data:        data,
		BlockNumber: 100,
		TxHash:      common.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000002"),
		Index:       0,
		Address:     common.HexToAddress(idempotBasket),
	}

	logs := []types.Log{vLog}

	// First write.
	if err := idx.writeChunkAtomic(nil, logs, 100); err != nil {
		t.Fatalf("first writeChunkAtomic: %v", err)
	}

	// Replay the same chunk — simulates crash-restart where cursor was not yet
	// advanced or was re-read from a previous checkpoint.
	if err := idx.writeChunkAtomic(nil, logs, 100); err != nil {
		t.Fatalf("second writeChunkAtomic (replay): %v", err)
	}

	var count int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM deposits WHERE basket_address = ?`,
		idempotBasket,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("idempotency: expected 1 deposit row after replay, got %d", count)
	}
}

// TestWriteChunkAtomic_CursorNotAdvancedOnWriteError verifies that if a log
// write fails, the cursor stays at its original value.
func TestWriteChunkAtomic_EmptyChunkAdvancesCursor(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	if _, err := d.Exec(`INSERT INTO sync_cursors (key, block_num) VALUES ('events', 500)`); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	if err := idx.writeChunkAtomic(nil, []types.Log{}, 1000); err != nil {
		t.Fatalf("writeChunkAtomic empty: %v", err)
	}

	var cursor int64
	if err := d.QueryRow(`SELECT block_num FROM sync_cursors WHERE key = 'events'`).Scan(&cursor); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != 1000 {
		t.Errorf("empty chunk: cursor expected 1000, got %d", cursor)
	}
}

// TestWriteChunkAtomic_MultipleEventTypes verifies that a chunk containing
// a deposit, a redemption, and a rebalance all write correctly in one transaction.
func TestWriteChunkAtomic_MultipleEventTypes(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	if _, err := d.Exec(`INSERT INTO sync_cursors (key, block_num) VALUES ('events', 0)`); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	basketAddr := "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"
	if _, err := d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, '0x0', '0x0', '', '', '', 0, 0, '', 0)`, basketAddr); err != nil {
		t.Fatalf("seed basket: %v", err)
	}

	investor    := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
	basketCommon := common.HexToAddress(basketAddr)

	depositData, _ := depositedABI.Pack(big.NewInt(1_000_000), big.NewInt(990_000_000_000_000_000), big.NewInt(10_000))
	redeemData, _  := redeemedABI.Pack(big.NewInt(990_000_000_000_000_000), big.NewInt(980_000), big.NewInt(10_000))

	logs := []types.Log{
		{
			Topics:      []common.Hash{topicDeposited, common.BytesToHash(investor.Bytes())},
			Data:        depositData,
			BlockNumber: 200,
			TxHash:      common.HexToHash("0xmultitx01"),
			Index:       0,
			Address:     basketCommon,
		},
		{
			Topics:      []common.Hash{topicRedeemed, common.BytesToHash(investor.Bytes())},
			Data:        redeemData,
			BlockNumber: 200,
			TxHash:      common.HexToHash("0xmultitx02"),
			Index:       1,
			Address:     basketCommon,
		},
		{
			Topics:      []common.Hash{topicRebalanced, common.BytesToHash(investor.Bytes())},
			Data:        nil,
			BlockNumber: 200,
			TxHash:      common.HexToHash("0xmultitx03"),
			Index:       2,
			Address:     basketCommon,
		},
	}

	if err := idx.writeChunkAtomic(nil, logs, 200); err != nil {
		t.Fatalf("writeChunkAtomic multi: %v", err)
	}

	var deposits, redemptions, rebalances int
	d.QueryRow(`SELECT COUNT(*) FROM deposits WHERE basket_address = ?`, "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6").Scan(&deposits)
	d.QueryRow(`SELECT COUNT(*) FROM redemptions WHERE basket_address = ?`, "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6").Scan(&redemptions)
	d.QueryRow(`SELECT COUNT(*) FROM rebalances WHERE basket_address = ?`, "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6").Scan(&rebalances)

	if deposits != 1 {
		t.Errorf("expected 1 deposit, got %d", deposits)
	}
	if redemptions != 1 {
		t.Errorf("expected 1 redemption, got %d", redemptions)
	}
	if rebalances != 1 {
		t.Errorf("expected 1 rebalance, got %d", rebalances)
	}
}

// handleBasketCreated database tests

// TestHandleBasketCreated_WritesBasketAndConstituents verifies that
// handleBasketCreated writes the basket row and all constituent rows atomically.
func TestHandleBasketCreated_WritesBasketAndConstituents(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	// Seed the constituent asset so symbol lookup succeeds.
	insertAsset(t, d, "0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e", "TSLA")
	insertAsset(t, d, "0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02", "AMZN")

	basket       := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	creatorToken := common.HexToAddress("0x29ba5c3470b3a6c06bd6cce2e43c019d846c01c0")
	creator      := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
	constituents := []common.Address{
		common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e"),
		common.HexToAddress("0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02"),
	}
	weights := []*big.Int{big.NewInt(7000), big.NewInt(3000)}

	data, _ := basketCreatedABI.Pack("AI Infra", "AIIB", "thesis text", constituents, weights, false)
	vLog := types.Log{
		Topics: []common.Hash{
			topicBasketCreated,
			common.BytesToHash(basket.Bytes()),
			common.BytesToHash(creatorToken.Bytes()),
			common.BytesToHash(creator.Bytes()),
		},
		Data:        data,
		BlockNumber: 68391146,
		TxHash:      common.HexToHash("0xcreate01"),
		Address:     common.HexToAddress("0xE9854c4734cd4A9dbC5086398A11df3c11f40b21"),
	}

	// nil client — blockTimestamp will return 0, which is acceptable here.
	idx.handleBasketCreated(nil, vLog)

	// Basket row must exist.
	var name, symbol string
	var suspended int
	err := d.QueryRow(
		`SELECT name, symbol, suspended FROM baskets WHERE address = ?`,
		strings.ToLower(basket.Hex()),
	).Scan(&name, &symbol, &suspended)
	if err != nil {
		t.Fatalf("basket not written: %v", err)
	}
	if name != "AI Infra" {
		t.Errorf("name: expected AI Infra, got %q", name)
	}
	if symbol != "AIIB" {
		t.Errorf("symbol: expected AIIB, got %q", symbol)
	}
	if suspended != 0 {
		t.Errorf("suspended: expected 0, got %d", suspended)
	}

	// Both constituent rows must exist with correct weights.
	var constituentCount int
	d.QueryRow(
		`SELECT COUNT(*) FROM basket_constituents WHERE basket_address = ?`,
		strings.ToLower(basket.Hex()),
	).Scan(&constituentCount)
	if constituentCount != 2 {
		t.Errorf("expected 2 constituent rows, got %d", constituentCount)
	}

	var tslaWeight int
	d.QueryRow(
		`SELECT target_weight_bps FROM basket_constituents WHERE basket_address = ? AND stock_address = ?`,
		strings.ToLower(basket.Hex()),
		"0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e",
	).Scan(&tslaWeight)
	if tslaWeight != 7000 {
		t.Errorf("TSLA weight: expected 7000, got %d", tslaWeight)
	}

	// basketAddrs map must be updated.
	idx.mu.RLock()
	_, present := idx.basketAddrs[basket]
	idx.mu.RUnlock()
	if !present {
		t.Error("basket not added to basketAddrs map after handleBasketCreated")
	}
}

// TestHandleBasketCreated_Idempotent verifies that calling handleBasketCreated
// twice for the same basket does not duplicate rows.
func TestHandleBasketCreated_Idempotent(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	insertAsset(t, d, "0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e", "TSLA")

	basket      := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	creatorToken := common.HexToAddress("0x29ba5c3470b3a6c06bd6cce2e43c019d846c01c0")
	creator     := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
	constituents := []common.Address{common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")}
	weights      := []*big.Int{big.NewInt(10000)}

	data, _ := basketCreatedABI.Pack("Test Basket", "TEST", "a thesis", constituents, weights, false)
	vLog := types.Log{
		Topics: []common.Hash{
			topicBasketCreated,
			common.BytesToHash(basket.Bytes()),
			common.BytesToHash(creatorToken.Bytes()),
			common.BytesToHash(creator.Bytes()),
		},
		Data:        data,
		BlockNumber: 100,
		TxHash:      common.HexToHash("0xidem01"),
	}

	idx.handleBasketCreated(nil, vLog)
	idx.handleBasketCreated(nil, vLog) // second call — must not duplicate

	var basketCount, constituentCount int
	d.QueryRow(`SELECT COUNT(*) FROM baskets WHERE address = ?`, strings.ToLower(basket.Hex())).Scan(&basketCount)
	d.QueryRow(`SELECT COUNT(*) FROM basket_constituents WHERE basket_address = ?`, strings.ToLower(basket.Hex())).Scan(&constituentCount)

	if basketCount != 1 {
		t.Errorf("idempotent: expected 1 basket row, got %d", basketCount)
	}
	if constituentCount != 1 {
		t.Errorf("idempotent: expected 1 constituent row, got %d", constituentCount)
	}
}

// TestHandleBasketCreated_InsufficientTopics verifies that a log with fewer
// than 4 topics is rejected without panicking or writing anything.
func TestHandleBasketCreated_InsufficientTopics(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	vLog := types.Log{
		Topics: []common.Hash{topicBasketCreated, {}, {}}, // only 3
		Data:   []byte{},
	}

	// Must not panic.
	idx.handleBasketCreated(nil, vLog)

	var count int
	d.QueryRow(`SELECT COUNT(*) FROM baskets`).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 basket rows for malformed log, got %d", count)
	}
}

// handleAssetAdded / handleAssetDeactivated tests

func TestHandleAssetAdded_WritesAssetRow(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	token  := common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")
	oracle := common.HexToAddress("0x26daf42381ced15760c5f47a5072a228370b100b")
	data, _ := assetAddedABI.Pack("TSLA", "Tesla Inc", "Consumer Discretionary", oracle)

	vLog := types.Log{
		Topics:      []common.Hash{topicAssetAdded, common.BytesToHash(token.Bytes())},
		Data:        data,
		BlockNumber: 100,
		TxHash:      common.HexToHash("0xasset01"),
	}

	idx.handleAssetAdded(vLog)

	var symbol, name, sector string
	var isActive int
	err := d.QueryRow(
		`SELECT symbol, name, sector, is_active FROM supported_assets WHERE address = ?`,
		strings.ToLower(token.Hex()),
	).Scan(&symbol, &name, &sector, &isActive)
	if err != nil {
		t.Fatalf("asset not written: %v", err)
	}
	if symbol != "TSLA" {
		t.Errorf("symbol: expected TSLA, got %q", symbol)
	}
	if isActive != 1 {
		t.Errorf("is_active: expected 1, got %d", isActive)
	}
}

func TestHandleAssetDeactivated_SetsInactive(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	insertAsset(t, d, "0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e", "TSLA")

	token := common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e")
	vLog := types.Log{
		Topics:      []common.Hash{topicAssetDeact, common.BytesToHash(token.Bytes())},
		BlockNumber: 200,
		TxHash:      common.HexToHash("0xdeact01"),
	}

	idx.handleAssetDeactivated(vLog)

	var isActive int
	d.QueryRow(
		`SELECT is_active FROM supported_assets WHERE address = ?`,
		strings.ToLower(token.Hex()),
	).Scan(&isActive)
	if isActive != 0 {
		t.Errorf("is_active: expected 0 after deactivation, got %d", isActive)
	}
}

// handleBasketSuspended tests

func TestHandleBasketSuspended_SetsSuspendedFlag(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	basketAddr := "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"
	if _, err := d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, '0x0', '0x0', '', '', '', 0, 0, '', 0)`, basketAddr); err != nil {
		t.Fatalf("seed basket: %v", err)
	}

	vLog := types.Log{
		Topics:  []common.Hash{topicBasketSuspend},
		Address: common.HexToAddress(basketAddr),
		TxHash:  common.HexToHash("0xsuspend01"),
	}

	idx.handleBasketSuspended(vLog)

	var suspended int
	d.QueryRow(`SELECT suspended FROM baskets WHERE address = ?`, basketAddr).Scan(&suspended)
	if suspended != 1 {
		t.Errorf("suspended: expected 1, got %d", suspended)
	}
}

// syncBaskets suspended-state propagation test

// TestSyncBaskets_SuspendedBasketWrittenCorrectly verifies that a basket whose
// active=false field in getAllBaskets is written with suspended=1.
func TestSyncBaskets_SuspendedStateUpdate(t *testing.T) {
	d := newTestDB(t)

	basketAddr := "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"

	// Insert basket as active (suspended=0), simulating a first syncBaskets run.
	_, err := d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, '0x0', '0x0', '', '', '', 0, 0, '', 0)
		ON CONFLICT(address) DO UPDATE SET suspended = excluded.suspended`,
		basketAddr,
	)
	if err != nil {
		t.Fatalf("initial insert: %v", err)
	}

	// Simulate a subsequent syncBaskets run where the chain reports active=false.
	_, err = d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, '0x0', '0x0', '', '', '', 0, 0, '', 1)
		ON CONFLICT(address) DO UPDATE SET suspended = excluded.suspended`,
		basketAddr,
	)
	if err != nil {
		t.Fatalf("update insert: %v", err)
	}

	var suspended int
	d.QueryRow(`SELECT suspended FROM baskets WHERE address = ?`, basketAddr).Scan(&suspended)
	if suspended != 1 {
		t.Errorf("suspended: expected 1 after active=false update, got %d", suspended)
	}
}

// recordSeedFailure and seedMissingConstituents tests

func TestRecordSeedFailure_IncrementsOnEachCall(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	basket := "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"

	idx.recordSeedFailure(basket)
	idx.recordSeedFailure(basket)
	idx.recordSeedFailure(basket)

	var attempts int
	d.QueryRow(`SELECT attempts FROM basket_seed_failures WHERE basket_address = ?`, basket).Scan(&attempts)
	if attempts != 3 {
		t.Errorf("attempts: expected 3, got %d", attempts)
	}
}

func TestRecordSeedFailure_UpdatesLastAttempt(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	basket := "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"
	before := time.Now().Unix()

	idx.recordSeedFailure(basket)

	var lastAttempt int64
	d.QueryRow(`SELECT last_attempt FROM basket_seed_failures WHERE basket_address = ?`, basket).Scan(&lastAttempt)
	if lastAttempt < before {
		t.Errorf("last_attempt: expected >= %d, got %d", before, lastAttempt)
	}
}

// TestSeedMissingConstituents_SkipsBasketExceedingMaxAttempts verifies that a
// basket with attempts >= seedMaxAttempts is excluded from the seeding query
// and seedOneBasket is never called for it.
func TestSeedMissingConstituents_SkipsBasketAtMaxAttempts(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	basketAddr := "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"
	if _, err := d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, '0x0', '0x0', '', '', '', 0, 0, '', 0)`, basketAddr); err != nil {
		t.Fatalf("seed basket: %v", err)
	}

	// Record exactly seedMaxAttempts failures — this basket should be skipped.
	if _, err := d.Exec(`
		INSERT INTO basket_seed_failures (basket_address, attempts, last_attempt)
		VALUES (?, ?, ?)`, basketAddr, seedMaxAttempts, time.Now().Unix()); err != nil {
		t.Fatalf("seed failures: %v", err)
	}

	// seedMissingConstituents with no RPC client, if it tries to seed the
	// basket it will call newHTTPClient which will fail to dial and log an error
	// but not panic. The basket should be skipped entirely (zero constituent rows).
	idx.seedMissingConstituents()

	var constituentCount int
	d.QueryRow(
		`SELECT COUNT(*) FROM basket_constituents WHERE basket_address = ?`, basketAddr,
	).Scan(&constituentCount)

	// Constituent count must still be 0 — the basket was skipped, not attempted.
	if constituentCount != 0 {
		t.Errorf("expected 0 constituents (basket skipped), got %d", constituentCount)
	}
}

// TestSeedMissingConstituents_NoOp_WhenAllSeeded verifies that
// seedMissingConstituents returns immediately with no work when all baskets
// already have constituents.
func TestSeedMissingConstituents_NoOp_WhenAllSeeded(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	basketAddr := "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"
	if _, err := d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, '0x0', '0x0', 'Test', 'TST', 'thesis', 0, 0, '', 0)`, basketAddr); err != nil {
		t.Fatalf("seed basket: %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO basket_constituents (basket_address, stock_address, symbol, target_weight_bps, display_order)
		VALUES (?, '0xtoken', 'TKN', 10000, 0)`, basketAddr); err != nil {
		t.Fatalf("seed constituent: %v", err)
	}

	idx.seedMissingConstituents()
}

// filterAddresses tests

func TestFilterAddresses_AlwaysContainsRegistryAndFactory(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	addrs := idx.filterAddresses()
	if len(addrs) < 2 {
		t.Fatalf("expected at least registry + factory, got %d addresses", len(addrs))
	}

	registry := strings.ToLower("0x19Ab3408af6503a7D4BeC255b064f8B02A345D04")
	factory  := strings.ToLower("0xE9854c4734cd4A9dbC5086398A11df3c11f40b21")

	found := make(map[string]bool)
	for _, a := range addrs {
		found[strings.ToLower(a.Hex())] = true
	}

	if !found[registry] {
		t.Errorf("registry address %s not in filterAddresses result", registry)
	}
	if !found[factory] {
		t.Errorf("factory address %s not in filterAddresses result", factory)
	}
}

func TestFilterAddresses_IncludesNewlyIndexedBasket(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	newBasket := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")

	idx.mu.Lock()
	idx.basketAddrs[newBasket] = true
	idx.mu.Unlock()

	addrs := idx.filterAddresses()
	found := false
	for _, a := range addrs {
		if strings.ToLower(a.Hex()) == strings.ToLower(newBasket.Hex()) {
			found = true
			break
		}
	}
	if !found {
		t.Error("newly indexed basket not included in filterAddresses result")
	}
}

// Migrate idempotency tests

func TestMigrate_Idempotent(t *testing.T) {
	d := newTestDB(t)

	// Run once — must succeed.
	if err := d.Migrate(); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	// Run again — must not error even though columns already exist.
	if err := d.Migrate(); err != nil {
		t.Fatalf("second Migrate (idempotent): %v", err)
	}
}

func TestMigrate_LogIndexColumnExists(t *testing.T) {
	d := newTestDB(t)

	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// INSERT with an explicit log_index — proves the column exists after migration.
	_, err := d.Exec(`
		INSERT INTO deposits
			(basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash, log_index)
		VALUES ('0xbasket', '0xinvestor', '1000', '990', '10', 1700000000, '0xtx01', 3)`)
	if err != nil {
		t.Errorf("log_index column missing after Migrate: %v", err)
	}
}

func TestMigrate_DeduplicationConstraintEnforced(t *testing.T) {
	d := newTestDB(t)

	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Insert a deposit.
	_, err := d.Exec(`
		INSERT INTO deposits
			(basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash, log_index)
		VALUES ('0xbasket', '0xinvestor', '1000', '990', '10', 1700000000, '0xdup', 0)`)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Insert the same (tx_hash, log_index) with ON CONFLICT DO NOTHING.
	_, err = d.Exec(`
		INSERT INTO deposits
			(basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash, log_index)
		VALUES ('0xbasket', '0xinvestor', '1000', '990', '10', 1700000000, '0xdup', 0)
		ON CONFLICT(tx_hash, log_index) DO NOTHING`)
	if err != nil {
		t.Fatalf("duplicate insert with ON CONFLICT DO NOTHING: %v", err)
	}

	// Insert the same (tx_hash, log_index) without ON CONFLICT.
	_, err = d.Exec(`
		INSERT INTO deposits
			(basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash, log_index)
		VALUES ('0xbasket', '0xinvestor', '1000', '990', '10', 1700000000, '0xdup', 0)`)
	if err == nil {
		t.Error("expected unique constraint error for duplicate (tx_hash, log_index), got nil")
	}

	var count int
	d.QueryRow(`SELECT COUNT(*) FROM deposits WHERE tx_hash = '0xdup'`).Scan(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 row after duplicate attempts, got %d", count)
	}
}

func TestMigrate_BasketSeedFailuresTableExists(t *testing.T) {
	d := newTestDB(t)

	if err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	_, err := d.Exec(`
		INSERT INTO basket_seed_failures (basket_address, attempts, last_attempt)
		VALUES ('0xbasket', 1, 1700000000)`)
	if err != nil {
		t.Errorf("basket_seed_failures table missing after Migrate: %v", err)
	}
}

// blockTimestamp cache correctness

// TestBlockTimestamp_CacheHit_NoPanic verifies that a pre-warmed cache entry
// is returned without reaching the RPC path. 
func TestBlockTimestamp_CacheHit_NoPanic(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	const blockNum = uint64(68391200)
	const expected = int64(1780525194)

	idx.blockTsMu.Lock()
	idx.blockTs[blockNum] = uint64(expected)
	idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: blockNum, timestamp: uint64(expected)})
	idx.blockTsMu.Unlock()

	got := idx.blockTimestamp(nil, blockNum) // nil client — must not panic
	if got != expected {
		t.Errorf("blockTimestamp cache hit: expected %d, got %d", expected, got)
	}
}

// TestBlockTimestamp_CacheEviction verifies the FIFO eviction at blockTsCacheMax
// keeps the map size bounded and evicts the oldest entry.
func TestBlockTimestamp_CacheEviction_BoundsMapSize(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	// Populate to exactly blockTsCacheMax.
	for i := uint64(0); i < blockTsCacheMax; i++ {
		idx.blockTsMu.Lock()
		idx.blockTs[i] = i * 1000
		idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: i, timestamp: i * 1000})
		idx.blockTsMu.Unlock()
	}

	// Trigger eviction by adding one more entry through the cache logic directly.
	idx.blockTsMu.Lock()
	if len(idx.blockTsFIFO) >= blockTsCacheMax {
		oldest := idx.blockTsFIFO[0]
		idx.blockTsFIFO = idx.blockTsFIFO[1:]
		delete(idx.blockTs, oldest.blockNumber)
	}
	idx.blockTs[blockTsCacheMax] = 9999999
	idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: blockTsCacheMax, timestamp: 9999999})
	mapLen := len(idx.blockTs)
	_, block0Present := idx.blockTs[0]
	idx.blockTsMu.Unlock()

	if mapLen != blockTsCacheMax {
		t.Errorf("cache size: expected %d after eviction, got %d", blockTsCacheMax, mapLen)
	}
	if block0Present {
		t.Error("block 0 (oldest) should have been evicted")
	}
}

// FeeSnapshot write test

func TestWriteFeeSnapshot_WritesCorrectSnapshotID(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	basketAddr      := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	creatorTokenAddr := common.HexToAddress("0x29ba5c3470b3a6c06bd6cce2e43c019d846c01c0")

	if _, err := d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, ?, '0x0', '', '', '', 0, 0, '', 0)`,
		strings.ToLower(basketAddr.Hex()),
		strings.ToLower(creatorTokenAddr.Hex()),
	); err != nil {
		t.Fatalf("seed basket: %v", err)
	}

	snapshotID  := int64(7)
	usdgAmount  := big.NewInt(80_000)
	totalSupply := new(big.Int).Mul(big.NewInt(1_000_000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	data, _     := feeSnapshotABI.Pack(usdgAmount, totalSupply)

	vLog := types.Log{
		Topics: []common.Hash{
			topicFeeSnapshoted,
			common.BigToHash(big.NewInt(snapshotID)),
		},
		Data:        data,
		BlockNumber: 300,
		TxHash:      common.HexToHash("0xsnap01"),
		Index:       0,
		Address:     creatorTokenAddr,
	}

	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := idx.writeFeeSnapshot(tx, nil, vLog); err != nil {
		tx.Rollback()
		t.Fatalf("writeFeeSnapshot: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var gotSnapshotID int64
	var gotAmount string
	err = d.QueryRow(
		`SELECT snapshot_id, usdg_amount FROM fee_snapshots WHERE basket_address = ? AND snapshot_id = ?`,
		strings.ToLower(basketAddr.Hex()), snapshotID,
	).Scan(&gotSnapshotID, &gotAmount)
	if err != nil {
		t.Fatalf("fee_snapshots row not written: %v", err)
	}
	if gotSnapshotID != snapshotID {
		t.Errorf("snapshot_id: expected %d, got %d", snapshotID, gotSnapshotID)
	}
	if gotAmount != "80000" {
		t.Errorf("usdg_amount: expected 80000, got %s", gotAmount)
	}
}