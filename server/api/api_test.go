package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/OCD-Labs/Weave/server/api"
	"github.com/OCD-Labs/Weave/server/db"
)

// testSchema mirrors server/db/schema.sql exactly.
// Embedded here to avoid relative path issues when go test runs from the package directory.
const testSchema = `
CREATE TABLE IF NOT EXISTS baskets (
    address              TEXT PRIMARY KEY,
    creator_token_address TEXT NOT NULL,
    creator_address      TEXT NOT NULL,
    name                 TEXT NOT NULL,
    symbol               TEXT NOT NULL,
    thesis               TEXT NOT NULL,
    rebalancing_enabled  INTEGER NOT NULL,
    drift_threshold_bps  INTEGER,
    created_at           INTEGER NOT NULL,
    created_tx           TEXT NOT NULL,
    suspended            INTEGER NOT NULL DEFAULT 0
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
CREATE INDEX IF NOT EXISTS idx_deposits_basket      ON deposits(basket_address);
CREATE INDEX IF NOT EXISTS idx_deposits_investor    ON deposits(investor_address);
CREATE INDEX IF NOT EXISTS idx_redemptions_basket   ON redemptions(basket_address);
CREATE INDEX IF NOT EXISTS idx_redemptions_investor ON redemptions(investor_address);
CREATE INDEX IF NOT EXISTS idx_price_history_addr   ON price_history(stock_address, timestamp);
CREATE INDEX IF NOT EXISTS idx_nav_history_basket   ON nav_history(basket_address, timestamp);
CREATE INDEX IF NOT EXISTS idx_fee_snapshots_basket ON fee_snapshots(basket_address);
`

// newTestDB creates an in-memory SQLite database for testing.
// Each test gets a fresh database — no shared state between tests.
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
	database := newTestDB(t)
	return api.NewRouter(database, "http://localhost:3001", "")
}

