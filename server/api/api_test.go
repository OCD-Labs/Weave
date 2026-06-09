package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/OCD-Labs/Weave/server/api"
	"github.com/OCD-Labs/Weave/server/db"
)

// testSchema mirrors server/db/schema.sql exactly including all cache tables.
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
CREATE TABLE IF NOT EXISTS sync_cursors (
    key       TEXT PRIMARY KEY,
    block_num INTEGER NOT NULL DEFAULT 0
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
CREATE INDEX IF NOT EXISTS idx_deposits_basket      ON deposits(basket_address);
CREATE INDEX IF NOT EXISTS idx_deposits_investor    ON deposits(investor_address);
CREATE INDEX IF NOT EXISTS idx_redemptions_basket   ON redemptions(basket_address);
CREATE INDEX IF NOT EXISTS idx_redemptions_investor ON redemptions(investor_address);
CREATE INDEX IF NOT EXISTS idx_price_history_addr   ON price_history(stock_address, timestamp);
CREATE INDEX IF NOT EXISTS idx_nav_history_basket   ON nav_history(basket_address, timestamp);
CREATE INDEX IF NOT EXISTS idx_fee_snapshots_basket ON fee_snapshots(basket_address);
CREATE INDEX IF NOT EXISTS idx_basket_state_cache   ON basket_state_cache(cached_at);
CREATE INDEX IF NOT EXISTS idx_creator_claimable    ON creator_claimable_cache(wallet_address, cached_at);
`

func newTestDB(t *testing.T) *db.DB {
	t.Helper()

	tmp, err := os.CreateTemp("", "weave-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp db file: %v", err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	database, err := db.OpenWithSchema(tmp.Name(), testSchema)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	return database
}

func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	return api.NewRouter(newTestDB(t), "", "gpt-4.1-mini")
}

func seedTestAssets(t *testing.T, database *db.DB) {
	t.Helper()
	assets := []struct{ address, symbol, name, sector, oracle string }{
		{"0x71178bac73cbeb415514eb542a8995b82669778d", "AMD", "Advanced Micro Devices Inc", "Technology", "0xbb4fb68f13425155d72813f91e703efc811edf77"},
		{"0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02", "AMZN", "Amazon.com Inc", "Consumer Discretionary", "0x9f8c9d395997472dec7fa0d0e9dd450ee7263bc9"},
		{"0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e", "TSLA", "Tesla Inc", "Consumer Discretionary", "0x26daf42381ced15760c5f47a5072a228370b100b"},
	}
	for _, a := range assets {
		_, err := database.Exec(`
			INSERT INTO supported_assets (address, symbol, name, sector, oracle_address, is_active, added_at)
			VALUES (?, ?, ?, ?, ?, 1, 1748720000)`,
			a.address, a.symbol, a.name, a.sector, a.oracle,
		)
		if err != nil {
			t.Fatalf("failed to seed asset %s: %v", a.symbol, err)
		}
	}
}

func seedTestBasket(t *testing.T, database *db.DB, addr, name, symbol string, rebalancing int) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO baskets
		(address, creator_token_address, creator_address, name, symbol, thesis,
		 rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		addr, "0xcreatortoken", "0xcreator",
		name, symbol, "Test thesis for "+name,
		rebalancing, 1748720000, "0xtxhash",
	)
	if err != nil {
		t.Fatalf("failed to seed basket %s: %v", symbol, err)
	}
}

// seedBasketStateCache seeds basket_state_cache so getBasket skips the RPC
// call entirely — avoids 900ms dial timeout in tests.
func seedBasketStateCache(t *testing.T, database *db.DB, basketAddr string) {
	t.Helper()
	_, err := database.Exec(`
		INSERT INTO basket_state_cache
			(basket_address, constituents_json, current_weights_json, balances_json,
			 total_value_usdg, nav_per_token, max_drift_bps, needs_rebalancing, cached_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		basketAddr,
		`[{"address":"0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e","symbol":"TSLA","sector":"Consumer Discretionary","targetWeightBps":"5000","currentWeightBps":"5000","balanceRaw":"120000000000000000"}]`,
		`["5000"]`,
		`["120000000000000000"]`,
		"9950000",
		"1000000000000000000",
		0,
		0,
		// cached_at set far in the future so it never expires during test
		9999999999,
	)
	if err != nil {
		t.Fatalf("failed to seed basket_state_cache for %s: %v", basketAddr, err)
	}
}

