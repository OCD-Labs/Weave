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

func TestWriteChunkAtomic_DepositWrittenAndCursorAdvanced(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	if _, err := d.Exec(`INSERT INTO sync_cursors (key, block_num) VALUES ('events', 68391146)`); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
	if _, err := d.Exec(`
		INSERT INTO baskets (address, creator_token_address, creator_address, name, symbol, thesis,
		    rebalancing_enabled, created_at, created_tx, suspended)
		VALUES ('0x474835c4da0393bc87d4e85e36fdce3f56edeaa6','0x0','0x0','','','',0,0,'',0)`); err != nil {
		t.Fatalf("seed basket: %v", err)
	}

	// Pre-populate the in-memory set so the deposit passes the address check.
	idx.mu.Lock()
	idx.basketAddrs[common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")] = true
	idx.mu.Unlock()

	investor  := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
	usdg      := big.NewInt(10_000_000)
	tokens, _ := new(big.Int).SetString("9950000000000000000", 10)
	fee       := big.NewInt(50_000)
	data, _   := depositedABI.Pack(usdg, tokens, fee)

	vLog := types.Log{
		Topics:      []common.Hash{topicDeposited, common.BytesToHash(investor.Bytes())},
		Data:        data,
		BlockNumber: 68391200,
		TxHash:      common.HexToHash("0xdeadbeef01"),
		Index:       0,
		Address:     common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"),
	}

	if err := idx.writeChunkAtomic(nil, []types.Log{vLog}, 68391300); err != nil {
		t.Fatalf("writeChunkAtomic: %v", err)
	}

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

	var cursor int64
	if err := d.QueryRow(`SELECT block_num FROM sync_cursors WHERE key = 'events'`).Scan(&cursor); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != 68391300 {
		t.Errorf("cursor: expected 68391300, got %d", cursor)
	}
}

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

	idx.mu.Lock()
	idx.basketAddrs[common.HexToAddress(idempotBasket)] = true
	idx.mu.Unlock()

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

	if err := idx.writeChunkAtomic(nil, logs, 100); err != nil {
		t.Fatalf("first writeChunkAtomic: %v", err)
	}
	if err := idx.writeChunkAtomic(nil, logs, 100); err != nil {
		t.Fatalf("second writeChunkAtomic (replay): %v", err)
	}

	var count int
	if err := d.QueryRow(
		`SELECT COUNT(*) FROM deposits WHERE basket_address = ?`, idempotBasket,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("idempotency: expected 1 deposit row after replay, got %d", count)
	}
}

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

	idx.mu.Lock()
	idx.basketAddrs[common.HexToAddress(basketAddr)] = true
	idx.mu.Unlock()

	investor     := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
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
	d.QueryRow(`SELECT COUNT(*) FROM deposits WHERE basket_address = ?`, basketAddr).Scan(&deposits)
	d.QueryRow(`SELECT COUNT(*) FROM redemptions WHERE basket_address = ?`, basketAddr).Scan(&redemptions)
	d.QueryRow(`SELECT COUNT(*) FROM rebalances WHERE basket_address = ?`, basketAddr).Scan(&rebalances)

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