// seedTestAssets inserts known assets directly into the test database.
func seedTestAssets(t *testing.T, database *db.DB) {
	t.Helper()
	assets := []struct {
		address, symbol, name, sector, oracle string
	}{
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

func TestGetCatalogue_Empty(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/catalogue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(result) != 0 {
		t.Errorf("expected empty catalogue, got %d assets", len(result))
	}
}

func TestGetCatalogue_WithAssets(t *testing.T) {
	database := newTestDB(t)
	seedTestAssets(t, database)
	router := api.NewRouter(database, "http://localhost:3001", "")

	req := httptest.NewRequest(http.MethodGet, "/catalogue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(result) != 3 {
		t.Errorf("expected 3 assets, got %d", len(result))
	}

	// Verify required fields are present on every asset.
	requiredFields := []string{"address", "symbol", "name", "sector", "oracle", "isActive", "currentPriceUsdg"}
	for _, asset := range result {
		for _, field := range requiredFields {
			if _, ok := asset[field]; !ok {
				t.Errorf("asset missing required field %q: %v", field, asset)
			}
		}
	}
}

func TestGetCatalogue_OrderedBySymbol(t *testing.T) {
	database := newTestDB(t)
	seedTestAssets(t, database)
	router := api.NewRouter(database, "http://localhost:3001", "")

	req := httptest.NewRequest(http.MethodGet, "/catalogue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result []map[string]interface{}
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

func TestGetCatalogueAsset_Found(t *testing.T) {
	database := newTestDB(t)
	seedTestAssets(t, database)
	router := api.NewRouter(database, "http://localhost:3001", "")

	req := httptest.NewRequest(http.MethodGet, "/catalogue/0x71178bac73cbeb415514eb542a8995b82669778d", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)

	if result["symbol"] != "AMD" {
		t.Errorf("expected AMD, got %v", result["symbol"])
	}
}

func TestGetCatalogueAsset_NotFound(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/catalogue/0x0000000000000000000000000000000000000000", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestListBaskets_Empty(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/baskets", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)

	if result == nil || len(result) != 0 {
		t.Errorf("expected empty array, got %v", result)
	}
}

func TestListBaskets_WithData(t *testing.T) {
	database := newTestDB(t)
	router := api.NewRouter(database, "http://localhost:3001", "")

	_, err := database.Exec(`
		INSERT INTO baskets
		(address, creator_token_address, creator_address, name, symbol, thesis,
		 rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"0xabc", "0xdef", "0x123",
		"AI Infrastructure Basket", "AIIB",
		"Companies building AI infrastructure",
		1, 1748720000, "0xtxhash", 0,
	)
	if err != nil {
		t.Fatalf("failed to insert basket: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/baskets", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result) != 1 {
		t.Fatalf("expected 1 basket, got %d", len(result))
	}

	if result[0]["name"] != "AI Infrastructure Basket" {
		t.Errorf("unexpected basket name: %v", result[0]["name"])
	}
}

func TestGetBasket_NotFound(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/baskets/0xdeadbeef", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestGetBasket_Found(t *testing.T) {
	database := newTestDB(t)
	router := api.NewRouter(database, "http://localhost:3001", "")

	_, err := database.Exec(`
		INSERT INTO baskets
		(address, creator_token_address, creator_address, name, symbol, thesis,
		 rebalancing_enabled, created_at, created_tx, suspended)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"0xabc123", "0xdef", "0x456",
		"Test Basket", "TEST", "Test thesis",
		0, 1748720000, "0xtx", 0,
	)
	if err != nil {
		t.Fatalf("failed to insert basket: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/baskets/0xabc123", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)

	if result["symbol"] != "TEST" {
		t.Errorf("expected TEST, got %v", result["symbol"])
	}

	if result["rebalancingEnabled"] != false {
		t.Errorf("expected rebalancingEnabled=false, got %v", result["rebalancingEnabled"])
	}
}

func TestGetPrices_Empty(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/prices", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result) != 0 {
		t.Errorf("expected empty prices, got %d", len(result))
	}
}

func TestGetPrices_WithData(t *testing.T) {
	database := newTestDB(t)
	router := api.NewRouter(database, "http://localhost:3001", "")

	_, err := database.Exec(`
		INSERT INTO price_history (stock_address, price_usdg, timestamp)
		VALUES (?, ?, ?)`,
		"0x71178bac73cbeb415514eb542a8995b82669778d",
		"11000000000",
		1748720000,
	)
	if err != nil {
		t.Fatalf("failed to insert price: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/prices", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	var result []map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)

	if len(result) != 1 {
		t.Fatalf("expected 1 price, got %d", len(result))
	}

	if result[0]["priceUsdg"] != "11000000000" {
		t.Errorf("unexpected price: %v", result[0]["priceUsdg"])
	}
}

func TestGetPortfolio_NoPositions(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/positions/0x4e4b989abe79381c1b8a4871d6af481b175f4865", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)

	positions, ok := result["positions"].([]interface{})
	if !ok {
		t.Fatalf("expected positions array, got %T", result["positions"])
	}

	if len(positions) != 0 {
		t.Errorf("expected empty positions, got %d", len(positions))
	}
}

func TestGetCreatorDashboard_NoBaskets(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/creator/0x4e4b989abe79381c1b8a4871d6af481b175f4865", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]interface{}
	json.NewDecoder(rec.Body).Decode(&result)

	baskets, ok := result["baskets"].([]interface{})
	if !ok {
		t.Fatalf("expected baskets array, got %T", result["baskets"])
	}

	if len(baskets) != 0 {
		t.Errorf("expected no baskets, got %d", len(baskets))
	}
}

func TestAICompose_AgentUnavailable(t *testing.T) {
	// Point to a port nothing is listening on — simulates agent being down.
	database := newTestDB(t)
	router := api.NewRouter(database, "http://localhost:19999", "")

	body := bytes.NewBufferString(`{"thesis": "companies building AI infrastructure"}`)
	req := httptest.NewRequest(http.MethodPost, "/ai/compose", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when agent is down, got %d", rec.Code)
	}
}

func TestCORSHeaders(t *testing.T) {
	router := newTestRouter(t)

	req := httptest.NewRequest(http.MethodOptions, "/catalogue", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for OPTIONS, got %d", rec.Code)
	}

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("expected CORS header, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestResponseContentType(t *testing.T) {
	router := newTestRouter(t)

	endpoints := []string{"/catalogue", "/baskets", "/prices"}
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