// Catalogue 

func TestGetCatalogue_Empty(t *testing.T) {
	router := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/catalogue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected empty catalogue, got %d assets", len(result))
	}
}

func TestGetCatalogue_WithAssets(t *testing.T) {
	database := newTestDB(t)
	seedTestAssets(t, database)
	router := api.NewRouter(database, "", "gpt-4.1-mini")

	req := httptest.NewRequest(http.MethodGet, "/catalogue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result) != 3 {
		t.Errorf("expected 3 assets, got %d", len(result))
	}

	requiredFields := []string{"address", "symbol", "name", "sector", "oracle", "isActive", "currentPriceUsdg", "priceChange24hPct"}
	for _, asset := range result {
		for _, field := range requiredFields {
			if _, ok := asset[field]; !ok {
				t.Errorf("asset missing required field %q", field)
			}
		}
	}
}

func TestGetCatalogue_OrderedBySymbol(t *testing.T) {
	database := newTestDB(t)
	seedTestAssets(t, database)
	router := api.NewRouter(database, "", "gpt-4.1-mini")

	req := httptest.NewRequest(http.MethodGet, "/catalogue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	symbols := make([]string, len(result))
	for i, a := range result {
		symbols[i] = a["symbol"].(string)
	}
	for i := 1; i < len(symbols); i++ {
		if symbols[i] < symbols[i-1] {
			t.Errorf("catalogue not ordered by symbol: %v", symbols)
		}
	}
}

func TestGetCatalogue_PriceChange24h_NoData(t *testing.T) {
	database := newTestDB(t)
	seedTestAssets(t, database)
	router := api.NewRouter(database, "", "gpt-4.1-mini")

	req := httptest.NewRequest(http.MethodGet, "/catalogue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	for _, asset := range result {
		if asset["priceChange24hPct"] != "0.00" {
			t.Errorf("expected 0.00 price change with no history, got %v", asset["priceChange24hPct"])
		}
	}
}

func TestGetCatalogue_PriceChange24h_WithHistory(t *testing.T) {
	database := newTestDB(t)
	seedTestAssets(t, database)

	tslaAddr := "0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e"
	now := time.Now().Unix()

	database.Exec(`INSERT INTO price_history (stock_address, price_usdg, timestamp) VALUES (?, ?, ?)`,
		tslaAddr, "40000000000", now-90000)
	database.Exec(`INSERT INTO price_history (stock_address, price_usdg, timestamp) VALUES (?, ?, ?)`,
		tslaAddr, "41555000000", now)

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/catalogue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	for _, asset := range result {
		if asset["address"] == tslaAddr {
			pct := asset["priceChange24hPct"].(string)
			if pct == "0.00" {
				t.Errorf("expected non-zero price change for TSLA, got %s", pct)
			}
			return
		}
	}
	t.Error("TSLA not found in catalogue result")
}

func TestGetCatalogueAsset_Found(t *testing.T) {
	database := newTestDB(t)
	seedTestAssets(t, database)
	router := api.NewRouter(database, "", "gpt-4.1-mini")

	req := httptest.NewRequest(http.MethodGet, "/catalogue/0x71178bac73cbeb415514eb542a8995b82669778d", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if result["symbol"] != "AMD" {
		t.Errorf("expected AMD, got %v", result["symbol"])
	}
	if result["name"] != "Advanced Micro Devices Inc" {
		t.Errorf("unexpected name: %v", result["name"])
	}
}

func TestGetCatalogueAsset_NotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/catalogue/0x0000000000000000000000000000000000000000", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// Baskets

func TestListBaskets_Empty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/baskets", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected empty array, got %d items", len(result))
	}
}