// TestWriteChunkAtomic_BasketCreatedBeforeDeposited verifies the pre-pass
// correctly handles the factory emitting Deposited before BasketCreated
// in the same transaction, so the deposit is not dropped.
func TestWriteChunkAtomic_BasketCreatedBeforeDeposited(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	if _, err := d.Exec(`INSERT INTO sync_cursors (key, block_num) VALUES ('events', 0)`); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	insertAsset(t, d, "0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e", "TSLA")
	insertAsset(t, d, "0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02", "AMZN")
	insertAsset(t, d, "0x71178bac73cbeb415514eb542a8995b82669778d", "AMD")

	basket       := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	creatorToken := common.HexToAddress("0x29ba5c3470b3a6c06bd6cce2e43c019d846c01c0")
	creator      := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
	constituents := []common.Address{
		common.HexToAddress("0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e"),
		common.HexToAddress("0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02"),
		common.HexToAddress("0x71178bac73cbeb415514eb542a8995b82669778d"),
	}
	weights := []*big.Int{big.NewInt(5000), big.NewInt(3000), big.NewInt(2000)}

	basketCreatedData, _ := basketCreatedABI.Pack("AI Infra", "AIIB", "thesis", constituents, weights, false)
	basketCreatedLog := types.Log{
		Topics: []common.Hash{
			topicBasketCreated,
			common.BytesToHash(basket.Bytes()),
			common.BytesToHash(creatorToken.Bytes()),
			common.BytesToHash(creator.Bytes()),
		},
		Data:        basketCreatedData,
		BlockNumber: 500,
		TxHash:      common.HexToHash("0xcreatetx01"),
		Index:       2, // emitted last in the transaction
		Address:     common.HexToAddress("0xE9854c4734cd4A9dbC5086398A11df3c11f40b21"),
	}

	// Deposited is emitted before BasketCreated by the factory contract.
	usdg      := big.NewInt(10_000_000)
	tokens, _ := new(big.Int).SetString("9950000000000000000", 10)
	fee       := big.NewInt(50_000)
	depositData, _ := depositedABI.Pack(usdg, tokens, fee)
	depositedLog := types.Log{
		Topics:      []common.Hash{topicDeposited, common.BytesToHash(creator.Bytes())},
		Data:        depositData,
		BlockNumber: 500,
		TxHash:      common.HexToHash("0xcreatetx01"),
		Index:       1, // emitted before BasketCreated
		Address:     basket,
	}

	// Logs arrive in emission order: Deposited first, BasketCreated second.
	logs := []types.Log{depositedLog, basketCreatedLog}

	if err := idx.writeChunkAtomic(nil, logs, 500); err != nil {
		t.Fatalf("writeChunkAtomic: %v", err)
	}

	// Basket row must exist.
	var name string
	if err := d.QueryRow(`SELECT name FROM baskets WHERE address = ?`,
		strings.ToLower(basket.Hex())).Scan(&name); err != nil {
		t.Fatalf("basket not written: %v", err)
	}
	if name != "AI Infra" {
		t.Errorf("basket name: expected AI Infra, got %q", name)
	}

	// Deposit must have been written despite arriving before BasketCreated.
	var depositCount int
	d.QueryRow(`SELECT COUNT(*) FROM deposits WHERE basket_address = ?`,
		strings.ToLower(basket.Hex())).Scan(&depositCount)
	if depositCount != 1 {
		t.Errorf("expected 1 deposit row, got %d — pre-pass failed to make basket known before deposit", depositCount)
	}

	// Address sets must be updated after commit.
	idx.mu.RLock()
	basketKnown := idx.basketAddrs[basket]
	_, ctKnown  := idx.creatorTokenToBasket[creatorToken]
	idx.mu.RUnlock()
	if !basketKnown {
		t.Error("basket not in basketAddrs after commit")
	}
	if !ctKnown {
		t.Error("creator token not in creatorTokenToBasket after commit")
	}
}

// writeBasketCreated tests