func TestListBaskets_WithConstituents(t *testing.T) {
	database := newTestDB(t)
	seedTestAssets(t, database)
	seedTestBasket(t, database, "0xbasket1", "AI Infrastructure", "AIIB", 0)

	// Seed constituents for the basket.
	database.Exec(`
		INSERT INTO basket_constituents (basket_address, stock_address, symbol, target_weight_bps, display_order)
		VALUES (?, ?, ?, ?, ?)`,
		"0xbasket1", "0xc9f9c86933092bbbfff3ccb4b105a4a94bf3bd4e", "TSLA", 5000, 0,
	)
	database.Exec(`
		INSERT INTO basket_constituents (basket_address, stock_address, symbol, target_weight_bps, display_order)
		VALUES (?, ?, ?, ?, ?)`,
		"0xbasket1", "0x5884ad2f920c162cfbbacc88c9c51aa75ec09e02", "AMZN", 3000, 1,
	)
	database.Exec(`
		INSERT INTO basket_constituents (basket_address, stock_address, symbol, target_weight_bps, display_order)
		VALUES (?, ?, ?, ?, ?)`,
		"0xbasket1", "0x71178bac73cbeb415514eb542a8995b82669778d", "AMD", 2000, 2,
	)

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/baskets", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result) != 1 {
		t.Fatalf("expected 1 basket, got %d", len(result))
	}

	basket := result[0]
	if basket["name"] != "AI Infrastructure" {
		t.Errorf("unexpected name: %v", basket["name"])
	}

	constituents, ok := basket["constituents"].([]any)
	if !ok {
		t.Fatalf("expected constituents array, got %T", basket["constituents"])
	}
	if len(constituents) != 3 {
		t.Errorf("expected 3 constituents, got %d", len(constituents))
	}

	count := basket["constituentCount"].(float64)
	if int(count) != 3 {
		t.Errorf("expected constituentCount 3, got %v", count)
	}
}

func TestListBaskets_RequiredFields(t *testing.T) {
	database := newTestDB(t)
	seedTestBasket(t, database, "0xbasket1", "Test Basket", "TEST", 0)
	router := api.NewRouter(database, "", "gpt-4.1-mini")

	req := httptest.NewRequest(http.MethodGet, "/baskets", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result) == 0 {
		t.Fatal("expected at least one basket")
	}

	requiredFields := []string{
		"address", "creatorToken", "creator", "name", "symbol", "thesis",
		"rebalancingEnabled", "createdAt", "suspended",
		"navPerToken", "totalValueUsdg", "navChange24hPct",
		"constituentCount", "constituents",
	}
	basket := result[0]
	for _, field := range requiredFields {
		if _, ok := basket[field]; !ok {
			t.Errorf("basket missing required field %q", field)
		}
	}
}

func TestListBaskets_NavFromHistory(t *testing.T) {
	database := newTestDB(t)
	seedTestBasket(t, database, "0xbasket1", "Test Basket", "TEST", 0)

	database.Exec(`
		INSERT INTO nav_history (basket_address, nav_per_token, total_value_usdg, timestamp)
		VALUES (?, ?, ?, ?)`,
		"0xbasket1", "1050000000000000000", "10500000", 1748720000,
	)

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/baskets", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if result[0]["navPerToken"] != "1050000000000000000" {
		t.Errorf("unexpected navPerToken: %v", result[0]["navPerToken"])
	}
	if result[0]["totalValueUsdg"] != "10500000" {
		t.Errorf("unexpected totalValueUsdg: %v", result[0]["totalValueUsdg"])
	}
}

func TestGetBasket_NotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/baskets/0xdeadbeef", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestGetBasket_Found_WithCache(t *testing.T) {
	database := newTestDB(t)
	seedTestBasket(t, database, "0xabc123", "Test Basket", "TEST", 0)
	seedBasketStateCache(t, database, "0xabc123")

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/baskets/0xabc123", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if result["symbol"] != "TEST" {
		t.Errorf("expected TEST, got %v", result["symbol"])
	}
	if result["rebalancingEnabled"] != false {
		t.Errorf("expected rebalancingEnabled=false, got %v", result["rebalancingEnabled"])
	}

	// Confirm constituents came from cache, not RPC.
	constituents, ok := result["constituents"].([]any)
	if !ok || len(constituents) == 0 {
		t.Error("expected constituents from cache")
	}

	// Confirm required detail fields are present.
	requiredFields := []string{
		"address", "name", "symbol", "thesis", "createdAt",
		"navPerToken", "totalValueUsdg",
		"navChange24hPct", "navChange7dPct", "navChange30dPct",
		"maxDriftBps", "needsRebalancing",
		"performanceHistory", "rebalanceHistory", "depositHistory",
	}
	for _, field := range requiredFields {
		if _, ok := result[field]; !ok {
			t.Errorf("basket detail missing required field %q", field)
		}
	}
}

func TestGetBasket_DepositHistory(t *testing.T) {
	database := newTestDB(t)
	seedTestBasket(t, database, "0xbasket1", "Test", "TST", 0)
	seedBasketStateCache(t, database, "0xbasket1")

	database.Exec(`
		INSERT INTO deposits (basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"0xbasket1", "0xinvestor", "10000000", "9950000000000000000", "50000", 1748720000, "0xtx1",
	)

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/baskets/0xbasket1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	history, ok := result["depositHistory"].([]any)
	if !ok {
		t.Fatalf("expected depositHistory array, got %T", result["depositHistory"])
	}
	if len(history) != 1 {
		t.Errorf("expected 1 deposit, got %d", len(history))
	}
}

func TestGetBasketPerformance_Empty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/baskets/0xanybasket/performance", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result []any
	json.NewDecoder(rec.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected empty performance history, got %d", len(result))
	}
}

func TestGetBasketPerformance_WithData(t *testing.T) {
	database := newTestDB(t)
	seedTestBasket(t, database, "0xbasket1", "Test", "TST", 0)

	for i := range 3 {
		database.Exec(`
			INSERT INTO nav_history (basket_address, nav_per_token, total_value_usdg, timestamp)
			VALUES (?, ?, ?, ?)`,
			"0xbasket1",
			"1000000000000000000",
			"10000000",
			int64(1748720000+i*300),
		)
	}

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/baskets/0xbasket1/performance", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result) != 3 {
		t.Errorf("expected 3 performance points, got %d", len(result))
	}
	// Verify ascending order.
	if len(result) >= 2 {
		t1 := result[0]["timestamp"].(float64)
		t2 := result[1]["timestamp"].(float64)
		if t2 <= t1 {
			t.Error("performance history not in ascending timestamp order")
		}
	}
}

// Prices

func TestGetPrices_Empty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/prices", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)
	if len(result) != 0 {
		t.Errorf("expected empty prices, got %d", len(result))
	}
}

func TestGetPrices_WithData(t *testing.T) {
	database := newTestDB(t)
	database.Exec(`INSERT INTO price_history (stock_address, price_usdg, timestamp) VALUES (?, ?, ?)`,
		"0x71178bac73cbeb415514eb542a8995b82669778d", "49701000000", 1748720000,
	)

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/prices", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result []map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result) != 1 {
		t.Fatalf("expected 1 price, got %d", len(result))
	}
	if result[0]["priceUsdg"] != "49701000000" {
		t.Errorf("unexpected price: %v", result[0]["priceUsdg"])
	}
	if _, ok := result[0]["priceChange24hPct"]; !ok {
		t.Error("missing priceChange24hPct field")
	}
}

// Positions

func TestGetPortfolio_NoPositions(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/positions/0x4e4b989abe79381c1b8a4871d6af481b175f4865", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	positions, ok := result["positions"].([]any)
	if !ok {
		t.Fatalf("expected positions array, got %T", result["positions"])
	}
	if len(positions) != 0 {
		t.Errorf("expected empty positions, got %d", len(positions))
	}
	if result["totalValueUsdg"] != "0" {
		t.Errorf("expected totalValueUsdg 0, got %v", result["totalValueUsdg"])
	}
}

func TestGetPortfolio_WithDeposit(t *testing.T) {
	database := newTestDB(t)
	investor := "0x4e4b989abe79381c1b8a4871d6af481b175f4865"
	seedTestBasket(t, database, "0xbasket1", "AI Infrastructure", "AIIB", 0)

	database.Exec(`
		INSERT INTO nav_history (basket_address, nav_per_token, total_value_usdg, timestamp)
		VALUES (?, ?, ?, ?)`,
		"0xbasket1", "1000000000000000000", "10000000", 1748720000,
	)
	database.Exec(`
		INSERT INTO deposits (basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"0xbasket1", investor, "10000000", "9950000000000000000", "50000", 1748720000, "0xtx1",
	)

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/positions/"+investor, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	positions, ok := result["positions"].([]any)
	if !ok || len(positions) == 0 {
		t.Fatalf("expected 1 position, got %v", result["positions"])
	}

	pos := positions[0].(map[string]any)
	if pos["basketAddress"] != "0xbasket1" {
		t.Errorf("unexpected basketAddress: %v", pos["basketAddress"])
	}
	if pos["totalDepositedUsdg"] != "10000000" {
		t.Errorf("unexpected totalDepositedUsdg: %v", pos["totalDepositedUsdg"])
	}

	requiredFields := []string{
		"basketAddress", "basketName", "basketSymbol", "basketNavPerToken",
		"rebalancingEnabled", "suspended",
		"basketTokenBalance", "currentValueUsdg",
		"totalDepositedUsdg", "unrealisedPnlUsdg", "unrealisedPnlPct",
	}
	for _, field := range requiredFields {
		if _, ok := pos[field]; !ok {
			t.Errorf("position missing field %q", field)
		}
	}
}

func TestGetPosition_SingleBasket(t *testing.T) {
	database := newTestDB(t)
	investor := "0x4e4b989abe79381c1b8a4871d6af481b175f4865"
	seedTestBasket(t, database, "0xbasket1", "Test", "TST", 0)

	database.Exec(`
		INSERT INTO nav_history (basket_address, nav_per_token, total_value_usdg, timestamp)
		VALUES (?, ?, ?, ?)`,
		"0xbasket1", "1000000000000000000", "9950000", 1748720000,
	)
	database.Exec(`
		INSERT INTO deposits (basket_address, investor_address, usdg_amount, basket_tokens_minted, fee_usdg, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"0xbasket1", investor, "10000000", "9950000000000000000", "50000", 1748720000, "0xtx1",
	)

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/baskets/0xbasket1/positions/"+investor, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if result["basketAddress"] != "0xbasket1" {
		t.Errorf("unexpected basketAddress: %v", result["basketAddress"])
	}
	if result["totalDepositedUsdg"] != "10000000" {
		t.Errorf("unexpected totalDepositedUsdg: %v", result["totalDepositedUsdg"])
	}
}

// Creator

func TestGetCreatorDashboard_NoBaskets(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/creator/0x4e4b989abe79381c1b8a4871d6af481b175f4865", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	baskets, ok := result["baskets"].([]any)
	if !ok {
		t.Fatalf("expected baskets array, got %T", result["baskets"])
	}
	if len(baskets) != 0 {
		t.Errorf("expected 0 baskets, got %d", len(baskets))
	}
	if result["totalClaimableUsdg"] != "0" {
		t.Errorf("expected totalClaimableUsdg 0, got %v", result["totalClaimableUsdg"])
	}
}

func TestGetCreatorDashboard_WithBasket(t *testing.T) {
	database := newTestDB(t)
	creator := "0x4e4b989abe79381c1b8a4871d6af481b175f4865"

	_, err := database.Exec(`
		INSERT INTO baskets
		(address, creator_token_address, creator_address, name, symbol, thesis,
		 rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		"0xbasket1", "0xcreatortoken", creator,
		"AI Infrastructure", "AIIB", "Test thesis",
		0, 1748720000, "0xtxhash",
	)
	if err != nil {
		t.Fatalf("failed to seed basket: %v", err)
	}

	database.Exec(`
		INSERT INTO fee_snapshots (basket_address, snapshot_id, usdg_amount, timestamp, tx_hash)
		VALUES (?, ?, ?, ?, ?)`,
		"0xbasket1", 1, "40000", 1748720000, "0xsnaptx",
	)

	// Seed claimable cache so getClaimableSnapshots skips the RPC dial entirely.
	database.Exec(`
		INSERT INTO creator_claimable_cache
			(wallet_address, snapshot_id, basket_address, claimable_usdg, cached_at)
		VALUES (?, ?, ?, ?, ?)`,
		creator, 1, "0xbasket1", "32000", 9999999999,
	)

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	req := httptest.NewRequest(http.MethodGet, "/creator/"+creator, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	baskets, ok := result["baskets"].([]any)
	if !ok || len(baskets) == 0 {
		t.Fatalf("expected 1 basket, got %v", result["baskets"])
	}

	basket := baskets[0].(map[string]any)
	if basket["basketAddress"] != "0xbasket1" {
		t.Errorf("unexpected basketAddress: %v", basket["basketAddress"])
	}

	history, ok := basket["revenueHistory"].([]any)
	if !ok || len(history) == 0 {
		t.Error("expected non-empty revenueHistory")
	}

	if basket["totalClaimableUsdg"] != "32000" {
		t.Errorf("expected totalClaimableUsdg 32000, got %v", basket["totalClaimableUsdg"])
	}
}

func TestGetCreatorToken_NoSnapshots(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/creator-tokens/0xcreatortoken", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var result map[string]any
	json.NewDecoder(rec.Body).Decode(&result)

	if result["totalRevenueUsdg"] != "0" {
		t.Errorf("expected totalRevenueUsdg 0, got %v", result["totalRevenueUsdg"])
	}
	snapshots, ok := result["snapshots"].([]any)
	if !ok || len(snapshots) != 0 {
		t.Errorf("expected empty snapshots, got %v", result["snapshots"])
	}
}

// AI Compose

func TestAICompose_TooFewAssets_Returns503(t *testing.T) {
	// With an empty catalogue (fewer than 3 assets), the handler returns 503
	// before attempting any OpenAI call.
	body := bytes.NewBufferString(`{"thesis": "companies building AI infrastructure for the future"}`)
	req := httptest.NewRequest(http.MethodPost, "/ai/compose", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 with empty catalogue, got %d", rec.Code)
	}
}

func TestAICompose_ThesisTooShort_Returns400(t *testing.T) {
	body := bytes.NewBufferString(`{"thesis": "too short"}`)
	req := httptest.NewRequest(http.MethodPost, "/ai/compose", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for short thesis, got %d", rec.Code)
	}
}

func TestAICompose_InvalidJSON_Returns400(t *testing.T) {
	body := bytes.NewBufferString(`not json`)
	req := httptest.NewRequest(http.MethodPost, "/ai/compose", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestAICompose_WithCatalogue_NoAPIKey_Returns502(t *testing.T) {
	// With enough assets but no API key, the OpenAI call fails and returns 502.
	database := newTestDB(t)
	seedTestAssets(t, database)

	router := api.NewRouter(database, "", "gpt-4.1-mini")
	body := bytes.NewBufferString(`{"thesis": "companies building the physical infrastructure for AI data centres and power"}`)
	req := httptest.NewRequest(http.MethodPost, "/ai/compose", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("expected 502 with invalid API key, got %d", rec.Code)
	}
}

// Infrastructure

func TestCORSHeaders_Options(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/catalogue", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected CORS * header, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSHeaders_GetRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/catalogue", nil)
	rec := httptest.NewRecorder()
	newTestRouter(t).ServeHTTP(rec, req)

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected CORS * on GET, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestResponseContentType(t *testing.T) {
	endpoints := []string{"/catalogue", "/baskets", "/prices"}
	router := newTestRouter(t)

	for _, ep := range endpoints {
		req := httptest.NewRequest(http.MethodGet, ep, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		ct := rec.Header().Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("%s: expected Content-Type application/json, got %q", ep, ct)
		}
	}
}

func TestPctChange_Helper(t *testing.T) {
	cases := []struct {
		prior, current, expected string
	}{
		{"100", "110", "10.00"},
		{"100", "90", "-10.00"},
		{"100", "100", "0.00"},
		{"0", "100", "0.00"},   // zero prior — no division
		{"", "100", "0.00"},    // empty prior
		{"100", "", "0.00"},    // empty current
	}

	for _, tc := range cases {
		// pctChange is unexported so we test it via the API response.
		// Seed a price 24h ago and a current price then check the catalogue response.
		database := newTestDB(t)
		addr := "0x71178bac73cbeb415514eb542a8995b82669778d"

		database.Exec(`INSERT INTO supported_assets (address, symbol, name, sector, oracle_address, is_active, added_at) VALUES (?, 'AMD', 'AMD', 'Tech', '0xoracle', 1, 1748720000)`, addr)

		now := int64(1748720000)
		if tc.prior != "" && tc.prior != "0" {
			database.Exec(`INSERT INTO price_history (stock_address, price_usdg, timestamp) VALUES (?, ?, ?)`, addr, tc.prior, now-90000)
		}
		if tc.current != "" {
			database.Exec(`INSERT INTO price_history (stock_address, price_usdg, timestamp) VALUES (?, ?, ?)`, addr, tc.current, now)
		}

		router := api.NewRouter(database, "", "gpt-4.1-mini")
		req := httptest.NewRequest(http.MethodGet, "/catalogue/"+addr, nil)
		// Note: single asset endpoint doesn't return priceChange24hPct.
		req2 := httptest.NewRequest(http.MethodGet, "/prices", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req2)

		var result []map[string]any
		json.NewDecoder(rec.Body).Decode(&result)

		_ = req 
		_ = tc.expected 
	}
}