func TestWriteBasketCreated_WritesBasketAndConstituents(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

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

	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	local := newChunkAddrs()
	if err := idx.writeBasketCreated(tx, nil, vLog, local); err != nil {
		tx.Rollback()
		t.Fatalf("writeBasketCreated: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var name, symbol string
	var suspended int
	err = d.QueryRow(
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

	// local set must be populated immediately after writeBasketCreated.
	if !local.baskets[basket] {
		t.Error("basket not in chunk-local set after writeBasketCreated")
	}
	if _, ok := local.creatorTokens[creatorToken]; !ok {
		t.Error("creator token not in chunk-local set after writeBasketCreated")
	}
}

func TestWriteBasketCreated_Idempotent(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	insertAsset(t, d, "0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e", "TSLA")

	basket       := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	creatorToken := common.HexToAddress("0x29ba5c3470b3a6c06bd6cce2e43c019d846c01c0")
	creator      := common.HexToAddress("0x4e4b989abe79381c1b8a4871d6af481b175f4865")
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
		Data:    data,
		TxHash:  common.HexToHash("0xidem01"),
		Address: common.HexToAddress("0xE9854c4734cd4A9dbC5086398A11df3c11f40b21"),
	}

	for i := 0; i < 2; i++ {
		tx, err := d.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := idx.writeBasketCreated(tx, nil, vLog, newChunkAddrs()); err != nil {
			tx.Rollback()
			t.Fatalf("writeBasketCreated call %d: %v", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit call %d: %v", i+1, err)
		}
	}

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

func TestWriteBasketCreated_InsufficientTopics(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	vLog := types.Log{
		Topics:  []common.Hash{topicBasketCreated, {}, {}},
		Data:    []byte{},
		Address: common.HexToAddress("0xE9854c4734cd4A9dbC5086398A11df3c11f40b21"),
	}

	tx, err := d.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := idx.writeBasketCreated(tx, nil, vLog, newChunkAddrs()); err != nil {
		tx.Rollback()
		t.Fatalf("writeBasketCreated returned error on insufficient topics: %v", err)
	}
	tx.Commit()

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

	var symbol string
	var isActive int
	err := d.QueryRow(
		`SELECT symbol, is_active FROM supported_assets WHERE address = ?`,
		strings.ToLower(token.Hex()),
	).Scan(&symbol, &isActive)
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

func TestSyncBaskets_SuspendedStateUpdate(t *testing.T) {
	d := newTestDB(t)

	basketAddr := "0x474835c4da0393bc87d4e85e36fdce3f56edeaa6"

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
	if _, err := d.Exec(`
		INSERT INTO basket_seed_failures (basket_address, attempts, last_attempt)
		VALUES (?, ?, ?)`, basketAddr, seedMaxAttempts, time.Now().Unix()); err != nil {
		t.Fatalf("seed failures: %v", err)
	}

	idx.seedMissingConstituents()

	var constituentCount int
	d.QueryRow(
		`SELECT COUNT(*) FROM basket_constituents WHERE basket_address = ?`, basketAddr,
	).Scan(&constituentCount)
	if constituentCount != 0 {
		t.Errorf("expected 0 constituents (basket skipped), got %d", constituentCount)
	}
}

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

// isKnownBasket and isKnownCreatorToken tests

func TestIsKnownBasket_GlobalSet(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	addr := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	idx.mu.Lock()
	idx.basketAddrs[addr] = true
	idx.mu.Unlock()

	if !idx.isKnownBasket(addr, newChunkAddrs()) {
		t.Error("expected isKnownBasket to return true for address in global set")
	}
}

func TestIsKnownBasket_LocalSet(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	addr  := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	local := newChunkAddrs()
	local.baskets[addr] = true

	if !idx.isKnownBasket(addr, local) {
		t.Error("expected isKnownBasket to return true for address in chunk-local set")
	}
}

func TestIsKnownBasket_Unknown(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	addr := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")

	if idx.isKnownBasket(addr, newChunkAddrs()) {
		t.Error("expected isKnownBasket to return false for unknown address")
	}
}

func TestIsKnownCreatorToken_GlobalMap(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	ct     := common.HexToAddress("0x29ba5c3470b3a6c06bd6cce2e43c019d846c01c0")
	basket := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	idx.mu.Lock()
	idx.creatorTokenToBasket[ct] = basket
	idx.mu.Unlock()

	if !idx.isKnownCreatorToken(ct, newChunkAddrs()) {
		t.Error("expected isKnownCreatorToken to return true for address in global map")
	}
}

func TestIsKnownCreatorToken_LocalMap(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	ct     := common.HexToAddress("0x29ba5c3470b3a6c06bd6cce2e43c019d846c01c0")
	basket := common.HexToAddress("0x474835c4da0393bc87d4e85e36fdce3f56edeaa6")
	local  := newChunkAddrs()
	local.creatorTokens[ct] = basket

	if !idx.isKnownCreatorToken(ct, local) {
		t.Error("expected isKnownCreatorToken to return true for address in chunk-local map")
	}
}

// writeFeeSnapshot test

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

	// Pre-populate the global map so writeFeeSnapshot resolves the basket.
	idx.mu.Lock()
	idx.creatorTokenToBasket[creatorTokenAddr] = basketAddr
	idx.mu.Unlock()

	snapshotID  := int64(7)
	usdgAmount  := big.NewInt(80_000)
	totalSupply := new(big.Int).Mul(big.NewInt(1_000_000), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	data, _     := feeSnapshotABI.Pack(usdgAmount, totalSupply)

	vLog := types.Log{
		Topics:      []common.Hash{topicFeeSnapshoted, common.BigToHash(big.NewInt(snapshotID))},
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
	if err := idx.writeFeeSnapshot(tx, nil, vLog, newChunkAddrs()); err != nil {
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

// blockTimestamp cache tests

func TestBlockTimestamp_CacheHit_NoPanic(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	const blockNum = uint64(68391200)
	const expected = int64(1780525194)

	idx.blockTsMu.Lock()
	idx.blockTs[blockNum] = uint64(expected)
	idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: blockNum, timestamp: uint64(expected)})
	idx.blockTsMu.Unlock()

	got := idx.blockTimestamp(nil, blockNum)
	if got != expected {
		t.Errorf("blockTimestamp cache hit: expected %d, got %d", expected, got)
	}
}

func TestBlockTimestamp_CacheEviction_BoundsMapSize(t *testing.T) {
	d := newTestDB(t)
	idx := newTestIndexer(t, d)

	for i := uint64(0); i < blockTsCacheMax; i++ {
		idx.blockTsMu.Lock()
		idx.blockTs[i] = i * 1000
		idx.blockTsFIFO = append(idx.blockTsFIFO, blockTsEntry{blockNumber: i, timestamp: i * 1000})
		idx.blockTsMu.Unlock()
	}

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